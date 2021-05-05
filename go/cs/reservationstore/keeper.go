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

	base "github.com/scionproto/scion/go/cs/reservation"
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

// sleepAtLeast is the time duration that the keeper will sleep at a minimum, even
// if it's called very frequently.
const sleepAtLeast = 4 * time.Second

const sleepAtMost = 5 * time.Minute

// min validity in the future for the reservations
const minDuration = time.Minute

type keeper struct {
	sleepUntil time.Time // nothing to do in the keeper until this time
	manager    Manager
	entries    map[addr.IA][]requirements
}

func NewKeeper(manager Manager, conf *conf.Reservations) (
	*keeper, error) {

	entries, err := parseInitial(conf)
	if err != nil {
		return nil, err
	}
	return &keeper{
		sleepUntil: time.Now().Add(-time.Nanosecond),
		manager:    manager,
		entries:    entries,
	}, nil
}

// OneShot returns the time when it expects to be called again. Before this time it has
// nothing to do.
func (k *keeper) OneShot(ctx context.Context) (time.Time, error) {
	wg := sync.WaitGroup{}
	wakeupTimes := make(chan time.Time, len(k.entries))
	for dst, entries := range k.entries {
		dst, entries := dst, entries
		wg.Add(1)
		go func(c chan time.Time) {
			defer log.HandlePanic()
			defer wg.Done()
			scionPaths, err := k.manager.PathsTo(ctx, dst)
			if err != nil {
				log.Error("keeping the reservations", "err", err)
			}
			wakeup, err := k.keepDestination(ctx, dst, entries, scionPaths)
			if err != nil {
				log.Error("keeping the reservations", "err", err)
			}
			c <- wakeup
		}(wakeupTimes)
	}
	wg.Wait()
	close(wakeupTimes)
	earliest := k.manager.Now().Add(sleepAtMost)
	for wakeup := range wakeupTimes {
		if wakeup.Before(earliest) {
			earliest = wakeup
		}
	}
	if earliest.Sub(k.manager.Now()) < sleepAtLeast {
		earliest = k.manager.Now().Add(sleepAtLeast)
	}
	return earliest, nil
}

// keepDestination will ensure that all reservations for dstIA exist and are compliant.
// It returns the time until there is nothing to do for these reservations.
func (k *keeper) keepDestination(ctx context.Context, dstIA addr.IA, entries []requirements,
	paths []snet.Path) (time.Time, error) {

	// get reservations once and pass them along.
	rsvs, err := k.manager.Store().GetSegmentRsvsFromSrcDstIA(ctx, k.manager.LocalIA(), dstIA)
	if err != nil {
		return time.Time{}, err
	}
	// setup new reservations
	sleepUntil, err := k.setupsPerDestination(ctx, dstIA, entries, paths, rsvs)
	if err != nil {
		return sleepUntil, serrors.WrapStr("keeping destination", err, "dst", dstIA)
	}
	return sleepUntil, nil
}

// setupsPerDestination processes all entries for a given IA sequentially, to avoid
// reservation racing. It creates the setup requests and later sends them.
// Returns the time until which there is nothing to do for these reservations.
func (k *keeper) setupsPerDestination(ctx context.Context, dstIA addr.IA, entries []requirements,
	paths []snet.Path, currentRsvs []*seg.Reservation) (time.Time, error) {

	wakeupTime := k.manager.Now().Add(sleepAtMost)
	for _, entry := range entries {
		// filter reservations
		atLeastUntil := k.manager.Now().Add(minDuration)
		compliantRsvs, couldBeCompliant, notCompliant :=
			entry.SplitByCompliance(currentRsvs, atLeastUntil)
		// report not compliant ones; don't delete them, they will expire eventually.
		if len(notCompliant) > 0 {
			log.Info("Non compliant reservations found (a change in requirements?)",
				"count", len(notCompliant))
			for _, rsv := range notCompliant {
				log.Info("not compliant rsv", "id", rsv.ID)
			}
		}
		// new indices:
		if err := k.askNewIndices(ctx, couldBeCompliant, dstIA, entry, atLeastUntil); err != nil {
			return time.Time{}, err
		}
		// reservations in couldBeComliant are now compliant

		// totally new reservations:
		var requestCount int = entry.minActiveRsvs -
			len(compliantRsvs) - len(couldBeCompliant)
		if err := k.askNewReservations(ctx, requestCount, dstIA, entry, paths, atLeastUntil); err != nil {
			return time.Time{}, err
		}
		// the couldBeCompliant reservations are good for minDuration,
		if len(couldBeCompliant) > 0 && atLeastUntil.Before(wakeupTime) {
			wakeupTime = atLeastUntil
		}
		// but we don't know about reservations from compliantRsvs
		for _, rsv := range compliantRsvs {
			if rsv.ActiveIndex().Expiration.Before(wakeupTime) {
				wakeupTime = rsv.ActiveIndex().Expiration
			}
		}
	}
	return wakeupTime, nil
}

// askNewIndices will prepare requests based on existing reservations and ask for new indices.
func (k *keeper) askNewIndices(ctx context.Context, rsvs []*seg.Reservation, dstIA addr.IA,
	entry requirements, atLeastUntil time.Time) error {

	// TODO(juagargi) test this function (the indices as seen in requests should be active+1)
	if len(rsvs) > 0 {
		requests, err := entry.PrepareRenewalRequests(rsvs, k.manager.Now(), atLeastUntil)
		if err != nil {
			return serrors.WrapStr("cannot request new indices", err)
		}
		// this will block until successfully finished
		return k.requestNSuccessfulRsvs(ctx, dstIA, entry, requests, len(rsvs))
	}
	return nil
}

// askNewReservations creates new requests based on the paths and the entry and ensures
// that at least `requiredSuccesful` are succesful.
func (k *keeper) askNewReservations(ctx context.Context, requiredSuccesful int, dstIA addr.IA,
	entry requirements, paths []snet.Path, expTime time.Time) error {

	// TODO(juagargi) test this function (indices seen in requests should always be zero)
	if requiredSuccesful > 0 {
		requests, err := entry.PrepareSetupRequests(paths, k.manager.Now(), expTime)
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
func (k *keeper) requestNSuccessfulRsvs(ctx context.Context, dstIA addr.IA, entry requirements,
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
		return serrors.New("could not request the minimum required of reservations",
			"dst", dstIA, "requests_len", len(requests))
	}
	return nil
}

// requirements is a 1 to 1 association to a conf.ReservationEntry
type requirements struct {
	predicate     *pathpol.Sequence
	minBW         reservation.BWCls
	maxBW         reservation.BWCls
	splitCls      reservation.SplitCls
	endProps      reservation.PathEndProps
	minActiveRsvs int
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

// SplitByCompliance will split the reservations into three groups:
// compliant, could be compliant and not compliant, according to the requirements of this entry.
// For an explanation of compliance, see the type `Compliance`.
func (e *requirements) SplitByCompliance(rsvs []*seg.Reservation, atLeastUntil time.Time) (
	[]*seg.Reservation, []*seg.Reservation, []*seg.Reservation) {

	compliant := make([]*seg.Reservation, 0)
	couldBeCompliant := make([]*seg.Reservation, 0)
	neverCompliant := make([]*seg.Reservation, 0)
	for _, rsv := range rsvs {
		compliance := e.Compliance(rsv, atLeastUntil)
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
func (e *requirements) PrepareSetupRequests(paths []snet.Path, now time.Time, expTime time.Time) (
	[]*seg.SetupReq, error) {

	// filter paths
	filtered := e.predicate.Eval(paths)
	requests := make([]*seg.SetupReq, len(filtered))
	// create setup requests
	for i, p := range filtered {
		opaque, err := seg.OpaquePathFromInterfaces(p.Metadata().Interfaces)
		if err != nil {
			return nil, err
		}

		pp, err := base.NewPacketPath(p.Path())
		if err != nil {
			return nil, err
		}
		meta, err := base.NewRequestMetadata(pp)
		if err != nil {
			return nil, err
		}
		req := &seg.SetupReq{
			Request: seg.Request{
				RequestMetadata: *meta,
				MsgId: base.MsgId{
					ID:        reservation.SegmentID{}, // new source setup in store
					Timestamp: now,
				},
			},
			ExpirationTime: expTime,
			// RLC:            rlc,
			// PathType:       pathType,
			PathType:   reservation.CorePath, // TODO(juagargi) replace after tests
			MinBW:      e.minBW,
			MaxBW:      e.maxBW,
			SplitCls:   e.splitCls,
			PathProps:  e.endProps,
			AllocTrail: reservation.AllocationBeads{},
			PathToDst:  opaque,
		}
		requests[i] = req
	}
	return requests, nil
}

func (e *requirements) PrepareRenewalRequests(rsvs []*seg.Reservation, now, expTime time.Time) (
	[]*seg.SetupReq, error) {

	requests := []*seg.SetupReq{}
	for _, rsv := range rsvs {
		if len(e.predicate.EvalInterfaces([]snet.PathInterfacesHaver{rsv.Path})) == 0 {
			continue
		}
		// for i, p := range filtered {
		opaque, err := seg.OpaquePathFromInterfaces(rsv.Path.Interfaces())
		if err != nil {
			return nil, err
		}
		req := &seg.SetupReq{
			Request: seg.Request{ // without path in metadata (it will be set in the store)
				MsgId: base.MsgId{
					ID:        rsv.ID, // new source setup in store
					Timestamp: now,
				},
				Reservation: rsv,
			},
			MinBW:      rsv.ActiveIndex().MinBW,
			MaxBW:      rsv.ActiveIndex().MaxBW,
			SplitCls:   rsv.TrafficSplit,
			PathProps:  rsv.PathEndProps,
			AllocTrail: reservation.AllocationBeads{}, // at source
			PathToDst:  opaque,
		}
		requests = append(requests, req)
	}
	return requests, nil
}

func (e *requirements) SelectRequests(requests []*seg.SetupReq, n int) []int {
	if n > len(requests) {
		n = len(requests)
	}
	rand.Seed(time.Now().UnixNano()) // TODO(juagargi) select using better criteria
	return rand.Perm(n)
}

// Compliance checks the given reservation against the requirements and returns true if
// it satisfies them, plus the reservation is good at least until the time in `atLeastUntil`.
func (e requirements) Compliance(rsv *seg.Reservation, atLeastUntil time.Time) Compliance {
	switch {
	case rsv.TrafficSplit != e.splitCls:
		return NeverCompliant
	case rsv.PathEndProps != e.endProps:
		return NeverCompliant
	case len(e.predicate.EvalInterfaces([]snet.PathInterfacesHaver{rsv.Path})) == 0:
		return NeverCompliant
	}
	indices := rsv.Indices.Filter(
		seg.ByExpiration(atLeastUntil),
		seg.NotSwitchableFrom(rsv.ActiveIndex()),
		seg.ByMinBW(e.minBW),
		seg.ByMaxBW(e.maxBW),
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

func parseInitial(conf *conf.Reservations) (map[addr.IA][]requirements, error) {
	if conf == nil {
		log.Info("COLIBRI not keeping any reservations")
		return nil, nil
	}
	log.Info("COLIBRI will keep reservations", "count", len(conf.Rsvs))
	initial := make(map[addr.IA][]requirements)
	for _, r := range conf.Rsvs {
		seq, err := pathpol.NewSequence(r.PathPredicate)
		if err != nil {
			return nil, err
		}

		if r.RequiredCount <= 0 {
			return nil, serrors.New("required reservation count must be positive",
				"count", r.RequiredCount)
		}
		if r.MinSize > r.MaxSize {
			return nil, serrors.New("min bw must be less or equal than max bw",
				"min_bw", r.MinSize, "max_bw", r.MaxSize)
		}

		initial[r.DstAS] = append(initial[r.DstAS], requirements{
			predicate:     seq,
			minBW:         r.MinSize,
			maxBW:         r.MaxSize,
			splitCls:      r.SplitCls,
			endProps:      reservation.PathEndProps(r.EndProps),
			minActiveRsvs: r.RequiredCount,
		})
	}
	return initial, nil
}
