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
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/cs/reservation/segment"
	st "github.com/scionproto/scion/go/cs/reservation/segmenttest"
	"github.com/scionproto/scion/go/cs/reservationstore/mock_reservationstore"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/pathpol"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/util"
	"github.com/scionproto/scion/go/lib/xtest"
)

func TestKeepOneShot(t *testing.T) {

}

func TestKeepDestination(t *testing.T) {

}

func TestSetupsPerDestination(t *testing.T) {
	ctx := context.Background()
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	now := util.SecsToTime(10)
	localIA := xtest.MustParseIA("1-ff00:0:1")
	dstIA := xtest.MustParseIA("1-ff00:0:2")
	entries := []activeEntry{
		{
			requirements: entryRequirements{
				predicate: newSequence(t, "1-ff00:0:1 1-ff00:0:2"), // direct
				minBW:     10,
				maxBW:     42,
				splitCls:  2,
				endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
			},
			mutex: new(sync.Mutex),
		},
		{
			requirements: entryRequirements{
				predicate: newSequence(t, "1-ff00:0:1 0+ 1-ff00:0:2"), // not direct
				minBW:     10,
				maxBW:     42,
				splitCls:  2,
				endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
			},
			mutex: new(sync.Mutex),
		},
	}
	paths := []snet.PathInterfacesHaver{
		st.NewPathFromComponents(0, "1-ff00:0:1", 1, 2, "1-ff00:0:2", 0), // direct
		st.NewPathFromComponents(0, "1-ff00:0:1", 2, 3, "1-ff00:0:2", 0), // direct
		st.NewPathFromComponents(0, "1-ff00:0:1", 3, 88, "1-ff00:0:88", 99, 4, "1-ff00:0:2", 0),
	}
	noPriorRsvs := []*segment.Reservation{}
	manager := mockManager(ctrl, now, localIA)
	keeper := keeper{
		manager: manager,
	}
	manager.EXPECT().RequestMany(gomock.Any(), gomock.Any()).Times(2).DoAndReturn(
		func(_ context.Context, reqs []*segment.SetupReq) ([]*segment.Reservation, error) {
			return make([]*segment.Reservation, len(reqs)), nil
		})

	err := keeper.setupsPerDestination(ctx, dstIA, entries, paths, noPriorRsvs)
	require.NoError(t, err)
}

func TestRequestNSuccessfulRsvs(t *testing.T) {
	cases := map[string]struct {
		requirements      entryRequirements
		paths             []snet.PathInterfacesHaver
		requiredCount     int // amount of rsvs we want
		successfulPerCall int // manager will only obtain these per call
		expectError       bool
		expectedReqs      []int // setup requests expected at the manager, per call. nil == error
	}{
		"empty": {
			requirements: entryRequirements{
				predicate: newSequence(t, ""),
				minBW:     10,
				maxBW:     42,
				splitCls:  2,
				endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
			},
			paths:             []snet.PathInterfacesHaver{},
			requiredCount:     1,
			successfulPerCall: 1,
			expectError:       true,
		},
		"ask 2 get 2": {
			requirements: entryRequirements{
				predicate: newSequence(t, "1-ff00:0:1 1-ff00:0:2"), // direct
				minBW:     10,
				maxBW:     42,
				splitCls:  2,
				endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
			},
			paths: []snet.PathInterfacesHaver{
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 2, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:1", 2, 3, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 111, "1-ff00:0:666", 222, 2, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 222, "1-ff00:0:666", 333, 2, "1-ff00:0:2", 0),
			},
			requiredCount:     2,
			successfulPerCall: 2,
			expectedReqs:      []int{2},
		},
		"ask too many": { // predicate(4 paths) -> 2 paths -> 2 requests; but desired is 4
			requirements: entryRequirements{
				predicate: newSequence(t, "1-ff00:0:1 1-ff00:0:2"), // direct
				minBW:     10,
				maxBW:     42,
				splitCls:  2,
				endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
			},
			paths: []snet.PathInterfacesHaver{
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 2, "1-ff00:0:2", 0), // direct
				st.NewPathFromComponents(0, "1-ff00:0:1", 2, 3, "1-ff00:0:2", 0), // direct
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 111, "1-ff00:0:666", 222, 2, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 222, "1-ff00:0:666", 333, 2, "1-ff00:0:2", 0),
			},
			requiredCount:     4,
			successfulPerCall: 4,
			expectError:       true,
			expectedReqs:      []int{2},
		},
		"ask 3 return 2": {
			requirements: entryRequirements{
				predicate: newSequence(t, ""),
				minBW:     10,
				maxBW:     42,
				splitCls:  2,
				endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
			},
			paths: []snet.PathInterfacesHaver{
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 2, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:1", 2, 3, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 111, "1-ff00:0:666", 222, 2, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 222, "1-ff00:0:666", 333, 2, "1-ff00:0:2", 0),
			},
			requiredCount:     3,
			successfulPerCall: 2,
			expectedReqs:      []int{3, 1},
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			ctrl := gomock.NewController(t)
			defer ctrl.Finish()

			now := util.SecsToTime(10)
			localIA := xtest.MustParseIA("1-ff00:0:1")
			dstIA := xtest.MustParseIA("1-ff00:0:2")

			entry := activeEntry{
				requirements: tc.requirements,
				mutex:        new(sync.Mutex),
			}
			manager := mockManager(ctrl, now, localIA)
			keeper := keeper{
				manager: manager,
				entries: map[addr.IA][]activeEntry{dstIA: {entry}},
			}
			// prepare the sequence of returns from the manager
			managerMutex := new(sync.Mutex)
			requestsCount := make([]int, len(tc.expectedReqs))
			var callCount int
			manager.EXPECT().RequestMany(gomock.Any(), gomock.Any()).AnyTimes().DoAndReturn(
				func(_ context.Context, reqs []*segment.SetupReq) ([]*segment.Reservation, error) {
					managerMutex.Lock()
					defer managerMutex.Unlock()

					if callCount > len(requestsCount) {
						require.FailNow(t, "unexpected call")
					}
					requestsCount[callCount] = len(reqs)
					callCount++
					n := tc.successfulPerCall
					if len(reqs) < n {
						n = len(reqs)
					}

					return make([]*segment.Reservation, n), nil
				})
			// build requests from paths (tested elsewhere)
			requests, err := entry.PrepareSetupRequests(tc.paths)
			require.NoError(t, err)
			// call and check
			err = keeper.requestNSuccessfulRsvs(ctx, dstIA, entry, requests, tc.requiredCount)
			if tc.expectError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.Equal(t, len(tc.expectedReqs), callCount)
			if callCount == 0 {
				requestsCount = nil // because Equal would fail otherwise
			}
			require.Equal(t, tc.expectedReqs, requestsCount)
		})
	}
}

func TestActiveEntryFilter(t *testing.T) {
	now := util.SecsToTime(0)
	tomorrow := now.Add(3600 * 24 * time.Second)
	requirements := entryRequirements{
		predicate: newSequence(t, "1-ff00:0:1#0,1 1-ff00:0:2 0*"),
		minBW:     10,
		maxBW:     42,
		splitCls:  2,
		endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
	}

	cases := map[string]struct {
		requirements entryRequirements
		expectedLen  int
		rsvs         []*segment.Reservation
	}{
		"empty": {
			requirements: requirements,
			expectedLen:  0,
			rsvs:         nil,
		},
		"three_identical": {
			requirements: requirements,
			expectedLen:  3,
			rsvs: st.NewRsvs(3, st.WithPath(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
				st.AddIndex(st.WithBW(12, 42, 0), st.WithExpiration(tomorrow)),
				st.AddIndex(st.WithBW(12, 24, 0), st.WithExpiration(tomorrow.Add(24*time.Hour))),
				st.WithActiveIndex(0),
				st.WithTrafficSplit(2),
				st.WithEndProps(requirements.endProps)),
		},
		"a non active index of all rsvs is modified to uncompliant": {
			requirements: requirements,
			expectedLen:  3,
			rsvs: st.NewRsvs(3, st.WithPath(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
				st.AddIndex(st.WithBW(12, 42, 0), st.WithExpiration(tomorrow)),
				st.AddIndex(st.WithBW(3, 24, 0), st.WithExpiration(tomorrow.Add(24*time.Hour))),
				st.WithActiveIndex(0),
				st.WithTrafficSplit(2),
				st.WithEndProps(requirements.endProps)),
		},
		"active index of first rsv is modified to uncompliant": {
			requirements: requirements,
			expectedLen:  2,
			rsvs: modOneRsv(st.NewRsvs(3, st.WithPath(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0),
				st.AddIndex(st.WithBW(12, 42, 0), st.WithExpiration(tomorrow)),
				st.AddIndex(st.WithBW(12, 24, 0), st.WithExpiration(tomorrow.Add(24*time.Hour))),
				st.WithActiveIndex(0),
				st.WithTrafficSplit(2),
				st.WithEndProps(requirements.endProps)), 0, st.ModIndex(0, st.WithBW(3, 0, 0))),
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			en := activeEntry{
				requirements: tc.requirements,
				mutex:        new(sync.Mutex),
			}
			compliant := en.Filter(tc.rsvs, now)
			require.Len(t, compliant, tc.expectedLen)
		})
	}
}

func TestEntryPrepareSetupRequests(t *testing.T) {
	cases := map[string]struct {
		requirements entryRequirements
		paths        []snet.PathInterfacesHaver
		expected     int
	}{
		"empty": {
			requirements: entryRequirements{},
			paths:        nil,
			expected:     0,
		},
		"no paths": {
			requirements: entryRequirements{
				predicate: newSequence(t, "1-ff00:0:1 0* 1-ff00:0:2"),
				minBW:     10,
				maxBW:     42,
				splitCls:  2,
				endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
			},
			paths:    []snet.PathInterfacesHaver{},
			expected: 0,
		},
		"starts here and ends there": {
			requirements: entryRequirements{
				predicate: newSequence(t, "1-ff00:0:1 0* 1-ff00:0:2"),
				minBW:     10,
				maxBW:     42,
				splitCls:  2,
				endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
			},
			paths: []snet.PathInterfacesHaver{
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 2, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:1", 2, 3, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 111, "1-ff00:0:666", 222, 2, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:1", 1, 222, "1-ff00:0:666", 333, 2, "1-ff00:0:2", 0),
			},
			expected: 4,
		},
		"all filtered out": {
			requirements: entryRequirements{
				predicate: newSequence(t, "1-ff00:0:1 0* 1-ff00:0:2"),
				minBW:     10,
				maxBW:     42,
				splitCls:  2,
				endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
			},
			paths: []snet.PathInterfacesHaver{
				st.NewPathFromComponents(0, "1-ff00:0:81", 1, 2, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:81", 2, 3, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:81", 1, 111, "1-ff00:0:666", 222, 2, "1-ff00:0:2", 0),
				st.NewPathFromComponents(0, "1-ff00:0:81", 1, 222, "1-ff00:0:666", 333, 2, "1-ff00:0:2", 0),
			},
			expected: 0,
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			entry := activeEntry{
				requirements: tc.requirements,
				mutex:        new(sync.Mutex),
				activeRsvs:   nil,
			}
			requests, err := entry.PrepareSetupRequests(tc.paths)
			require.NoError(t, err)
			require.Len(t, requests, tc.expected)
			filtered := entry.requirements.predicate.EvalInterfaces(tc.paths)
			require.Len(t, filtered, tc.expected) // this is internal, but forces 1 req per path
			bagOfPaths := make(map[string]struct{}, len(filtered))
			for _, p := range filtered {
				opaque, err := segment.NewOpaquePathFromInterfaces(p.Interfaces())
				require.NoError(t, err)
				k := opaque.String()
				_, ok := bagOfPaths[k]
				require.False(t, ok, "duplicated path in test", p)
				bagOfPaths[k] = struct{}{}
			}
			for _, req := range requests {
				// check req.PathToDst is in filtered paths
				_, ok := bagOfPaths[req.PathToDst.String()]
				require.True(t, ok, "len(bag)=%d, bag:%s", len(bagOfPaths), bagOfPaths)
				delete(bagOfPaths, req.PathToDst.String())
				// check the rest of the request
				require.Equal(t, tc.requirements.minBW, req.MinBW)
				require.Equal(t, tc.requirements.maxBW, req.MaxBW)
				require.Equal(t, tc.requirements.splitCls, req.SplitCls)
				require.Equal(t, tc.requirements.endProps, req.PathProps)
				require.Len(t, req.AllocTrail, 0)
			}
		})
	}
}

func TestEntrySelectRequests(t *testing.T) {
	entry := activeEntry{
		requirements: entryRequirements{
			predicate: newSequence(t, "1-ff00:0:1#0,1 1-ff00:0:2 0*"),
			minBW:     10,
			maxBW:     42,
			splitCls:  2,
			endProps:  reservation.StartLocal | reservation.EndLocal | reservation.EndTransfer,
		},
		mutex:      new(sync.Mutex),
		activeRsvs: nil,
	}
	cases := map[string]struct {
		requests    []*segment.SetupReq
		n           int
		expectedLen int
	}{
		"regular": {
			requests:    make([]*segment.SetupReq, 8),
			n:           3,
			expectedLen: 3,
		},
		"no requests": {
			requests:    make([]*segment.SetupReq, 0),
			n:           3,
			expectedLen: 0,
		},
		"asked too many": {
			requests:    make([]*segment.SetupReq, 8),
			n:           9,
			expectedLen: 8,
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			indices := entry.SelectRequests(tc.requests, tc.n)
			require.Len(t, indices, tc.expectedLen)
			visited := make(map[int]struct{}, len(indices))
			for _, idx := range indices {
				_, ok := visited[idx]
				require.False(t, ok, "selected indices have duplicates: %v", indices)
				visited[idx] = struct{}{}
				require.Less(t, idx, len(tc.requests))
			}
		})
	}
}

func TestSplitRequests(t *testing.T) {
	cases := map[string]struct {
		reqs []*segment.SetupReq
		idxs []int
		a    []*segment.SetupReq
		b    []*segment.SetupReq
	}{
		"empty": {
			reqs: fakeReqs(),
			idxs: []int{},
			a:    fakeReqs(),
			b:    fakeReqs(),
		},
		"no_indices": {
			reqs: fakeReqs(0),
			idxs: []int{},
			a:    fakeReqs(),
			b:    fakeReqs(0),
		},
		"all_a": {
			reqs: fakeReqs(0, 1, 2),
			idxs: []int{1, 2, 0},
			a:    fakeReqs(1, 2, 0),
			b:    fakeReqs(),
		},
		"sides": {
			reqs: fakeReqs(0, 1, 2, 3),
			idxs: []int{3, 0},
			a:    fakeReqs(3, 0),
			b:    fakeReqs(1, 2),
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			a, b := splitRequests(tc.reqs, tc.idxs)
			require.Equal(t, tc.a, a)
			require.ElementsMatch(t, tc.b, b)
		})
	}
}

func fakeReqs(ids ...int) []*segment.SetupReq {
	reqs := make([]*segment.SetupReq, len(ids))
	for i, id := range ids {
		reqs[i] = fakeReq(id)
	}
	return reqs
}

func fakeReq(id int) *segment.SetupReq {
	return &segment.SetupReq{
		MinBW: reservation.BWCls(id),
	}
}

func newSequence(t *testing.T, str string) *pathpol.Sequence {
	t.Helper()
	seq, err := pathpol.NewSequence(str)
	xtest.FailOnErr(t, err)
	return seq
}

func modOneRsv(rsvs []*segment.Reservation, whichRsv int,
	mods ...st.ReservationMod) []*segment.Reservation {

	rsvs[whichRsv] = st.ModRsv(rsvs[whichRsv], mods...)
	return rsvs
}

func mockManager(ctrl *gomock.Controller, now time.Time,
	localIA addr.IA) *mock_reservationstore.MockManager {

	m := mock_reservationstore.NewMockManager(ctrl)
	m.EXPECT().LocalIA().AnyTimes().Return(localIA)
	m.EXPECT().Now().AnyTimes().Return(now)
	return m
}
