// Copyright 2020 ETH Zurich, Anapaya Systems
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

package conf

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/lib/xtest"
)

func TestJson(t *testing.T) {
	// test that this number cannot be represented with float64
	// (it will be used in one of the test cases)
	var bigint uint64 = 4418489049307132905
	require.NotEqual(t, bigint, uint64(float64(bigint)))

	cases := map[string]struct {
		filename string
		cap      Capacities
	}{
		"using all keys": {
			filename: "caps1.json",
			cap: Capacities{c: capacities{
				CapIn: map[uint16]uint64{1: 100, 2: 200, 3: 300},
				CapEg: map[uint16]uint64{1: 100, 2: 200, 3: 40},
			}},
		},
		"big num no float": {
			filename: "caps2.json",
			cap: Capacities{c: capacities{
				CapIn: map[uint16]uint64{1: bigint},
				CapEg: map[uint16]uint64{1: bigint},
			},
			},
		},
	}

	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			tc.cap.init()
			buf, err := json.MarshalIndent(tc.cap, "", "  ")
			buf = append(buf, '\n')
			require.NoError(t, err)
			expectedJSON := xtest.MustReadFromFile(t, tc.filename)
			require.Equal(t, expectedJSON, buf)
			c2 := Capacities{}
			err = c2.UnmarshalJSON(expectedJSON)
			require.NoError(t, err)
			require.Equal(t, tc.cap, c2)
		})
	}
}

func TestValidation(t *testing.T) {
	cases := map[string]struct {
		okay bool
		cap  Capacities
	}{
		"okay": {
			okay: true,
			cap: Capacities{c: capacities{
				CapIn: map[uint16]uint64{1: 100, 2: 200, 3: 300},
				CapEg: map[uint16]uint64{1: 100, 2: 200, 3: 40},
			}},
		},
		"too much ingress": {
			okay: false,
			cap: Capacities{c: capacities{
				CapIn: map[uint16]uint64{1: 10},
			}},
		},
		"too few ingress capacities": {
			okay: false,
			cap: Capacities{c: capacities{
				CapIn: map[uint16]uint64{1: 100, 3: 100},
				CapEg: map[uint16]uint64{1: 100, 2: 100, 3: 100},
			}},
		},
		"too few egress capacities": {
			okay: false,
			cap: Capacities{c: capacities{
				CapIn: map[uint16]uint64{1: 100, 2: 100, 3: 100},
				CapEg: map[uint16]uint64{1: 100, 3: 100},
			}},
		},
	}

	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			err := tc.cap.init()
			if tc.okay {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
		})
	}
}

func TestCapacities(t *testing.T) {
	c := &Capacities{c: capacities{
		CapIn: map[uint16]uint64{1: 100, 2: 100, 3: 100},
		CapEg: map[uint16]uint64{1: 100, 2: 100, 3: 100},
	}}
	copy := cloneCapacities(c) // save a copy of the original
	require.Equal(t, uint64(100), c.CapacityIngress(1))
	require.Equal(t, uint64(0), c.CapacityIngress(5))

	require.Equal(t, copy, c)
}

func cloneCapacities(c *Capacities) *Capacities {
	ret := &Capacities{}
	copy(ret.inIfs, c.inIfs)
	copy(ret.egIfs, c.egIfs)
	ret.c.CapIn = make(map[uint16]uint64)
	for k, v := range c.c.CapIn {
		ret.c.CapIn[k] = v
	}
	ret.c.CapEg = make(map[uint16]uint64)
	for k, v := range c.c.CapEg {
		ret.c.CapEg[k] = v
	}
	return ret
}
