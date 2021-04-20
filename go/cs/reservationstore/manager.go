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
	"sync"
	"time"

	"github.com/scionproto/scion/go/cs/reservation/conf"
	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/cs/reservationstorage"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/pathpol"
	"github.com/scionproto/scion/go/lib/periodic"
	"github.com/scionproto/scion/go/lib/snet"
)

// Manager takes care of the health of the segment reservations.
// TODO(juagargi) do the Manager interface
type Manager struct {
	localIA addr.IA
	store   reservationstorage.Store
	// initial conf.Reservations
	initial map[addr.IA]perDestination
	// alreadyRequested bool
	wakeupTime time.Time        // no need to do anything until this time
	now        func() time.Time // to be able to replace it in tests
}

var _ periodic.Task = (*Manager)(nil)

func NewColibriManager(localIA addr.IA, store reservationstorage.Store,
	initial conf.Reservations) (*Manager, error) {

	ini, err := parseInitial(initial)
	if err != nil {
		return nil, err
	}
	return &Manager{
		store:      store,
		initial:    ini,
		wakeupTime: time.Now().Add(-time.Second),
		now:        time.Now,
	}, nil
}

func (m *Manager) Name() string {
	return "colibri.Manager"
}

func (m *Manager) Run(ctx context.Context) {
	logger := log.FromCtx(ctx)

	if time.Now().Before(m.wakeupTime) {
		return
	}
	logger.Debug("Reservation manager starting")
	defer logger.Debug("Reservation manager finished")
	// if 4%5 != 0 {
	// 	return
	// }
	//
	//
	//
	//

	var wg sync.WaitGroup
	for dstIA, entries := range m.initial {
		wg.Add(1)
		dstIA, entries := dstIA, entries
		go func(wg *sync.WaitGroup) {
			defer log.HandlePanic()
			paths, err := m.pathsTo(dstIA)
			if err != nil {
				logger.Error("obtaining paths", "dst_ia", dstIA, "err", err)
				return
			}
			err = m.checkReservations(ctx, dstIA, entries.entries, paths)
			if err != nil {
				logger.Error("checking reservations")
			}
			wg.Done()
		}(&wg)
	}
	log.Info("waiting for initial reservations", "count", len(m.initial))
	wg.Wait()
}

func (m *Manager) pathsTo(dst addr.IA) ([]snet.PathInterfacesHaver, error) {
	// TODO
	return nil, nil
}

func (m *Manager) checkReservations(ctx context.Context, dstIA addr.IA, entries []entry,
	paths []snet.PathInterfacesHaver) error {

	// get reservations from the store
	rsvs, err := m.store.GetSegmentRsvsFromSrcDstIA(ctx, m.localIA, dstIA)
	if err != nil {
		return err
	}
	//
	//

	//
	//
	//

	var wg sync.WaitGroup
	for _, e := range entries {
		wg.Add(1)
		e := e
		go func() {
			defer log.HandlePanic()
			rsvs := append(rsvs[:0:0], rsvs...)
			paths := append(paths[:0:0], paths...)
			m.checkReservation(ctx, &e, rsvs, paths)
			wg.Done()
		}()
	}
	wg.Wait()
	return nil
}

func (m *Manager) checkReservation(ctx context.Context, e *entry,
	rsvs []*segment.Reservation, paths []snet.PathInterfacesHaver) error {

	now := m.now()
	// check if any existing reservation is valid
	invalidCount := 0
	for _, r := range rsvs {
		if r.ActiveIndex() == nil ||
			len(e.predicate.EvalInterfaces([]snet.PathInterfacesHaver{r.Path})) == 0 ||
			r.ActiveIndex().Expiration.Before(now) {
			invalidCount++
			continue
		}
	}
	if invalidCount < len(rsvs)-1 {
		// there is at least one
		return nil
	}
	// use the paths variable to create possible reservation(s)
	paths = e.predicate.EvalInterfaces(paths)
	return m.createReservation(ctx, e, paths)
}

func (m *Manager) createReservation(ctx context.Context, e *entry,
	paths []snet.PathInterfacesHaver) error {

	// TODO
	if err := m.store.InitSegmentReservation(ctx, e.setupReq); err != nil {
		log.Error("failed to request initial reservation", "error", err)
	}
	return nil
}

type entry struct {
	predicate *pathpol.Sequence
	setupReq  *segment.SetupReq
}

type perDestination struct {
	entries []entry
}

func parseInitial(conf conf.Reservations) (map[addr.IA]perDestination, error) {
	initial := make(map[addr.IA]perDestination)
	for _, r := range conf.Rsvs {
		seq, err := pathpol.NewSequence(r.PathPredicate)
		if err != nil {
			return nil, err
		}
		req := &segment.SetupReq{
			Request:    segment.Request{},
			MinBW:      r.MinSize,
			MaxBW:      r.MaxSize,
			SplitCls:   r.SplitCls,
			PathProps:  r.EndProps.PathEndProps,
			AllocTrail: reservation.AllocationBeads{},
		}

		dst, ok := initial[r.DstAS]
		if !ok {
			dst = perDestination{}
		}
		dst.entries = append(dst.entries, entry{
			predicate: seq,
			setupReq:  req,
		})
		initial[r.DstAS] = dst
	}
	return initial, nil
}
