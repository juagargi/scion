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
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/periodic"
	"github.com/scionproto/scion/go/lib/snet"
)

var now func() time.Time // to be able to replace it in tests

func init() {
	now = time.Now
}

// Manager takes care of the health of the segment reservations.
// TODO(juagargi) do the Manager interface
type Manager struct {
	keeper     *keeper
	localIA    addr.IA
	store      reservationstorage.Store
	wakeupTime time.Time // no need to do anything until this time
}

var _ periodic.Task = (*Manager)(nil)

func NewColibriManager(localIA addr.IA, store reservationstorage.Store,
	initial conf.Reservations) (*Manager, error) {

	m := &Manager{
		localIA:    localIA,
		store:      store,
		wakeupTime: time.Now().Add(-time.Second),
	}

	keeper, err := NewKeeper(m, initial)
	if err != nil {
		return nil, err
	}
	m.keeper = keeper
	return m, nil
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
}

func (m *Manager) pathsTo(dst addr.IA) ([]snet.PathInterfacesHaver, error) {
	// TODO
	return nil, nil
}

func (m *Manager) request(ctx context.Context, req *segment.SetupReq) (
	*segment.Reservation, error) {

	err := m.store.InitSegmentReservation(ctx, req)
	// TODO(juagargi) send request, wait for answer, etc
	return req.Reservation, err
}

func (m *Manager) requestMany(ctx context.Context, reqs []*segment.SetupReq) (
	[]*segment.Reservation, []error) {

	wg := sync.WaitGroup{}
	errs := make([]error, len(reqs))
	rsvs := make([]*segment.Reservation, len(reqs))
	for i, req := range reqs {
		i, req := i, req
		wg.Add(1)
		go func(req *segment.SetupReq) {
			defer log.HandlePanic()
			defer wg.Done()
			rsvs[i], errs[i] = m.request(ctx, req)
		}(req)
	}
	wg.Wait()
	returningErrs := make([]error, 0)
	returningRsvs := make([]*segment.Reservation, 0)
	for i := 0; i < len(reqs); i++ {
		if errs[i] != nil {
			returningErrs = append(returningErrs, errs[i])
		} else {
			returningRsvs = append(returningRsvs, rsvs[i])
		}
	}
	return returningRsvs, returningErrs
}
