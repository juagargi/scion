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

	"github.com/scionproto/scion/go/cs/reservation/conf"
	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/cs/reservationstorage"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/periodic"
)

// Manager takes care of the health of the segment reservations.
// TODO(juagargi) do the Manager interface
type Manager struct {
	store            reservationstorage.Store
	initial          conf.Reservations
	alreadyRequested bool
}

var _ periodic.Task = (*Manager)(nil)

func NewColibriManager(store reservationstorage.Store, initial conf.Reservations) *Manager {
	return &Manager{
		store:   store,
		initial: initial,
	}
}

func (m *Manager) Name() string {
	return "colibri.Manager"
}

func (m *Manager) Run(ctx context.Context) {
	m.alreadyRequested = true
	if m.alreadyRequested {
		// TODO(juagargi) this should not be a task
		return
	}
	var wg sync.WaitGroup
	for _, rsv := range m.initial.Rsvs {
		wg.Add(1)
		cfg := rsv
		go func() {
			defer log.HandlePanic()
			m.requestReservation(ctx, &wg, cfg)
		}()
	}
	log.Info("waiting for initial reservations", "count", len(m.initial.Rsvs))
	wg.Wait()
}

func (m *Manager) requestReservation(ctx context.Context, wg *sync.WaitGroup,
	cfg conf.ReservationEntry) {

	defer wg.Done()

	// prepare request
	req := segment.SetupReq{
		Request:    segment.Request{},
		MinBW:      cfg.MinSize,
		MaxBW:      cfg.MaxSize,
		SplitCls:   cfg.SplitCls,
		PathProps:  cfg.EndProps.PathEndProps,
		AllocTrail: reservation.AllocationBeads{},
	}
	if err := m.store.InitSegmentReservation(ctx, &req); err != nil {
		log.Error("failed to request initial reservation", "error", err)
	}
}
