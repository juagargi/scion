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
)

type Reservation struct {
	runner *periodic.Runner

	daemon      sciond.Connector
	request     *colibri.E2EReservationSetup
	colibriPath snet.Path
	onError     func(rsv *Reservation, err error)
}

// NewReservation
// The list of less functions is used to sort the full trips. The i+1 function
// is applied before the ith one, so the ith function takes preference over the i+1 (i.e. it's
// "more important" to the sorting).
func NewReservation(ctx context.Context, daemon sciond.Connector,
	dstIA addr.IA, bw reservation.BWCls, index reservation.IndexNumber,
	lessFcns ...func(a, b colibri.FullTrip) bool) (*Reservation, error) {

	// 1. list segments from sciond
	stitchable, err := daemon.ColibriListRsvs(ctx, dstIA)
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
	localIA, err := daemon.LocalIA(ctx)
	if err != nil {
		return nil, serrors.WrapStr("creating reservation setup", err)
	}
	setupReq := &colibri.E2EReservationSetup{
		Id: reservation.ID{
			ASID:   localIA.A,
			Suffix: make([]byte, 12), // TODO(juagargi) FIXME deleteme suffixes are 12 bytes long now!!! check everywhere
		},
		SrcIA:       localIA,
		DstIA:       dstIA,
		Index:       index,
		Segments:    trip.Segments(),
		RequestedBW: bw,
	}
	rand.Read(setupReq.Id.Suffix) // random suffix
	return &Reservation{
		daemon:  daemon,
		request: setupReq,
	}, nil
}

// e2eRenewalTaskDuration is only a convenient way to modify the task duration for the tests.
// Since it's not exported, the compiler should see it's not reassigned via SSA, and just
// treat it as a constant when not running a test.
var e2eRenewalTaskDuration time.Duration = reservation.TicksInE2ERsv *
	reservation.DurationPerTick / 2

// StartReservation periodically sets up/renews the reservation. Returns error iff the setup failed.
// On renewal error, it runs the callback and stops the periodic renewal.
func (r *Reservation) StartReservation(ctx context.Context,
	onError func(rsv *Reservation, err error)) error {

	if r.runner != nil {
		return nil
	}

	var err error
	r.colibriPath, err = r.daemon.ColibriSetupRsv(ctx, r.request)
	if err != nil {
		return serrors.WrapStr("first reservation setup failed", err)
	}

	r.onError = onError
	r.runner = periodic.Start(&renewalTask{
		reservation: r,
	}, e2eRenewalTaskDuration, e2eRenewalTaskDuration)
	return nil
}

func (r *Reservation) StopReservation(ctx context.Context) error {
	if r.runner == nil {
		return nil
	}
	r.runner.Stop()
	r.runner = nil

	return r.daemon.ColibriCleanupRsv(ctx, &r.request.Id, r.request.Index)
}

type renewalTask struct {
	reservation *Reservation
}

func (t *renewalTask) Name() string {
	return "colibri_renewal_task"
}

func (t *renewalTask) Run(ctx context.Context) {
	t.reservation.request.Index = t.reservation.request.Index.Add(1)
	colibriPath, err := t.reservation.daemon.ColibriSetupRsv(ctx, t.reservation.request)
	if err == nil {
		t.reservation.colibriPath = colibriPath
		return
	}
	t.reservation.onError(t.reservation, err)
	// because it failed, stop the task (ourselves). Different routine for it (or deadlock)
	go func() {
		defer log.HandlePanic()
		t.reservation.runner.Stop() // blocks until the task exits. The task is this Run function
	}()
}
