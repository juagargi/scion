// Copyright 2021 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//   http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package client

import (
	"context"
	"crypto/rand"
	"net"
	"sort"
	"time"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/periodic"
	"github.com/scionproto/scion/go/lib/sciond"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/spath"
)

type Reservation struct {
	runner                 *periodic.Runner
	e2eRenewalTaskDuration time.Duration // tests modify this value

	network     *snet.SCIONNetwork
	daemon      sciond.Connector
	dstAddr     *snet.UDPAddr
	request     *colibri.E2EReservationSetup
	currentTrip *colibri.FullTrip // current trip for current setup
	connection  *snet.Conn
	colibriPath snet.Path
	onError     RenewalError
}

// LessFunction is used to sort full trips. The function should return true if
// a is preferred over b; false otherwise.
type LessFunction func(a, b colibri.FullTrip) bool

// RenewalError is a function that is called whenever there is an error during renewal.
// If it returns a FullTrip, the Reservation will try a new setup with it.
type RenewalError func(*Reservation, error) *colibri.FullTrip

var _ snet.Path = (*Reservation)(nil)

// NewReservation
// The list of less functions is used to sort the full trips. The i+1 function
// is applied before the ith one, so the ith function takes preference over the i+1 (i.e. it's
// "more important" to the sorting).
func NewReservation(ctx context.Context, network *snet.SCIONNetwork, daemon sciond.Connector,
	dstAddr *snet.UDPAddr, bw reservation.BWCls, index reservation.IndexNumber,
	lessFcns ...LessFunction) (*Reservation, error) {

	// 1. list segments from sciond
	stitchable, err := daemon.ColibriListRsvs(ctx, dstAddr.IA)
	if err != nil {
		return nil, err
	}
	// 2. stitch segments according to lessFcn
	trips := colibri.CombineAll(stitchable)
	if len(trips) == 0 {
		return nil, serrors.New("no available stitched reservation to dst")
	}

	// sort using the functions in their reverse order (0th is most important, then 1st, etc)
	for i := len(lessFcns) - 1; i >= 0; i-- {
		lessFcn := lessFcns[i]
		sort.SliceStable(trips, func(a, b int) bool {
			return lessFcn(*trips[a], *trips[b])
		})
	}
	trip := trips[0]
	// 3. create setup reservation
	setupReq := &colibri.E2EReservationSetup{
		Id: reservation.ID{
			ASID:   network.LocalIA.A,
			Suffix: make([]byte, 12), // TODO(juagargi) FIXME deleteme suffixes are 12 bytes long now!!! check everywhere
		},
		SrcIA:       network.LocalIA,
		DstIA:       dstAddr.IA,
		Index:       index,
		Segments:    trip.Segments(),
		RequestedBW: bw,
	}
	rand.Read(setupReq.Id.Suffix) // random suffix
	return &Reservation{
		network:     network,
		daemon:      daemon,
		dstAddr:     dstAddr.Copy(),
		request:     setupReq,
		currentTrip: trip,
		// e2eRenewalTaskDuration is only a convenient way to modify the task duration at tests.
		// Since it's not exported, the compiler should see it's not reassigned via SSA, and just
		// treat it as a constant when not running a test.
		e2eRenewalTaskDuration: reservation.TicksInE2ERsv * reservation.DurationPerTick / 2,
	}, nil
}

// Open periodically sets up/renews the reservation. Returns error iff the setup failed.
// On renewal error, it runs the callback and stops the periodic renewal if said
// function returns nil. If it returns a FullTrip, it is used to try to setup a new reservation.
func (r *Reservation) Open(ctx context.Context, localAddr *net.UDPAddr,
	fallbackFcn RenewalError) error {

	if r.runner != nil {
		return nil
	}

	var err error
	r.colibriPath, err = r.daemon.ColibriSetupRsv(ctx, r.request)
	if err != nil {
		return serrors.WrapStr("first reservation setup failed", err)
	}
	r.dstAddr.NextHop = r.colibriPath.UnderlayNextHop()
	// replace the path in the destination address
	r.dstAddr.Path = r.colibriPath.Path()

	r.connection, err = r.network.Dial(ctx, "udp", localAddr, r.dstAddr, addr.SvcNone)
	if err != nil {
		return err
	}

	r.onError = fallbackFcn
	r.runner = periodic.Start(&renewalTask{
		reservation: r,
	}, r.e2eRenewalTaskDuration, r.e2eRenewalTaskDuration)
	return nil
}

func (r *Reservation) Close(ctx context.Context) error {
	if r.runner == nil {
		return nil
	}
	if err := r.connection.Close(); err != nil {
		return err
	}
	r.runner.Stop()
	r.runner = nil

	return r.daemon.ColibriCleanupRsv(ctx, &r.request.Id, r.request.Index)
}

func (r *Reservation) CurrentTrip() colibri.FullTrip {
	return *r.currentTrip.Copy()
}

// Read allows reading from the connection associated to the reservation.
// TODO(juagargi) with the current architecture this doesn't make huge sense, as the reservation
// has a direction.
func (r *Reservation) Read(buff []byte) (int, error) {
	return r.connection.Read(buff)
}

func (r *Reservation) Write(buffer []byte) (int, error) {
	return r.connection.WriteTo(buffer, r.dstAddr)
}

func (r *Reservation) UnderlayNextHop() *net.UDPAddr {
	return r.colibriPath.UnderlayNextHop()
}

func (r *Reservation) Path() spath.Path {
	return r.colibriPath.Path()
}

func (r *Reservation) Destination() addr.IA {
	return r.colibriPath.Destination()
}

func (r *Reservation) Metadata() *snet.PathMetadata {
	return r.colibriPath.Metadata()
}

// Copy is disallowed for a Reservation.
func (r *Reservation) Copy() snet.Path {
	panic("only one copy of a reservation must exist")
}

type renewalTask struct {
	reservation *Reservation
}

func (t *renewalTask) Name() string {
	return "colibri_renewal_task"
}

func (t *renewalTask) Run(ctx context.Context) {
	t.reservation.request.Index = t.reservation.request.Index.Add(1)
	for {
		colibriPath, err := t.reservation.daemon.ColibriSetupRsv(ctx, t.reservation.request)
		if err == nil {
			t.reservation.colibriPath = colibriPath
			// replace the path in the destination address
			t.reservation.dstAddr.Path = colibriPath.Path()
			return
		}
		trip := t.reservation.onError(t.reservation, err)
		if trip == nil {
			break // no fallback
		}
		t.reservation.request.Segments = trip.Segments()
	}
	// because it failed, stop the task (ourselves). Different routine for it (or deadlock)
	go func() {
		defer log.HandlePanic()
		t.reservation.runner.Stop() // blocks until the task exits. The task is this Run function
		t.reservation.runner = nil
	}()
	_ = t.reservation.connection.Close() // ignore errors
	t.reservation.connection = nil
}

func NewReservationForTesting(
	runner *periodic.Runner,
	e2eRenewalTaskDuration time.Duration,
	network *snet.SCIONNetwork,
	daemon sciond.Connector,
	dstAddr *snet.UDPAddr,
	request *colibri.E2EReservationSetup,
	currentTrip *colibri.FullTrip,
	connection *snet.Conn,
	colibriPath snet.Path,
	onError RenewalError) *Reservation {

	return &Reservation{
		runner:                 runner,
		e2eRenewalTaskDuration: e2eRenewalTaskDuration,
		network:                network,
		daemon:                 daemon,
		dstAddr:                dstAddr,
		request:                request,
		currentTrip:            currentTrip,
		connection:             connection,
		colibriPath:            colibriPath,
		onError:                onError,
	}
}
