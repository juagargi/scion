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

package main

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/addr"
	humm "github.com/scionproto/scion/pkg/hummingbird"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// tinyBaseHops are the base hops of the tiny-topology path 112 -> 110 -> 111.
func tinyBaseHops() []snetpath.BaseHop {
	ia := addr.MustParseIA
	return snetpath.InterfacesToBaseHops([]snet.PathInterface{
		{IA: ia("1-ff00:0:112"), ID: 1},
		{IA: ia("1-ff00:0:110"), ID: 2},
		{IA: ia("1-ff00:0:110"), ID: 1},
		{IA: ia("1-ff00:0:111"), ID: 41},
	})
}

func TestPartialSelection(t *testing.T) {
	t.Parallel()
	baseHops := tinyBaseHops()
	require.Len(t, baseHops, 3)
	ias := []string{"1-ff00:0:112", "1-ff00:0:110", "1-ff00:0:111"}
	for i, hop := range baseHops {
		require.Equal(t, addr.MustParseIA(ias[i]), hop.IA)
	}

	cases := []struct {
		name        string
		flag        string
		forward     []int
		reverse     []int
		marketplace []int // nil: every AS
	}{
		{"1 absent", "", []int{0, 1, 2}, []int{0, 1, 2}, nil},
		{"2 !111", "!1-ff00:0:111", []int{0, 1}, []int{1, 2}, []int{0, 1}},
		{"3 !112", "!1-ff00:0:112", []int{1, 2}, []int{0, 1}, []int{1, 2}},
		{"4 !110", "!1-ff00:0:110", []int{0, 2}, []int{0, 2}, []int{0, 2}},
		{"5 111,110", "1-ff00:0:111,1-ff00:0:110", []int{1, 2}, []int{0, 1}, []int{1, 2}},
		{"6 111,112", "1-ff00:0:111,1-ff00:0:112", []int{0, 2}, []int{0, 2}, []int{0, 2}},
		{"7 110,112", "1-ff00:0:110,1-ff00:0:112", []int{0, 1}, []int{1, 2}, []int{0, 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			filter, err := humm.ParseASFilter(tc.flag)
			require.NoError(t, err)

			selected := filter.SelectIndices(baseHops)
			require.Equal(t, tc.forward, selected)
			require.Equal(t, tc.marketplace, filter.MarketSelection(baseHops))

			// The selected hops are the ones at the indices.
			hops := humm.PickHops(baseHops, selected)
			require.Len(t, hops, len(selected))
			for k, idx := range selected {
				require.Equal(t, baseHops[idx], hops[k])
			}

			// The reverse selection is the mirror, and picks the same ASes.
			reverseHops := reverseBaseHops(baseHops)
			reverseSelected := humm.MirrorIndices(len(baseHops), selected)
			require.Equal(t, tc.reverse, reverseSelected)
			revPicked := humm.PickHops(reverseHops, reverseSelected)
			require.Len(t, revPicked, len(hops))
			for k := range hops {
				// Both lists are in their path order, so they are each other's reverse.
				other := revPicked[len(revPicked)-1-k]
				require.Equal(t, hops[k].IA, other.IA)
				require.Equal(t, hops[k].Ingress, other.Egress)
				require.Equal(t, hops[k].Egress, other.Ingress)
			}
		})
	}
}

func TestPartialSelectionNilAndEmpty(t *testing.T) {
	t.Parallel()
	baseHops := tinyBaseHops()

	// No filter: every AS, and nil for the marketplace.
	var filter *humm.ASFilter
	require.Equal(t, []int{0, 1, 2}, filter.SelectIndices(baseHops))
	require.Nil(t, (*humm.ASFilter)(nil).MarketSelection(baseHops))

	// A filter selecting nothing on the path is a non-nil empty selection.
	filter, err := humm.ParseASFilter("1-ff00:0:999")
	require.NoError(t, err)
	selected := filter.SelectIndices(baseHops)
	require.Empty(t, selected)
	require.NotNil(t, filter.MarketSelection(baseHops))
	require.Empty(t, filter.MarketSelection(baseHops))
	require.Empty(t, humm.PickHops(baseHops, selected))
	require.Empty(t, humm.MirrorIndices(len(baseHops), selected))

	seq, err := humm.ExpandFlyovers(len(baseHops), selected, nil)
	require.NoError(t, err)
	require.Len(t, seq, len(baseHops))
	for _, hop := range seq {
		require.Nil(t, hop)
	}
}
