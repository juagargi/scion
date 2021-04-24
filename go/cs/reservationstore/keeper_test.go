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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/cs/reservation/segment"
	st "github.com/scionproto/scion/go/cs/reservation/segmenttest"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/pathpol"
	"github.com/scionproto/scion/go/lib/util"
	"github.com/scionproto/scion/go/lib/xtest"
)

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

func TestSelectRequests(t *testing.T) {
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
