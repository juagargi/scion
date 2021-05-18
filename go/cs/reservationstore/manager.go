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

	base "github.com/scionproto/scion/go/cs/reservation"
	"github.com/scionproto/scion/go/cs/reservation/conf"
	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/cs/reservationstorage"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/periodic"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
)

// Manager is what a colibri manager requires to expose.
type Manager interface {
	periodic.Task
	Now() time.Time
	LocalIA() addr.IA
	Store() reservationstorage.Store
	// TODO(juagargi) move to sub interface, e.g. pather, comms manager,...
	PathsTo(ctx context.Context, dst addr.IA) ([]snet.Path, error)
	SetupRequest(ctx context.Context, req *segment.SetupReq) error
	SetupManyRequest(ctx context.Context, reqs []*segment.SetupReq) []error
	ActivateRequest(ctx context.Context, req *segment.Request) error
	ActivateManyRequest(ctx context.Context, reqs []*segment.Request) []error
}

// manager takes care of the health of the segment reservations.
type manager struct {
	now           func() time.Time // replace in tests
	wakeupTime    time.Time        // no need to do anything until this time
	wakeupExpirer time.Time        // wake up the colibri reservation expire routine
	wakeupKeeper  time.Time        // wake up the keeper (new rsvs/indices)
	keeper        *keeper          // handles new rsvs/indices
	localIA       addr.IA
	store         reservationstorage.Store
	router        snet.Router
}

func NewColibriManager(localIA addr.IA, router snet.Router, store reservationstorage.Store,
	initial *conf.Reservations) (Manager, error) {

	m := &manager{
		now:        time.Now,
		wakeupTime: time.Now().Add(-time.Nanosecond),
		localIA:    localIA,
		store:      store,
		router:     router,
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

	now := time.Now()
	if now.Before(m.wakeupTime) {
		return
	}
	wg := sync.WaitGroup{}
	wg.Add(2)
	go func() {
		defer log.HandlePanic()
		defer wg.Done()
		if now.Before(m.wakeupKeeper) {
			return
		}
		logger.Debug("Reservation manager starting")
		defer logger.Debug("Reservation manager finished")

		wakeupTime, err := m.keeper.OneShot(ctx)
		if err != nil {
			logger.Error("while keeping the reservations", "err", err)
		}
		logger.Info("will wait until the specified time", "wakeup_time", wakeupTime)
		m.wakeupKeeper = wakeupTime
	}()

	go func() {
		defer log.HandlePanic()
		defer wg.Done()
		if now.Before(m.wakeupExpirer) {
			return
		}
		n, wakeupTime, err := m.store.DeleteExpiredIndices(ctx)
		logger.Info("deleteme EXPIRER", "n", n, "wakeup", wakeupTime, "err", err)
		if err != nil {
			logger.Error("deleting expired indices", "count", n, "err", err)
		}
		m.wakeupExpirer = wakeupTime
	}()
	wg.Wait()
	if m.wakeupKeeper.Before(m.wakeupExpirer) {
		m.wakeupTime = m.wakeupKeeper
	} else {
		m.wakeupTime = m.wakeupExpirer
	}
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

func (m *manager) PathsTo(ctx context.Context, dst addr.IA) ([]snet.Path, error) {
	paths, err := m.router.AllRoutes(ctx, dst)
	log.Debug("colibri manager requested paths", "dst", dst, "count", len(paths), "err", err)
	return paths, err
}

func (m *manager) SetupRequest(ctx context.Context, req *segment.SetupReq) error {
	err := m.store.InitSegmentReservation(ctx, req)
	if err != nil {
		return err
	}
	rsv := req.Reservation
	// confirm new index
	deletemeIndex := rsv.Index(req.Index)
	log.Info("deleteme confirm", "id", req.ID, "index", req.Index, "index_index", deletemeIndex)
	confirmReq := &segment.Request{
		MsgId: base.MsgId{
			ID:        rsv.ID,
			Index:     req.Index,
			Timestamp: m.now(),
		},
		Path: req.Path,
	}
	res, err := m.store.ConfirmSegmentReservation(ctx, confirmReq)
	if err != nil || !res.Success() {
		log.Info("failed to confirm the index", "id", req.ID, "idx", req.Index,
			"err", err, "res", res)
	}
	return err
}

func (m *manager) SetupManyRequest(ctx context.Context, reqs []*segment.SetupReq) []error {
	wg := sync.WaitGroup{}
	wg.Add(len(reqs))
	errs := make([]error, len(reqs))
	for i, req := range reqs {
		i, req := i, req
		go func() {
			defer log.HandlePanic()
			defer wg.Done()
			errs[i] = m.SetupRequest(ctx, req)
		}()
	}
	wg.Wait()
	return errs
}

func (m *manager) ActivateRequest(ctx context.Context, req *segment.Request) error {
	res, err := m.store.ActivateSegmentReservation(ctx, req)
	if err != nil {
		return err
	}
	if !res.Success() {
		failure := res.(*base.ResponseFailure)
		return serrors.New("error activating index", "msg", failure.Message)
	}
	return nil
}

func (m *manager) ActivateManyRequest(ctx context.Context, reqs []*segment.Request) []error {
	wg := sync.WaitGroup{}
	wg.Add(len(reqs))
	errs := make([]error, len(reqs))
	for i, req := range reqs {
		i, req := i, req
		go func() {
			defer log.HandlePanic()
			defer wg.Done()
			errs[i] = m.ActivateRequest(ctx, req)
		}()
	}
	wg.Wait()
	return errs
}
