// Copyright 2026 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package marketplace

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/segment/iface"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// TestUniqueAPIsOnly checks that the notes per AS are kept only if the api_address is unique.
func TestUniqueAPIsOnly(t *testing.T) {
	testCases := map[string]struct {
		note     string
		expected []string
	}{
		"a single marketplace": {note: noteWith("a"), expected: []string{"a"}},
		"two marketplaces":     {note: noteWith("a", "b"), expected: []string{"a", "b"}},
		"the same one twice":   {note: noteWith("a", "a"), expected: []string{"a"}},
		"a repeat in between":  {note: noteWith("a", "b", "a"), expected: []string{"a", "b"}},
		"nothing advertised":   {note: `{"hummingbird":[]}`, expected: []string{}},
		"unrelated":            {note: `{"latency":{"inter":100}}`, expected: []string{}},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			unique := ParseNote(tc.note).UniqueAPIsOnly()
			assert.Equal(t, tc.expected, addressesOf(unique.Notes))
		})
	}
}

// TestUniqueAPIsOnlyKeepsTheFirst pins which of the entries of one address survives,
// since they differ in the protocol the client would use to reach the marketplace.
func TestUniqueAPIsOnlyKeepsTheFirst(t *testing.T) {
	note := `{"hummingbird":[
		{"name":"m","api_protocol":"connectrpc/TLS/QUIC/SCION","api_address":"a"},
		{"name":"m","api_protocol":"connectrpc/TLS/TCP","api_address":"a"}]}`
	unique := ParseNote(note).UniqueAPIsOnly().Notes
	require.Len(t, unique, 1)
	assert.Equal(t, "connectrpc/TLS/QUIC/SCION", unique[0].APIProtocol)
}

// TestNewPathMarketplaces checks that the note of AS i of the path becomes the metadata of AS i,
// so that the marketplace coverage is computed against the right AS.
func TestNewPathMarketplaces(t *testing.T) {
	p := pathWithNotes(noteWith("a"), "", noteWith("b", "c"))
	discovered, err := NewPathMarketplaces(p)
	require.NoError(t, err)
	require.Len(t, discovered.Hops, 3)
	assert.Equal(t, snetpath.InterfacesToBaseHops(p.Metadata().Interfaces), discovered.Hops)
	require.Len(t, discovered.PathASes, 3)
	assert.Equal(t, []string{"a"}, addressesOf(discovered.PathASes[0].Notes))
	assert.Empty(t, discovered.PathASes[1].Notes)
	assert.Equal(t, []string{"b", "c"}, addressesOf(discovered.PathASes[2].Notes))
	// Every advertised marketplace is known, and none of them dialed yet.
	assert.Len(t, discovered.clients, 3)
	for _, client := range discovered.clients {
		assert.Nil(t, client)
	}
}

// TestNewPathMarketplacesForASesSubsetsNotes checks that only the selected ASes are kept,
// with their own notes and hops.
func TestNewPathMarketplacesForASesSubsetsNotes(t *testing.T) {
	p := pathWithNotes(noteWith("a"), noteWith("b"), noteWith("c"))
	all := snetpath.InterfacesToBaseHops(p.Metadata().Interfaces)
	m, err := NewPathMarketplacesForASes(p, []int{0, 2})
	require.NoError(t, err)
	assert.Equal(t, []snetpath.BaseHop{all[0], all[2]}, m.Hops)
	require.Len(t, m.PathASes, 2)
	assert.Equal(t, []string{"a"}, addressesOf(m.PathASes[0].Notes))
	assert.Equal(t, []string{"c"}, addressesOf(m.PathASes[1].Notes))
	assert.Len(t, m.clients, 2)
}

// TestNewPathMarketplacesForASesRejectsBadIndices checks the validation of the selection.
func TestNewPathMarketplacesForASesRejectsBadIndices(t *testing.T) {
	p := pathWithNotes(noteWith("a"), noteWith("b"), noteWith("c"))
	for name, indices := range map[string][]int{
		"out of range":      {0, 3},
		"negative":          {-1, 1},
		"descending":        {2, 0},
		"duplicates":        {1, 1},
		"descending middle": {0, 2, 1},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewPathMarketplacesForASes(p, indices)
			assert.Error(t, err)
		})
	}
	t.Run("empty is valid", func(t *testing.T) {
		m, err := NewPathMarketplacesForASes(p, []int{})
		require.NoError(t, err)
		assert.Empty(t, m.Hops)
	})
}

// TestFullCoverageCountIgnoresUnselectedAS checks that an AS without a marketplace does not
// prevent covering the selected ones.
func TestFullCoverageCountIgnoresUnselectedAS(t *testing.T) {
	p := pathWithNotes(noteWith("a"), "", noteWith("a"))
	all, err := NewPathMarketplaces(p)
	require.NoError(t, err)
	count, _ := all.FullCoverageCount()
	assert.Equal(t, 0, count)

	subset, err := NewPathMarketplacesForASes(p, []int{0, 2})
	require.NoError(t, err)
	count, coverage := subset.FullCoverageCount()
	assert.Equal(t, 1, count)
	assert.Equal(t, []string{"a"}, addressesOf(coverage))
}

// TestNewPathMarketplacesFromNotes checks the constructor taking explicit hops and notes.
func TestNewPathMarketplacesFromNotes(t *testing.T) {
	hops := snetpath.InterfacesToBaseHops(
		pathWithNotes("", "", "").Metadata().Interfaces)[:2]
	m, err := NewPathMarketplacesFromNotes(hops, []string{noteWith("a"), noteWith("b", "c")})
	require.NoError(t, err)
	assert.Equal(t, hops, m.Hops)
	require.Len(t, m.PathASes, 2)
	assert.Equal(t, []string{"a"}, addressesOf(m.PathASes[0].Notes))
	assert.Equal(t, []string{"b", "c"}, addressesOf(m.PathASes[1].Notes))
	assert.Len(t, m.clients, 3)

	_, err = NewPathMarketplacesFromNotes(hops, []string{noteWith("a")})
	assert.Error(t, err)
}

// TestInterfacePairsFromBaseHops checks the conversion keeps order and values.
func TestInterfacePairsFromBaseHops(t *testing.T) {
	p := pathWithNotes("", "", "")
	hops := snetpath.InterfacesToBaseHops(p.Metadata().Interfaces)
	pairs := interfacePairsFromBaseHops(hops)
	require.Len(t, pairs, 3)
	for i, h := range hops {
		assert.Equal(t, InterfacePair{
			IA: uint64(h.IA), Ingress: uint32(h.Ingress), Egress: uint32(h.Egress),
		}, pairs[i])
	}
	assert.Equal(t, pairs, interfacePairsFromInterfaces(p.Metadata().Interfaces))
}

// TestNewPathMarketplacesRejectsInconsistentMetadata checks that pairing a note with an AS
// is only possible when there is one note per AS.
func TestNewPathMarketplacesRejectsInconsistentMetadata(t *testing.T) {
	t.Run("no path at all", func(t *testing.T) {
		_, err := NewPathMarketplaces(nil)
		assert.Error(t, err)
	})
	t.Run("fewer notes than ASes", func(t *testing.T) {
		p := pathWithNotes(noteWith("a"), "", "")
		meta := p.Metadata()
		meta.Notes = meta.Notes[:2]
		_, err := NewPathMarketplaces(snetpath.Path{Meta: *meta})
		assert.Error(t, err)
	})
}

// TestFullCoverageCount covers the sets of marketplaces a path can be reserved through,
// including the ones where the smallest set is not the one a greedy pick would take.
func TestFullCoverageCount(t *testing.T) {
	testCases := map[string]struct {
		notes    []string
		count    int
		coverage []string
	}{
		"one marketplace everywhere": {
			notes:    []string{noteWith("a"), noteWith("a"), noteWith("a")},
			count:    1,
			coverage: []string{"a"},
		},
		"one shared marketplace": {
			notes:    []string{noteWith("a", "b"), noteWith("b", "c"), noteWith("b")},
			count:    1,
			coverage: []string{"b"},
		},
		"two disjoint halves": {
			notes:    []string{noteWith("a"), noteWith("a"), noteWith("b")},
			count:    2,
			coverage: []string{"a", "b"},
		},
		// This is a case to check that the minimum coverage can be found when the last entries
		// are the ones that determine said coverage (no greedy algorithm)
		"last_ases_determine_answer": {
			notes: []string{
				noteWith("big", "left"),
				noteWith("big", "left"),
				noteWith("big", "right"),
				noteWith("big", "right"),
				noteWith("left"),
				noteWith("right"),
			},
			count:    2,
			coverage: []string{"left", "right"},
		},
		"an AS advertises nothing": {
			notes:    []string{noteWith("a"), "", noteWith("a")},
			count:    0,
			coverage: nil,
		},
		"no AS advertises anything": {
			notes:    []string{"", ""},
			count:    0,
			coverage: nil,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			discovered, err := NewPathMarketplaces(pathWithNotes(tc.notes...))
			require.NoError(t, err)
			count, coverage := discovered.FullCoverageCount()
			assert.Equal(t, tc.count, count)
			assert.Equal(t, tc.count, len(coverage))
			if tc.coverage == nil {
				assert.Nil(t, coverage)
			} else {
				assert.ElementsMatch(t, tc.coverage, addressesOf(coverage))
			}
		})
	}
}

// TestConnectUnadvertised checks that we cannot connect to an unseen marketplace.
func TestConnectUnadvertised(t *testing.T) {
	discovered, err := NewPathMarketplaces(pathWithNotes(noteWith("a"), noteWith("a")))
	require.NoError(t, err)
	err = discovered.Connect(t.Context(), "b", "jwt", nil, snet.Topology{}, true)
	assert.Error(t, err)
}

// TestClientPerASNeedsAConnection checks that the clientPerAS function returns error if there
// is any on-path AS missing from the discovered clients.
func TestClientPerASNeedsAConnection(t *testing.T) {
	discovered, err := NewPathMarketplaces(pathWithNotes(noteWith("a"), noteWith("b")))
	require.NoError(t, err)
	_, err = discovered.clientPerAS()
	t.Logf("error is: %s", err.Error())
	assert.Error(t, err)

	// Add one client, but only covers the first AS.
	for entry := range discovered.clients {
		if entry.APIAddress == "a" {
			discovered.clients[entry] = &Client{}
		}
	}
	_, err = discovered.clientPerAS()
	assert.Error(t, err)

	// Add a second client. Now all ASes should be covered.
	for entry := range discovered.clients {
		if entry.APIAddress == "b" {
			discovered.clients[entry] = &Client{}
		}
	}
	clients, err := discovered.clientPerAS()
	require.NoError(t, err)
	require.Len(t, clients, 2) // a, b
	assert.NotSame(t, clients[0], clients[1])
}

func TestReverseInterfacePairs(t *testing.T) {
	pairs := []InterfacePair{
		{IA: 1, Ingress: 0, Egress: 2},
		{IA: 2, Ingress: 1, Egress: 3},
		{IA: 3, Ingress: 4, Egress: 0},
	}
	assert.Equal(t, []InterfacePair{
		{IA: 3, Ingress: 0, Egress: 4},
		{IA: 2, Ingress: 3, Egress: 1},
		{IA: 1, Ingress: 2, Egress: 0},
	}, reverseInterfacePairs(pairs))
}

// noteWith builds the PCB note of an AS advertising the given marketplaces,
// each of them named after the address it is reachable at.
func noteWith(addresses ...string) string {
	entries := make([]string, 0, len(addresses))
	for _, address := range addresses {
		entries = append(entries, fmt.Sprintf(
			`{
			  	"name":%q,
			  	"api_protocol":"connectrpc/TLS/TCP",
			  	"api_address":%q,`+
				`"client_registration_website":"https://example.com"}`, address, address))
	}
	list := ""
	for i, entry := range entries {
		if i > 0 {
			list += ","
		}
		list += entry
	}
	return `{"hummingbird":[` + list + `]}`
}

// pathWithNotes returns a path of len(notes) ASes, the i-th of them carrying notes[i].
func pathWithNotes(notes ...string) snet.Path {
	ifaces := []snet.PathInterface{}
	for i := range notes {
		ia := addr.MustParseIA(fmt.Sprintf("1-ff00:0:%d", 110+i))
		if i > 0 {
			ifaces = append(ifaces, snet.PathInterface{IA: ia, ID: iface.ID(1)})
		}
		if i < len(notes)-1 {
			ifaces = append(ifaces, snet.PathInterface{IA: ia, ID: iface.ID(2)})
		}
	}
	return snetpath.Path{
		Src: addr.MustParseIA("1-ff00:0:110"),
		Dst: addr.MustParseIA(fmt.Sprintf("1-ff00:0:%d", 110+len(notes)-1)),
		Meta: snet.PathMetadata{
			Interfaces: ifaces,
			Notes:      notes,
		},
	}
}

// addressesOf names the marketplaces of a coverage by the address they live at, which is
// what noteWith made their name.
func addressesOf(coverage []NoteEntry) []string {
	addresses := make([]string, 0, len(coverage))
	for _, entry := range coverage {
		addresses = append(addresses, entry.APIAddress)
	}
	return addresses
}
