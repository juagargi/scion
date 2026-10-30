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
	"slices"
	"time"

	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// PathMarketplaces are the marketplaces that the ASes of one path advertise in their PCBs,
// together with the clients talking to them.
// A client reserving the whole path needs a client for each of the on-path ASes.
type PathMarketplaces struct {
	// Hops are the ASes to buy reservations for, in path order. They need not be every AS of
	// the path: a partially reserved path only buys for some of them.
	Hops []snetpath.BaseHop
	// PathASes is the Hummingbird metadata of each AS of Hops, same length and order.
	// Entry i belongs to the same AS as Hops[i].
	// If AS i doesn't advertise a valid marketplace, its entry is empty.
	PathASes []ASNotes

	// clients is the client talking to each advertised marketplace.
	// Every marketplace of Metadata is a key.
	// Entries are nil until `Connect` dials them.
	clients map[NoteEntry]*Client
}

// Close closes all cached client instances accessing the marketplaces.
// They must be recreated with Connect before using them again.
func (m *PathMarketplaces) Close() error {
	// Get the unique clients.
	clients := make(map[*Client]struct{})
	for k, c := range m.clients {
		if c == nil {
			continue
		}
		clients[c] = struct{}{}
		m.clients[k] = nil
	}

	// Close all unique clients, report all errors.
	var errs []error
	for c := range clients {
		if err := c.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// NewPathMarketplaces discovers the marketplaces advertised by the ASes of the given path.
// Only the marketplaces that carry an API address are kept, and at most one per address per AS,
// since the several entries of one marketplace differ only in the protocol used to reach it.
// No marketplace is dialed: use Connect or ConnectAll for that.
func NewPathMarketplaces(p snet.Path) (*PathMarketplaces, error) {
	return NewPathMarketplacesForASes(p, nil)
}

// NewPathMarketplacesForASes discovers the marketplaces of the given subset of the path.
// indices are positions into the base hops of p, in strictly ascending order; nil means
// every AS of the path.
func NewPathMarketplacesForASes(p snet.Path, indices []int) (*PathMarketplaces, error) {
	if p == nil || p.Metadata() == nil {
		return nil, serrors.New("path without metadata")
	}
	hops := snetpath.InterfacesToBaseHops(p.Metadata().Interfaces)
	notes := p.Metadata().Notes
	if len(notes) != len(hops) {
		return nil, serrors.New("inconsistent path metadata",
			"notes", len(notes), "ases", len(hops))
	}
	if indices == nil {
		return NewPathMarketplacesFromNotes(hops, notes)
	}
	prev := -1
	selectedHops := make([]snetpath.BaseHop, 0, len(indices))
	selectedNotes := make([]string, 0, len(indices))
	for _, idx := range indices {
		if idx < 0 || idx >= len(hops) {
			return nil, serrors.New("AS index out of range", "index", idx, "ases", len(hops))
		}
		if idx <= prev {
			return nil, serrors.New("AS indices must be strictly ascending",
				"index", idx, "previous", prev)
		}
		prev = idx
		selectedHops = append(selectedHops, hops[idx])
		selectedNotes = append(selectedNotes, notes[idx])
	}
	return NewPathMarketplacesFromNotes(selectedHops, selectedNotes)
}

// NewPathMarketplacesFromNotes builds the marketplaces from an explicit list of ASes and their
// PCB notes, for callers that do not hold an snet.Path.
// hops and notes must have the same length, entry i of notes belonging to hops[i].
func NewPathMarketplacesFromNotes(
	hops []snetpath.BaseHop,
	notes []string,
) (*PathMarketplaces, error) {
	if len(notes) != len(hops) {
		return nil, serrors.New("inconsistent path metadata",
			"notes", len(notes), "ases", len(hops))
	}
	metadata := make([]ASNotes, len(notes))
	clients := make(map[NoteEntry]*Client)
	for i, note := range notes {
		metadata[i] = ASNotes{
			Notes: ParseNote(note).UniqueAPIsOnly().WithAPI(),
		}
		for _, entry := range metadata[i].Notes {
			clients[entry] = nil
		}
	}
	return &PathMarketplaces{
		Hops:     slices.Clone(hops),
		PathASes: metadata,
		clients:  clients,
	}, nil
}

// FullCoverageCount returns the smallest set of the advertised marketplaces that together
// sell the reservations of every AS of the path, and the set size (member count).
// Several sets of that size may exist; the first one found is returned, since nothing else
// is known here that would tell them apart.
// A path where some AS advertises no reachable marketplace cannot be covered at all, and
// yields zero and no set.
// Although the set is represented with a []NoteEntry, there is no particular order to its elements.
func (m *PathMarketplaces) FullCoverageCount() (count int, coverage []NoteEntry) {
	for _, meta := range m.PathASes {
		if len(meta.Notes) == 0 {
			// One AS has no marketplace, impossible to cover the path.
			return 0, nil
		}
	}
	entries, covers := m.coverageSets()
	// The path has few ASes and each advertises few marketplaces,
	// so searching by increasing size is affordable and gives the true minimum.
	covered := make([]bool, len(m.PathASes))
	for size := 1; size <= len(entries); size++ {
		if chosen, ok := searchCoverage(covers, covered, size, nil); ok {
			coverage = make([]NoteEntry, 0, len(chosen))
			for _, i := range chosen {
				coverage = append(coverage, entries[i])
			}
			return len(coverage), coverage
		}
	}
	return 0, nil
}

// coverageSets returns the distinct advertised marketplaces, in the order the ASes of the
// path advertised them, and for each of them the indices of the ASes selling through it.
func (m *PathMarketplaces) coverageSets() (entries []NoteEntry, covers [][]int) {
	index := make(map[NoteEntry]int)
	for i, notes := range m.PathASes {
		for _, entry := range notes.Notes {
			j, ok := index[entry]
			if !ok {
				j = len(entries)
				index[entry] = j
				entries = append(entries, entry)
				covers = append(covers, nil)
			}
			covers[j] = append(covers[j], i)
		}
	}
	return entries, covers
}

// searchCoverage looks for at most `size` of the marketplaces in covers that together cover
// every AS not yet covered, and returns their indices.
// It branches on the first AS still uncovered, so that only marketplaces that make progress
// are ever tried, and every cover is reached exactly once.
func searchCoverage(covers [][]int, covered []bool, size int, chosen []int) ([]int, bool) {
	target := -1
	for i, done := range covered {
		if !done {
			target = i
			break
		}
	}
	if target < 0 {
		// All found.
		return chosen, true
	}
	if size == 0 {
		// Can't find such a small coverage.
		return nil, false
	}
	for i, ases := range covers {
		if !slices.Contains(ases, target) {
			continue
		}
		added := make([]int, 0, len(ases))
		for _, as := range ases {
			if !covered[as] {
				covered[as] = true
				added = append(added, as)
			}
		}
		found, ok := searchCoverage(covers, covered, size-1, append(chosen, i))
		for _, as := range added {
			covered[as] = false
		}
		if ok {
			return found, true
		}
	}
	return nil, false
}

// Connect dials the marketplace advertised at apiAddress, so that AcquireReservations can
// buy the assets of the ASes selling through it.
// The querier and the topology are only used for marketplaces reached over SCION, and
// `insecure` disables the validation of the server certificate.
// All the marketplace entries with the same API address share the same client.
func (m *PathMarketplaces) Connect(
	ctx context.Context,
	apiAddress string,
	jwt string,
	querier snet.PathQuerier,
	topo snet.Topology,
	insecure bool,
) error {
	advertised := make([]NoteEntry, 0, len(m.clients))
	for entry := range m.clients {
		if entry.APIAddress == apiAddress {
			advertised = append(advertised, entry)
		}
	}
	if len(advertised) == 0 {
		return serrors.New("no AS of the path advertises this marketplace",
			"api_address", apiAddress)
	}
	client, err := NewClient(ctx, apiAddress, jwt,
		ClientOptions{Querier: querier, Topology: topo, Insecure: insecure})
	if err != nil {
		return serrors.Wrap("connecting to marketplace", err, "api_address", apiAddress)
	}
	for _, entry := range advertised {
		m.clients[entry] = client
	}
	return nil
}

// ConnectAll dials every marketplace of jwts, which maps the API address of a marketplace to
// the token authenticating this client there.
// It stops at the first marketplace that cannot be reached, leaving the ones already dialed
// connected.
func (m *PathMarketplaces) ConnectAll(
	ctx context.Context,
	jwts map[string]string,
	querier snet.PathQuerier,
	topo snet.Topology,
	insecure bool,
) error {
	for apiAddress, jwt := range jwts {
		if err := m.Connect(ctx, apiAddress, jwt, querier, topo, insecure); err != nil {
			return err
		}
	}
	return nil
}

// AcquireReservations obtains the reservations of every AS of m.Hops,
// also the ones for the reverse reservation if reverseBwInKbps is not zero, nil otherwise.
// The returned forward slice is aligned with m.Hops (entry i is the flyover of m.Hops[i]).
// The reverse slice is aligned with the reversed m.Hops (entry j belongs to
// m.Hops[len(m.Hops)-1-j]), so it mirrors the forward selection exactly.
// The caller expands both to the full path length when only a subset of the path was selected.
// The remaining arguments are the ones of ObtainReservationsForInterfacePairs,
// which does the real acquisition once the ASes have been grouped by the marketplace.
func (m *PathMarketplaces) AcquireReservations(
	ctx context.Context,
	bwInKbps uint32,
	reverseBwInKbps uint32,
	startsAt time.Time,
	stopsAt time.Time,
	maxPrice uint64,
	buyMode BuyMode,
	fetchReservations bool,
	combineAssets bool,
	numRetries int,
) (forward []*snetpath.Hop, reverse []*snetpath.Hop, err error) {
	clients, err := m.clientPerAS()
	if err != nil {
		return nil, nil, err
	}
	pairs := interfacePairsFromBaseHops(m.Hops)
	forward, err = buyPerMarketplace(ctx, pairs, clients, bwInKbps, startsAt, stopsAt,
		maxPrice, buyMode, fetchReservations, combineAssets, numRetries)
	if err != nil {
		return nil, nil, err
	}
	if reverseBwInKbps == 0 {
		return forward, nil, nil
	}
	// The reverse direction visits the same ASes in the opposite order, so the client of
	// its hop j is the one of the AS at the mirrored index.
	reverseClients := make([]*Client, len(clients))
	for i, client := range clients {
		reverseClients[len(clients)-1-i] = client
	}
	reverse, err = buyPerMarketplace(ctx, reverseInterfacePairs(pairs), reverseClients,
		reverseBwInKbps, startsAt, stopsAt, maxPrice, buyMode, fetchReservations,
		combineAssets, numRetries)
	if err != nil {
		return nil, nil, serrors.Wrap("obtaining the reverse reservations", err)
	}
	return forward, reverse, nil
}

// clientPerAS returns the marketplace to buy each AS of the path from,
// which is the first one it advertised that has been connected to.
func (m *PathMarketplaces) clientPerAS() ([]*Client, error) {
	clients := make([]*Client, len(m.PathASes))
	for i, meta := range m.PathASes {
		for _, entry := range meta.Notes {
			if client := m.clients[entry]; client != nil {
				clients[i] = client
				break
			}
		}
		if clients[i] == nil {
			return nil, serrors.New("no marketplace connected for an AS of the path",
				"ia", m.Hops[i].IA, "advertised", len(meta.Notes))
		}
	}
	return clients, nil
}

// buyPerMarketplace buys the reservations of pairs, asking each marketplace only for the
// ASes it sells: clients[i] is the marketplace selling pairs[i].
// The reservations are returned in the order of pairs, whatever the grouping was.
func buyPerMarketplace(
	ctx context.Context,
	pairs []InterfacePair,
	clients []*Client,
	bwInKbps uint32,
	startsAt time.Time,
	stopsAt time.Time,
	maxPrice uint64,
	buyMode BuyMode,
	fetchReservations bool,
	combineAssets bool,
	numRetries int,
) ([]*snetpath.Hop, error) {
	// In the order of the path, so that the same path always buys in the same order.
	order := []*Client{}
	grouped := map[*Client][]int{}
	for i, client := range clients {
		if _, ok := grouped[client]; !ok {
			order = append(order, client)
		}
		grouped[client] = append(grouped[client], i)
	}
	hops := make([]*snetpath.Hop, len(pairs))
	for _, client := range order {
		indices := grouped[client]
		group := make([]InterfacePair, 0, len(indices))
		for _, i := range indices {
			group = append(group, pairs[i])
		}
		bought, err := client.ObtainReservationsForInterfacePairs(ctx, group, bwInKbps,
			startsAt, stopsAt, maxPrice, buyMode, fetchReservations, combineAssets,
			numRetries)
		if err != nil {
			return nil, err
		}
		if len(bought) != len(indices) {
			return nil, serrors.New("marketplace returned a wrong number of reservations",
				"expected", len(indices), "actual", len(bought))
		}
		for j, i := range indices {
			hops[i] = bought[j]
		}
	}
	return hops, nil
}

// reverseInterfacePairs returns the pairs of the reverse direction of a path, i.e. the same ASes
// in the opposite order, each traversed from its egress to its ingress.
func reverseInterfacePairs(pairs []InterfacePair) []InterfacePair {
	reversed := make([]InterfacePair, len(pairs))
	for i, pair := range pairs {
		reversed[len(pairs)-1-i] = InterfacePair{
			IA:      pair.IA,
			Ingress: pair.Egress,
			Egress:  pair.Ingress,
		}
	}
	return reversed
}
