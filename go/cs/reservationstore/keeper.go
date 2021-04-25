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

package reservationstore

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"time"

	"github.com/scionproto/scion/go/cs/reservation/conf"
	seg "github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/pathpol"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
)

// This file defines the entries that the keeper looks after.
// Per entry, the keeper will setup new reservations:
// - look for compatible reservations in our store; these will be renewed. If not enough, go on.
// - using all the compatible paths to destination:
// 	 - emit as many requests as needed to fill minActiveRsvs.
//   - wait for success or keep on emitting requests, until he have enough successful minActiveRsvs.
//
// Per entry, the keeper will renew all the associated reservations.
// - renew each reservation
// - if failure, remove it from the active reservations association.
// - the next pass of the keeper will check if we have enough active reservations.
//
// The keeper always knows the nearest point in time when a reservation will expire.

type keeper struct {
	manager     Manager
	entries     map[addr.IA][]activeEntry
	minDuration time.Duration // min validity in the future for the reservations
	// TODO(juagargi) use minDuration in both the setup and in the renew, so that we always have indices ready
}

func NewKeeper(manager Manager, conf conf.Reservations) (
	*keeper, error) {

	entries, err := parseInitial(conf)
	if err != nil {
		return nil, err
	}
	return &keeper{
		manager:     manager,
		entries:     entries,
		minDuration: time.Minute,
	}, nil
}

func (k *keeper) OneShot(ctx context.Context) error {
	wg := sync.WaitGroup{}
	for dst, entries := range k.entries {
		dst, entries := dst, entries
		wg.Add(1)
		go func() {
			defer log.HandlePanic()
			defer wg.Done()
			scionPaths, err := k.manager.PathsTo(dst)
			if err != nil {
				log.Error("keeping the reservations", "err", err)
			}
			err = k.keepDestination(ctx, dst, entries, scionPaths)
			if err != nil {
				log.Error("keeping the reservations", "err", err)
			}
		}()
	}
	wg.Wait() // TODO(juagargi) use timeouts instead of blocking forever in all calls to wg.Wait()
	return nil
}

func (k *keeper) keepDestination(ctx context.Context, dstIA addr.IA, entries []activeEntry,
	paths []snet.PathInterfacesHaver) error {

	// get reservations once and pass them along.
	rsvs, err := k.manager.Store().GetSegmentRsvsFromSrcDstIA(ctx, k.manager.LocalIA(), dstIA)
	if err != nil {
		return err
	}
	// setup new reservations
	err = k.setupsPerDestination(ctx, dstIA, entries, paths, rsvs)
	if err != nil {
		return serrors.WrapStr("keeping destination", err, "dst", dstIA)
	}
	// renew the reservations
	// TODO(juagargi)
	//
	//

	return nil
}

// setupsPerDestination process all entries for a given IA sequentially, to avoid
// reservation racing. It creates the setup requests and later sends them.
func (k *keeper) setupsPerDestination(ctx context.Context, dstIA addr.IA, entries []activeEntry,
	paths []snet.PathInterfacesHaver, currentRsvs []*seg.Reservation) error {

	for _, entry := range entries {
		entry.mutex.Lock()
		defer entry.mutex.Unlock()

		// filter reservations
		atLeastUntil := k.manager.Now().Add(k.minDuration)
		fullyCompliant, askNewIndices, notCompliant :=
			entry.SplitByCompliance(currentRsvs, atLeastUntil)
		entry.activeRsvs = fullyCompliant
		// report not compliant ones; don't delete them, they will expire eventually.
		if len(notCompliant) > 0 {
			log.Info("Non compliant reservations found (a change in requirements?)",
				"count", len(notCompliant))
			for _, rsv := range notCompliant {
				log.Info("not compliant rsv", "id", rsv.ID)
			}
		}
		// new indices:
		if err := k.askNewIndices(ctx, askNewIndices, dstIA, entry); err != nil {
			return err
		}
		// totally new reservations:
		var requestCount int = minActiveRsvs - len(entry.activeRsvs)
		if err := k.askNewReservations(ctx, requestCount, dstIA, entry, paths); err != nil {
			return err
		}

	}
	return nil
}

// askNewIndices will prepare requests based on existing reservations and ask for new indices.
func (k *keeper) askNewIndices(ctx context.Context, rsvs []*seg.Reservation, dstIA addr.IA,
	entry activeEntry) error {

	// TODO(juagargi) test this function (the indices as seen in requests should be active+1)
	if len(rsvs) > 0 {
		paths := make([]snet.PathInterfacesHaver, len(rsvs))
		for i, rsv := range rsvs {
			paths[i] = rsv.Path
		}
		requests, err := entry.PrepareSetupRequests(paths)
		if err != nil {
			return serrors.WrapStr("cannot setup new reservations", err, "paths", paths)
		}
		// add indices
		for i, req := range requests {
			if activeIndex := rsvs[i].ActiveIndex(); activeIndex != nil {
				req.Index = activeIndex.Idx.Add(1)
			}
		}
		// this will block until successfully finished
		return k.requestNSuccessfulRsvs(ctx, dstIA, entry, requests, len(paths))
	}
	return nil
}

// askNewReservations creates new requests based on the paths and the entry and ensures
// that at least `requiredSuccesful` are succesful.
func (k *keeper) askNewReservations(ctx context.Context, requiredSuccesful int, dstIA addr.IA,
	entry activeEntry, paths []snet.PathInterfacesHaver) error {

	// TODO(juagargi) test this function (indices seen in requests should always be zero)
	if requiredSuccesful > 0 {
		requests, err := entry.PrepareSetupRequests(paths)
		if err != nil {
			return serrors.WrapStr("cannot setup new reservations", err, "paths", paths)
		}
		// this will block until successfully finished
		return k.requestNSuccessfulRsvs(ctx, dstIA, entry, requests, requiredSuccesful)
	}
	return nil
}

// requestNSuccessfulRsvs uses the manager to request reservations in parallel, until
// the pending count is reached, or there are no more available requests.
func (k *keeper) requestNSuccessfulRsvs(ctx context.Context, dstIA addr.IA, entry activeEntry,
	requests []*seg.SetupReq, pendingCount int) error {

	var setups []*seg.SetupReq
	for pendingCount > 0 && len(requests) > 0 {
		indices := entry.SelectRequests(requests, pendingCount)
		setups, requests = splitRequests(requests, indices)
		rsvs, errs := k.manager.RequestMany(ctx, setups)
		if len(errs) > 0 {
			log.Info("errors while requesting reservations", "errs", errs)
		}
		pendingCount -= len(rsvs)
	}
	if pendingCount > 0 {
		return serrors.New("could not request the minimum required of reservations", "dst", dstIA)
	}
	return nil
}

// activeEntry is a 1 to 1 association to a conf.ReservationEntry
type activeEntry struct {
	mutex        *sync.Mutex
	requirements entryRequirements
	activeRsvs   []*seg.Reservation
	// minActiveRsvs int // TODO(juagargi) allow to setup a value here instead of the const minActiveRsvs
}

const minActiveRsvs = 1

// SplitByCompliance will split the reservations into three groups:
// compliant, could be compliant and not compliant, according to the requirements of this entry.
// For an explanation of compliance, see the type `Compliance`.
func (e *activeEntry) SplitByCompliance(rsvs []*seg.Reservation, atLeastUntil time.Time) (
	[]*seg.Reservation, []*seg.Reservation, []*seg.Reservation) {

	compliant := make([]*seg.Reservation, 0)
	couldBeCompliant := make([]*seg.Reservation, 0)
	neverCompliant := make([]*seg.Reservation, 0)
	for _, rsv := range rsvs {
		compliance := e.requirements.Compliance(rsv, atLeastUntil)
		switch compliance {
		case Compliant:
			compliant = append(compliant, rsv)
		case CouldBeCompliant:
			couldBeCompliant = append(couldBeCompliant, rsv)
		case NeverCompliant:
			neverCompliant = append(neverCompliant, rsv)
		}
	}
	return compliant, couldBeCompliant, neverCompliant
}

// PrepareSetupRequests creates new reservation requests compliant with the requirements.
// This function creates as many reservations requests as there are
// scion paths compatible with the requirements.
func (e *activeEntry) PrepareSetupRequests(ifaces []snet.PathInterfacesHaver) (
	[]*seg.SetupReq, error) {

	// filter paths
	filtered := e.requirements.predicate.EvalInterfaces(ifaces)
	requests := make([]*seg.SetupReq, len(filtered))
	// create setup requests
	for i, p := range filtered {
		opaque, err := seg.NewOpaquePathFromInterfaces(p.Interfaces())
		if err != nil {
			return nil, err
		}
		// TODO(juagargi) complete filling up the request (exp time, etc)
		req := &seg.SetupReq{
			Request:    seg.Request{},
			MinBW:      e.requirements.minBW,
			MaxBW:      e.requirements.maxBW,
			SplitCls:   e.requirements.splitCls,
			PathProps:  e.requirements.endProps,
			AllocTrail: reservation.AllocationBeads{},
			PathToDst:  opaque,
		}
		requests[i] = req
	}
	return requests, nil
}

func (e *activeEntry) SelectRequests(requests []*seg.SetupReq, n int) []int {
	if n > len(requests) {
		n = len(requests)
	}
	rand.Seed(time.Now().UnixNano()) // TODO(juagargi) select using better criteria
	return rand.Perm(n)
}

type entryRequirements struct {
	predicate *pathpol.Sequence
	minBW     reservation.BWCls
	maxBW     reservation.BWCls
	splitCls  reservation.SplitCls
	endProps  reservation.PathEndProps
}

type Compliance int

const (
	NeverCompliant   = Compliance(iota) // reservation values always out of compliance
	CouldBeCompliant                    // ask for a new index
	Compliant                           // already has an index
)

func (c Compliance) String() string {
	switch c {
	case NeverCompliant:
		return "NeverCompliant"
	case CouldBeCompliant:
		return "CouldBeCompliant"
	case Compliant:
		return "Compliant"
	default:
		panic(fmt.Errorf("unknown value for compliance %d", c))
	}
}

// Compliance checks the given reservation against the requirements and returns true if
// it satisfies them, plus the reservation is good at least until the time in `atLeastUntil`.
func (r entryRequirements) Compliance(rsv *seg.Reservation, atLeastUntil time.Time) Compliance {
	switch {
	case rsv.TrafficSplit != r.splitCls:
		return NeverCompliant
	case rsv.PathEndProps != r.endProps:
		return NeverCompliant
	case len(r.predicate.EvalInterfaces([]snet.PathInterfacesHaver{rsv.Path})) == 0:
		return NeverCompliant
	}
	indices := rsv.Indices.Filter(
		seg.ByExpiration(atLeastUntil),
		seg.NotSwitchableFrom(rsv.ActiveIndex()),
		seg.ByMinBW(r.minBW),
		seg.ByMaxBW(r.maxBW),
	)
	if len(indices) == 0 {
		return CouldBeCompliant
	}
	return Compliant
}

// splitRequests takes a slice of requests and indices, and returns two slices:
// first, those elements in the indices, in the exact order as specified in indices.
// second, all the other elements.
func splitRequests(requests []*seg.SetupReq, indices []int) (
	[]*seg.SetupReq, []*seg.SetupReq) {
	a := make([]*seg.SetupReq, len(indices))
	b := append(requests[:0:0], requests...)
	for i, idx := range indices {
		a[i] = requests[idx]
		b[idx] = b[len(b)-i-1]
	}
	return a, b[:len(b)-len(a)]
}

func parseInitial(conf conf.Reservations) (map[addr.IA][]activeEntry, error) {
	initial := make(map[addr.IA][]activeEntry)
	for _, r := range conf.Rsvs {
		seq, err := pathpol.NewSequence(r.PathPredicate)
		if err != nil {
			return nil, err
		}

		initial[r.DstAS] = append(initial[r.DstAS], activeEntry{
			requirements: entryRequirements{
				predicate: seq,
				minBW:     r.MinSize,
				maxBW:     r.MaxSize,
				splitCls:  r.SplitCls,
				endProps:  r.EndProps.PathEndProps,
			},
			mutex: new(sync.Mutex),
		})
	}
	return initial, nil
}
