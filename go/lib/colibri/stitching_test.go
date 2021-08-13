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

// Package colibri contains methods for the creation and verification of the colibri packet
// timestamp and validation fields.
package colibri_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/lib/colibri"
	ct "github.com/scionproto/scion/go/lib/colibri/coltest"
)

func TestCombineAll(t *testing.T) {
	cases := map[string]struct {
		stitchable *colibri.StitchableSegments
		expected   []*colibri.FullTrip
	}{
		"empty": {
			stitchable: nil,
			expected:   nil,
		},
		"tiny topo": {
			stitchable: ct.NewStitchableSegments("1-ff00:0:111", "1-ff00:0:112",
				ct.WithCoreASes("1-ff00:0:110", "1-ff00:0:120"),
				ct.WithUpSegs(2),                         // src to core1
				ct.WithDownSegs(2),                       // from core1 to dst
				ct.WithCoreSegs(ct.P(2, 3), ct.P(3, 2))), // 2->3 , 3->2
			expected: ct.NewFullTrips("1-ff00:0:111", "1-ff00:0:112",
				ct.WithCoresInTrip("1-ff00:0:110"),
				ct.WithTrips(ct.T(ct.U(0, 2), ct.D(2, 1)))),
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			t.Log("---------------------------- stitchable:")
			t.Log(tc.stitchable)
			t.Log("---------------------------- expected:")
			t.Log(tc.expected)
			t.Log("---------------------------- actual:")
			actual := colibri.CombineAll(tc.stitchable)
			t.Log(actual)
			t.Log("---------------------------------------------")
			require.Equal(t, tc.expected, actual)
		})
	}
	require.NoError(t, nil)
}

//////// Test helper functions, TODO(juagargi) move them to their own test package
