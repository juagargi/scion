// Copyright 2026 ETH Zurich
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

package hummingbird

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/addr"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

var (
	ia110 = addr.MustParseIA("1-ff00:0:110")
	ia111 = addr.MustParseIA("1-ff00:0:111")
	ia112 = addr.MustParseIA("1-ff00:0:112")
	ia113 = addr.MustParseIA("1-ff00:0:113")
)

func tinyHops() []snetpath.BaseHop {
	return []snetpath.BaseHop{
		{IA: ia111, Egress: 41},
		{IA: ia110, Ingress: 1, Egress: 2},
		{IA: ia112, Ingress: 1},
	}
}

func TestParseASFilterErrors(t *testing.T) {
	for name, raw := range map[string]string{
		"empty token":     "1-ff00:0:111,,1-ff00:0:112",
		"trailing comma":  "1-ff00:0:111,",
		"bang only":       "!",
		"bang in list":    "!1-ff00:0:110,!",
		"unparseable":     "not-an-ia",
		"unparseable neg": "!garbage",
		"mixed":           "1-ff00:0:111,!1-ff00:0:110",
		"mixed neg first": "!1-ff00:0:110,1-ff00:0:111",
	} {
		t.Run(name, func(t *testing.T) {
			f, err := ParseASFilter(raw)
			require.Error(t, err)
			require.Nil(t, f)
		})
	}
}

func TestASFilterSelectIndices(t *testing.T) {
	dupHops := append(tinyHops(), snetpath.BaseHop{IA: ia111})
	tests := map[string]struct {
		raw      string
		hops     []snetpath.BaseHop
		expected []int
		str      string
	}{
		"empty flag selects all": {
			raw: "", hops: tinyHops(), expected: []int{0, 1, 2}, str: "all",
		},
		"allow list": {
			raw: "1-ff00:0:111,1-ff00:0:112", hops: tinyHops(),
			expected: []int{0, 2}, str: "only 1-ff00:0:111,1-ff00:0:112",
		},
		"deny list": {
			raw: "!1-ff00:0:110", hops: tinyHops(),
			expected: []int{0, 2}, str: "all except 1-ff00:0:110",
		},
		"deny several, whitespace": {
			raw: " !1-ff00:0:110 , !1-ff00:0:111 ", hops: tinyHops(),
			expected: []int{2}, str: "all except 1-ff00:0:110,1-ff00:0:111",
		},
		"unknown AS in allow list": {
			raw: "1-ff00:0:113", hops: tinyHops(),
			expected: []int{}, str: "only 1-ff00:0:113",
		},
		"unknown AS in deny list": {
			raw: "!1-ff00:0:113", hops: tinyHops(),
			expected: []int{0, 1, 2}, str: "all except 1-ff00:0:113",
		},
		"duplicate token": {
			raw: "1-ff00:0:111,1-ff00:0:111", hops: tinyHops(),
			expected: []int{0}, str: "only 1-ff00:0:111",
		},
		"order irrelevant": {
			raw: "1-ff00:0:112,1-ff00:0:111", hops: tinyHops(),
			expected: []int{0, 2}, str: "only 1-ff00:0:111,1-ff00:0:112",
		},
		"duplicate AS on path": {
			raw: "1-ff00:0:111", hops: dupHops,
			expected: []int{0, 3}, str: "only 1-ff00:0:111",
		},
		"no hops": {
			raw: "1-ff00:0:111", hops: nil, expected: []int{}, str: "only 1-ff00:0:111",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			f, err := ParseASFilter(tc.raw)
			require.NoError(t, err)
			require.Equal(t, tc.expected, f.SelectIndices(tc.hops))
			require.Equal(t, tc.str, f.String())
		})
	}
}

func TestASFilterSelects(t *testing.T) {
	allow, err := ParseASFilter("1-ff00:0:111")
	require.NoError(t, err)
	deny, err := ParseASFilter("!1-ff00:0:111")
	require.NoError(t, err)
	var nilFilter *ASFilter
	for name, f := range map[string]*ASFilter{
		"nil": nilFilter, "zero": {},
	} {
		t.Run(name, func(t *testing.T) {
			require.True(t, f.Selects(ia111))
			require.True(t, f.Selects(ia113))
			require.Equal(t, "all", f.String())
			require.Equal(t, []int{0, 1, 2}, f.SelectIndices(tinyHops()))
		})
	}
	require.True(t, allow.Selects(ia111))
	require.False(t, allow.Selects(ia112))
	require.False(t, deny.Selects(ia111))
	require.True(t, deny.Selects(ia112))
}

func TestExpandFlyovers(t *testing.T) {
	h0 := &snetpath.Hop{BaseHop: snetpath.BaseHop{IA: ia111}}
	h2 := &snetpath.Hop{BaseHop: snetpath.BaseHop{IA: ia112}}
	tests := map[string]struct {
		pathLen  int
		indices  []int
		flyovers []*snetpath.Hop
		expected snetpath.FlyoverSequence
		wantErr  bool
	}{
		"holes": {
			pathLen: 3, indices: []int{0, 2}, flyovers: []*snetpath.Hop{h0, h2},
			expected: snetpath.FlyoverSequence{h0, nil, h2},
		},
		"none selected": {
			pathLen: 3, expected: snetpath.FlyoverSequence{nil, nil, nil},
		},
		"length mismatch": {
			pathLen: 3, indices: []int{0, 2}, flyovers: []*snetpath.Hop{h0}, wantErr: true,
		},
		"index too large": {
			pathLen: 3, indices: []int{3}, flyovers: []*snetpath.Hop{h0}, wantErr: true,
		},
		"negative index": {
			pathLen: 3, indices: []int{-1}, flyovers: []*snetpath.Hop{h0}, wantErr: true,
		},
		"not ascending": {
			pathLen: 3, indices: []int{2, 0}, flyovers: []*snetpath.Hop{h2, h0}, wantErr: true,
		},
		"duplicate index": {
			pathLen: 3, indices: []int{1, 1}, flyovers: []*snetpath.Hop{h0, h2}, wantErr: true,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			seq, err := ExpandFlyovers(tc.pathLen, tc.indices, tc.flyovers)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Len(t, seq, tc.pathLen)
			require.Equal(t, tc.expected, seq)
		})
	}
}

func TestMirrorIndices(t *testing.T) {
	tests := map[string]struct {
		pathLen  int
		indices  []int
		expected []int
	}{
		"3 hops, first and second": {3, []int{0, 1}, []int{1, 2}},
		"3 hops, first and last":   {3, []int{0, 2}, []int{0, 2}},
		"3 hops, middle":           {3, []int{1}, []int{1}},
		"3 hops, all":              {3, []int{0, 1, 2}, []int{0, 1, 2}},
		"empty":                    {3, []int{}, []int{}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.expected, MirrorIndices(tc.pathLen, tc.indices))
		})
	}
}

func TestMarketSelection(t *testing.T) {
	tests := map[string]struct {
		raw      string
		expected []int
	}{
		"no filter":          {raw: "", expected: nil},
		"selects everything": {raw: "!1-ff00:0:113", expected: nil},
		"allow list":         {raw: "1-ff00:0:111", expected: []int{0}},
		"deny list":          {raw: "!1-ff00:0:110", expected: []int{0, 2}},
		"empty but non-nil":  {raw: "1-ff00:0:113", expected: []int{}},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			filter, err := ParseASFilter(tc.raw)
			require.NoError(t, err)
			require.Equal(t, tc.expected, filter.MarketSelection(tinyHops()))
		})
	}
	t.Run("nil filter", func(t *testing.T) {
		require.Nil(t, (*ASFilter)(nil).MarketSelection(tinyHops()))
	})
}

func TestPickHops(t *testing.T) {
	hops := tinyHops()
	require.Equal(t, []snetpath.BaseHop{hops[2], hops[0]}, PickHops(hops, []int{2, 0}))
	require.Empty(t, PickHops(hops, nil))
}
