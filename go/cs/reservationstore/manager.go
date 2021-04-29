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

// Manager is what a colibri manager requires to expose.
type Manager interface {
	periodic.Task
	Now() time.Time
	LocalIA() addr.IA
	Store() reservationstorage.Store
	// TODO(juagargi) move to sub interface, e.g. pather, comms manager,...
	PathsTo(dst addr.IA) ([]snet.PathInterfacesHaver, error)
	Request(ctx context.Context, req *segment.SetupReq) (*segment.Reservation, error)
	RequestMany(ctx context.Context, reqs []*segment.SetupReq) ([]*segment.Reservation, []error)
}

// manager takes care of the health of the segment reservations.
type manager struct {
	now        func() time.Time // replace in tests
	wakeupTime time.Time        // no need to do anything until this time
	keeper     *keeper
	localIA    addr.IA
	store      reservationstorage.Store
}

func NewColibriManager(localIA addr.IA, store reservationstorage.Store,
	initial *conf.Reservations) (Manager, error) {

	m := &manager{
		now:        time.Now,
		wakeupTime: time.Now().Add(-time.Nanosecond),
		localIA:    localIA,
		store:      store,
	}

	keeper, err := NewKeeper(m, initial)
	if err != nil {
		return nil, err
	}
	m.keeper = keeper
	return m, nil
}

func (m *manager) Name() string {
	return "colibri.manager"
}

func (m *manager) Run(ctx context.Context) {
	logger := log.FromCtx(ctx)

	if time.Now().Before(m.wakeupTime) {
		return
	}
	logger.Debug("Reservation manager starting")
	defer logger.Debug("Reservation manager finished")
	wakeupTime, err := m.keeper.OneShot(ctx)
	if err != nil {
		logger.Error("while keeping the reservations", "err", err)
	}
	logger.Info("will wait until the specified time", "wakeup_time", wakeupTime)
	m.wakeupTime = wakeupTime
}

func (m *manager) Now() time.Time {
	return m.now()
}

func (m *manager) LocalIA() addr.IA {
	return m.localIA
}

func (m *manager) Store() reservationstorage.Store {
	return m.store
}

func (m *manager) PathsTo(dst addr.IA) ([]snet.PathInterfacesHaver, error) {
	// TODO
	return nil, nil
}

func (m *manager) Request(ctx context.Context, req *segment.SetupReq) (
	*segment.Reservation, error) {

	err := m.store.InitSegmentReservation(ctx, req)
	// TODO(juagargi) send request, wait for answer, etc
	return req.Reservation, err
}

func (m *manager) RequestMany(ctx context.Context, reqs []*segment.SetupReq) (
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
			rsvs[i], errs[i] = m.Request(ctx, req)
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
