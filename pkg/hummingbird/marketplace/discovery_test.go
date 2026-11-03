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
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"github.com/scionproto/scion/pkg/proto/hummingbird/v1/hummingbirdconnect"
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

// TestNewPathMarketplacesAt checks that a marketplace location given beforehand covers the
// selected ASes, whatever the path advertises, and also when it advertises nothing at all.
func TestNewPathMarketplacesAt(t *testing.T) {
	const tcp = "https://172.20.0.20:8888"
	const scion = "[1-ff00:0:111,172.20.0.20]:8888"
	// A topology without marketplace metainfo yields paths without any notes.
	noNotes := pathWithNotes("", "", "").(snetpath.Path)
	noNotes.Meta.Notes = nil
	testCases := map[string]struct {
		path     snet.Path
		indices  []int
		address  string
		protocol string
		ases     int
	}{
		"no notes": {
			path: noNotes, address: tcp, protocol: "connectrpc/TLS/TCP", ases: 3,
		},
		"over SCION": {
			path: noNotes, address: scion, protocol: "connectrpc/TLS/QUIC/SCION", ases: 3,
		},
		"other marketplaces advertised": {
			path:    pathWithNotes(noteWith("a"), noteWith("b"), noteWith("c")),
			address: tcp, protocol: "connectrpc/TLS/TCP", ases: 3,
		},
		"subset": {
			path: noNotes, indices: []int{0, 2}, address: tcp,
			protocol: "connectrpc/TLS/TCP", ases: 2,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			m, err := NewPathMarketplacesAt(tc.path, tc.indices, tc.address)
			require.NoError(t, err)
			all := snetpath.InterfacesToBaseHops(tc.path.Metadata().Interfaces)
			if tc.indices == nil {
				assert.Equal(t, all, m.Hops)
			} else {
				assert.Equal(t, []snetpath.BaseHop{all[0], all[2]}, m.Hops)
			}
			require.Len(t, m.PathASes, tc.ases)
			count, coverage := m.FullCoverageCount()
			assert.Equal(t, 1, count)
			require.Len(t, coverage, 1)
			assert.Equal(t, tc.address, coverage[0].APIAddress)
			assert.Equal(t, tc.protocol, coverage[0].APIProtocol)

			// Every AS buys from the client attached at the overridden address.
			client := &Client{}
			require.NoError(t, m.attach(tc.address, client))
			clients, err := m.clientPerAS()
			require.NoError(t, err)
			for _, c := range clients {
				assert.Same(t, client, c)
			}
		})
	}
}

// TestNewPathMarketplacesAtRejects checks the validation of the arguments.
func TestNewPathMarketplacesAtRejects(t *testing.T) {
	p := pathWithNotes("", "", "")
	t.Run("nil path", func(t *testing.T) {
		_, err := NewPathMarketplacesAt(nil, nil, "https://a")
		assert.Error(t, err)
	})
	t.Run("empty address", func(t *testing.T) {
		_, err := NewPathMarketplacesAt(p, nil, "")
		assert.Error(t, err)
	})
	t.Run("bad indices", func(t *testing.T) {
		_, err := NewPathMarketplacesAt(p, []int{2, 0}, "https://a")
		assert.Error(t, err)
	})
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

// singleHopMarketplaces returns the marketplaces of a path with one AS, 1-ff00:0:1 entered through
// interface 3 and left through interface 4, whose only marketplace is the given one.
func singleHopMarketplaces(marketplace hummingbirdconnect.MarketplaceServiceClient) *PathMarketplaces {
	entry := NoteEntry{APIAddress: "a"}
	return &PathMarketplaces{
		Hops:     []snetpath.BaseHop{{IA: 1, Ingress: 3, Egress: 4}},
		PathASes: []ASNotes{{Notes: []NoteEntry{entry}}},
		clients:  map[NoteEntry]*Client{entry: {Marketplace: marketplace}},
	}
}

// TestAcquireReservationsBuysBothDirectionsConcurrently checks that the forward and the reverse
// reservations are looked up at the same time: the mock marketplace only answers once the fetch and
// the three searches of both directions are in flight.
func TestAcquireReservationsBuysBothDirectionsConcurrently(t *testing.T) {
	startsAt := time.Unix(1_000_000, 0)
	stopsAt := startsAt.Add(time.Minute)
	mock := &mockMarketplace{
		startsAt:    startsAt,
		stopsAt:     stopsAt,
		barrierSize: 2 * (1 + 3),
		barrier:     make(chan struct{}),
		requests:    map[string]int{},
	}

	// The mock reports the forward hop (3 -> 4) as reserved, so only the reverse one is bought.
	forward, reverse, err := singleHopMarketplaces(mock).AcquireReservations(
		context.Background(), 100, 100, startsAt, stopsAt, 1_000_000_000, FailOnError,
		true, false, 0)
	require.NoError(t, err)
	require.Len(t, forward, 1)
	require.Len(t, reverse, 1)
	assert.Equal(t, uint32(7), forward[0].Flyover.ResID, "the existing reservation is kept")
	assert.Equal(t, uint32(9), reverse[0].Flyover.ResID, "the reverse hop is bought and redeemed")
	assert.Equal(t, 2, mock.requests["fetch"], "one fetch per direction")
	assert.Len(t, mock.bought, 1)
}

// failingReverseMarketplace fails every search of the reverse direction, and leaves the forward ones waiting until they are cancelled.
type failingReverseMarketplace struct {
	hummingbirdconnect.MarketplaceServiceClient

	forwardCancelled atomic.Bool
}

func (f *failingReverseMarketplace) FetchReservations(
	context.Context, *connect.Request[hummingbird.FetchReservationsRequest],
) (*connect.Response[hummingbird.FetchReservationsResponse], error) {

	return connect.NewResponse(&hummingbird.FetchReservationsResponse{}), nil
}

func (f *failingReverseMarketplace) SearchAssets(
	ctx context.Context, req *connect.Request[hummingbird.SearchAssetsRequest],
) (*connect.Response[hummingbird.SearchAssetsResponse], error) {

	// The reverse direction enters through interface 4 and leaves through interface 3.
	enters4 := req.Msg.IfIdIngress != nil && *req.Msg.IfIdIngress == 4
	leaves3 := req.Msg.IfIdIngress == nil && req.Msg.IfIdEgress != nil && *req.Msg.IfIdEgress == 3
	if enters4 || leaves3 {
		return nil, errors.New("reverse failure")
	}
	select {
	case <-ctx.Done():
		f.forwardCancelled.Store(true)
		return nil, ctx.Err()
	case <-time.After(5 * time.Second):
		return nil, errors.New("the forward direction was not cancelled")
	}
}

// TestAcquireReservationsCancelsTheOtherDirectionOnFailure checks that a failing direction stops
// the other one, and that the error reported is the cause, not the resulting cancellation.
func TestAcquireReservationsCancelsTheOtherDirectionOnFailure(t *testing.T) {
	mock := &failingReverseMarketplace{}
	start := time.Now()

	_, _, err := singleHopMarketplaces(mock).AcquireReservations(
		context.Background(), 100, 100, time.Unix(1_000_000, 0), time.Unix(1_000_060, 0),
		1_000_000_000, FailOnError, true, false, 0)

	require.Error(t, err)
	assert.ErrorContains(t, err, "reverse failure")
	assert.ErrorContains(t, err, "reverse reservations")
	assert.NotErrorIs(t, err, context.Canceled)
	assert.True(t, mock.forwardCancelled.Load(), "the forward direction must be cancelled")
	assert.Less(t, time.Since(start), 4*time.Second)
}

// TestAcquireReservationsWithoutReverseBuysOnlyForward checks that nothing is bought for the
// reverse direction if no bandwidth is requested for it.
func TestAcquireReservationsWithoutReverseBuysOnlyForward(t *testing.T) {
	startsAt := time.Unix(1_000_000, 0)
	mock := &mockMarketplace{
		startsAt:    startsAt,
		stopsAt:     startsAt.Add(time.Minute),
		barrierSize: 1 + 3,
		barrier:     make(chan struct{}),
		requests:    map[string]int{},
	}

	forward, reverse, err := singleHopMarketplaces(mock).AcquireReservations(
		context.Background(), 100, 0, startsAt, startsAt.Add(time.Minute), 1_000_000_000,
		FailOnError, true, false, 0)
	require.NoError(t, err)
	assert.Len(t, forward, 1)
	assert.Nil(t, reverse)
	assert.Equal(t, 1, mock.requests["fetch"])
}
