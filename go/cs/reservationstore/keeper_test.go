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
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
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
