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
	"math/rand"
	"sync"
	"time"

	"github.com/scionproto/scion/go/cs/reservation/conf"
	"github.com/scionproto/scion/go/cs/reservation/segment"
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
// reservation racing.
func (k *keeper) setupsPerDestination(ctx context.Context, dstIA addr.IA, entries []activeEntry,
	paths []snet.PathInterfacesHaver, currentRsvs []*segment.Reservation) error {

	for _, entry := range entries {
		entry.mutex.Lock()
		defer entry.mutex.Unlock()

		// filter reservations
		compatible := entry.Filter(currentRsvs, k.manager.Now())
		entry.activeRsvs = compatible
		var requestCount int = len(entry.activeRsvs) - minActiveRsvs
		if requestCount > 0 {
			requests, err := entry.PrepareSetupRequests(paths)
			if err != nil {
				return serrors.WrapStr("cannot setup new reservations", err, "paths", paths)
			}
			// this will block until successfully finished
			return k.requestNSuccessfulRsvs(ctx, dstIA, entry, requests, requestCount)
		}
	}
	return nil
}

// requestNSuccessfulRsvs uses the manager to request reservations in parallel, until
// the pending count is reached, or there are no more available requests.
func (k *keeper) requestNSuccessfulRsvs(ctx context.Context, dstIA addr.IA, entry activeEntry,
	requests []*segment.SetupReq, pendingCount int) error {

	var setups []*segment.SetupReq
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
	activeRsvs   []*segment.Reservation
	// minActiveRsvs int // TODO(juagargi) allow to setup a value here instead of the const minActiveRsvs
}

const minActiveRsvs = 1

// Filter will filter reservations compatible with the requirements for this entry.
func (e *activeEntry) Filter(rsvs []*segment.Reservation, now time.Time) []*segment.Reservation {
	accepted := make([]*segment.Reservation, 0)
	for _, rsv := range rsvs {
		if e.requirements.Compliant(rsv, now) {
			accepted = append(accepted, rsv)
		}
	}
	return accepted
}

// PrepareSetupRequests creates new reservation requests compliant with the requirements.
// This function creates as many reservations requests as there are
// scion paths compatible with the requirements.
func (e *activeEntry) PrepareSetupRequests(ifaces []snet.PathInterfacesHaver) (
	[]*segment.SetupReq, error) {

	// filter paths
	filtered := e.requirements.predicate.EvalInterfaces(ifaces)
	requests := make([]*segment.SetupReq, len(filtered))
	// create setup requests
	for i, p := range filtered {
		opaque, err := segment.NewOpaquePathFromInterfaces(p.Interfaces())
		if err != nil {
			return nil, err
		}
		req := &segment.SetupReq{
			Request:    segment.Request{},
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

func (e *activeEntry) SelectRequests(requests []*segment.SetupReq, n int) []int {
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

func (r entryRequirements) Compliant(rsv *segment.Reservation, now time.Time) bool {
	idx := rsv.ActiveIndex()
	switch {
	case idx == nil:
		return false
	case !idx.Expiration.After(now):
		return false
	case idx.MinBW < r.minBW:
		return false
	case idx.MaxBW > r.maxBW:
		return false
	case rsv.TrafficSplit != r.splitCls:
		return false
	case rsv.PathEndProps != r.endProps:
		return false
	case len(r.predicate.EvalInterfaces([]snet.PathInterfacesHaver{rsv.Path})) == 0:
		return false
	}
	return true
}

// splitRequests takes a slice of requests and indices, and returns two slices:
// first, those elements in the indices, in the exact order as specified in indices.
// second, all the other elements.
func splitRequests(requests []*segment.SetupReq, indices []int) (
	[]*segment.SetupReq, []*segment.SetupReq) {
	a := make([]*segment.SetupReq, len(indices))
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
