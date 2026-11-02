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
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"github.com/scionproto/scion/pkg/proto/hummingbird/v1/hummingbirdconnect"
	"github.com/scionproto/scion/pkg/snet"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestInterfacePairsFromInterfaces(t *testing.T) {
	interfaces := []snet.PathInterface{
		{IA: addr.MustParseIA("1-ff00:0:111"), ID: 41},
		{IA: addr.MustParseIA("1-ff00:0:110"), ID: 1},
		{IA: addr.MustParseIA("1-ff00:0:110"), ID: 2},
		{IA: addr.MustParseIA("1-ff00:0:112"), ID: 1},
	}

	assert.Equal(t, []InterfacePair{
		{IA: uint64(addr.MustParseIA("1-ff00:0:111")), Ingress: 0, Egress: 41},
		{IA: uint64(addr.MustParseIA("1-ff00:0:110")), Ingress: 1, Egress: 2},
		{IA: uint64(addr.MustParseIA("1-ff00:0:112")), Ingress: 1, Egress: 0},
	}, interfacePairsFromInterfaces(interfaces))
	assert.Empty(t, interfacePairsFromInterfaces(nil))
}

func TestIsSCIONURL(t *testing.T) {
	tests := map[string]struct {
		url  string
		want bool
	}{
		"SCION IP address":   {url: "[1-ff00:0:111,127.0.0.1]:9888", want: true},
		"SCION hostname":     {url: "[1-ff00:0:111,marketplace.invalid]:9888", want: true},
		"TCP IPv4 address":   {url: "https://127.0.0.1:8888", want: false},
		"TCP IPv6 address":   {url: "https://[::1]:8888", want: false},
		"malformed URL":      {url: "https://https://localhost:8888", want: false},
		"invalid SCION IA":   {url: "[invalid,127.0.0.1]:9888", want: false},
		"missing SCION host": {url: "[1-ff00:0:111]:9888", want: false},
		"missing SCION port": {url: "[1-ff00:0:111,127.0.0.1]", want: false},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsSCIONURL(tc.url))
		})
	}
}

func TestCombine(t *testing.T) {
	type Test struct {
		duration         time.Duration
		bw               uint32
		splitCombineCost uint64
		assets           []*hummingbird.SearchAsset
		expectedResponse []*hummingbird.BuyAsset
	}
	start, _ := time.Parse(time.RFC3339, "2026-07-07T00:00:00Z")
	tests := map[string]Test{
		"baseCombine": {
			duration: time.Second * 2,
			bw:       1,
			expectedResponse: []*hummingbird.BuyAsset{
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 1},
					StartsAtExactly: timestamppb.New(start),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 1)),
					BandwidthExact:  1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 2},
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 1)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 2)),
					BandwidthExact:  1,
				},
			},
			assets: []*hummingbird.SearchAsset{
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 1},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start),
					StopsAt:         timestamppb.New(start.Add(time.Second * 1)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 2},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 2)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
			},
		},
		"baseCombineChooseCheaper": {
			duration: time.Second * 2,
			bw:       1,
			expectedResponse: []*hummingbird.BuyAsset{
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 1},
					StartsAtExactly: timestamppb.New(start),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 1)),
					BandwidthExact:  1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 3},
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 1)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 2)),
					BandwidthExact:  1,
				},
			},
			assets: []*hummingbird.SearchAsset{
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 1},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start),
					StopsAt:         timestamppb.New(start.Add(time.Second * 1)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 2},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 2)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           2,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 3},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 2)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
			},
		},
		"combineOverlappingSolutions": {
			duration: time.Second * 5,
			bw:       1,
			expectedResponse: []*hummingbird.BuyAsset{
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 1},
					StartsAtExactly: timestamppb.New(start),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 1)),
					BandwidthExact:  1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 2},
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 1)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 2)),
					BandwidthExact:  1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 3},
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 2)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 4)),
					BandwidthExact:  1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 4},
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 4)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 5)),
					BandwidthExact:  1,
				},
			},
			assets: []*hummingbird.SearchAsset{
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 1},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start),
					StopsAt:         timestamppb.New(start.Add(time.Second * 1)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 2},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 2)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 3},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 4)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 4},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 4)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 5)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
			},
		},
		"combineOverlappingSolutionsWithCombineCost": {
			duration:         time.Second * 5,
			bw:               1,
			splitCombineCost: 10,
			expectedResponse: []*hummingbird.BuyAsset{
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 1},
					StartsAtExactly: timestamppb.New(start),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 1)),
					BandwidthExact:  1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 3},
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 1)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 4)),
					BandwidthExact:  1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 4},
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 4)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 5)),
					BandwidthExact:  1,
				},
			},
			assets: []*hummingbird.SearchAsset{
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 1},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start),
					StopsAt:         timestamppb.New(start.Add(time.Second * 1)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 2},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 2)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 3},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 4)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         []byte{0, 0, 0, 0, 0, 0, 0, 4},
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 4)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 5)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
			},
		},
	}
	c := &Client{}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			assert.NotEqual(t, 0, tc.bw)
			buyAssets, _, ok := c.recursiveSelectStart(tc.assets, tc.bw, start, start.Add(tc.duration), tc.splitCombineCost)
			if tc.expectedResponse == nil {
				assert.False(t, ok)
			} else {
				assert.NotEmpty(t, buyAssets)
				assert.Len(t, buyAssets, len(tc.expectedResponse))
				for i := range buyAssets {
					assert.Equal(t, tc.expectedResponse[i].AssetId, buyAssets[i].AssetId)
					assert.Equal(t, tc.expectedResponse[i].StartsAtExactly.Seconds, buyAssets[i].StartsAtExactly.Seconds)
					assert.Equal(t, tc.expectedResponse[i].StopsAtExactly.Seconds, buyAssets[i].StopsAtExactly.Seconds)
					assert.Equal(t, tc.expectedResponse[i].BandwidthExact, buyAssets[i].BandwidthExact)
				}
			}
		})
	}
}

func TestObtainReservationsRunsLookupsConcurrentlyAndBuysOnlyMissing(t *testing.T) {
	startsAt := time.Unix(1_000_000, 0)
	stopsAt := startsAt.Add(time.Minute)
	// Info is skipped without combining, so: fetch + 3 searches for each of the two pairs.
	mock := &mockMarketplace{
		startsAt:    startsAt,
		stopsAt:     stopsAt,
		barrierSize: 1 + 2*3,
		barrier:     make(chan struct{}),
		requests:    map[string]int{},
	}
	client := &Client{Marketplace: mock}
	pairs := []InterfacePair{
		{IA: 1, Ingress: 3, Egress: 4}, // already reserved
		{IA: 1, Ingress: 1, Egress: 2}, // must be bought
	}

	hops, err := client.ObtainReservationsForInterfacePairs(
		context.Background(), pairs, 100, startsAt, stopsAt, 1_000_000_000, FailOnError,
		true, false, 0)
	require.NoError(t, err)
	require.Len(t, hops, 2)

	assert.Equal(t, uint32(7), hops[0].Flyover.ResID, "existing reservation is kept")
	assert.Equal(t, uint32(9), hops[1].Flyover.ResID, "missing hop is redeemed")
	require.Len(t, mock.bought, 1, "a single batched purchase")
	require.Len(t, mock.bought[0], 1, "only the missing hop is bought")
	assert.Equal(t, []byte("asset"), mock.bought[0][0].AssetId)
	assert.Zero(t, mock.requests["info"], "the fee is not needed without combining")
}

func TestObtainReservationsSkipsLookupWhenNothingIsBought(t *testing.T) {
	startsAt := time.Unix(1_000_000, 0)
	mock := &mockMarketplace{
		startsAt:    startsAt,
		stopsAt:     startsAt.Add(time.Minute),
		barrierSize: 1 + 3,
		barrier:     make(chan struct{}),
		requests:    map[string]int{},
	}
	client := &Client{Marketplace: mock}

	hops, err := client.ObtainReservationsForInterfacePairs(
		context.Background(), []InterfacePair{{IA: 1, Ingress: 3, Egress: 4}}, 100,
		startsAt, startsAt.Add(time.Minute), 1_000_000_000, FailOnError, true, false, 0)
	require.NoError(t, err)
	require.Len(t, hops, 1)
	assert.Equal(t, uint32(7), hops[0].Flyover.ResID)
	assert.Empty(t, mock.bought)
	assert.Zero(t, mock.requests["owned"])
}

func TestSearchAllAssetsPaging(t *testing.T) {
	asset := func(id string) *hummingbird.SearchAsset {
		return &hummingbird.SearchAsset{AssetId: []byte(id)}
	}
	page := func(hasMore bool, ids ...string) *hummingbird.SearchAssetsResponse {
		rep := &hummingbird.SearchAssetsResponse{HasMore: hasMore}
		for _, id := range ids {
			rep.Assets = append(rep.Assets, asset(id))
		}
		return rep
	}

	testCases := map[string]struct {
		pages        []*hummingbird.SearchAssetsResponse
		wantIDs      []string
		wantRequests []uint32
	}{
		"has_more false on the first page needs one request": {
			pages:        []*hummingbird.SearchAssetsResponse{page(false, "a", "b")},
			wantIDs:      []string{"a", "b"},
			wantRequests: []uint32{0},
		},
		"has_more true continues, and false ends without an extra request": {
			pages: []*hummingbird.SearchAssetsResponse{
				page(true, "a", "b"), page(true, "c"), page(false, "d"),
			},
			wantIDs:      []string{"a", "b", "c", "d"},
			wantRequests: []uint32{0, 1, 2},
		},
		"no assets and has_more false needs one request": {
			pages:        []*hummingbird.SearchAssetsResponse{page(false)},
			wantRequests: []uint32{0},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			mock := &pagedMarketplace{pages: tc.pages}
			client := &Client{Marketplace: mock}

			assets, err := client.searchAllAssets(
				context.Background(), false, nil, nil, nil, 100, time.Unix(0, 0), time.Unix(1, 0))
			require.NoError(t, err)

			var ids []string
			for _, a := range assets {
				ids = append(ids, string(a.AssetId))
			}
			assert.Equal(t, tc.wantIDs, ids)
			assert.Equal(t, tc.wantRequests, mock.requests)
		})
	}
}

// mockMarketplace answers the requests of ObtainReservationsForInterfacePairs. The first
// `barrierSize` Info, FetchReservations and non-owned SearchAssets calls block until all of them
// are in flight, which only happens if the client issues them concurrently.
type mockMarketplace struct {
	hummingbirdconnect.MarketplaceServiceClient

	startsAt, stopsAt time.Time
	barrierSize       int

	mu       sync.Mutex
	arrived  int
	barrier  chan struct{}
	requests map[string]int
	bought   [][]*hummingbird.BuyAsset
}

func (f *mockMarketplace) wait(name string) error {
	f.mu.Lock()
	f.requests[name]++
	f.arrived++
	if f.arrived == f.barrierSize {
		close(f.barrier)
	}
	f.mu.Unlock()
	select {
	case <-f.barrier:
		return nil
	case <-time.After(5 * time.Second):
		return errors.New("requests were not concurrent")
	}
}

func (f *mockMarketplace) Info(context.Context, *connect.Request[hummingbird.MarketplaceInfoRequest],
) (*connect.Response[hummingbird.MarketplaceInfoResponse], error) {

	if err := f.wait("info"); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hummingbird.MarketplaceInfoResponse{}), nil
}

func (f *mockMarketplace) FetchReservations(
	context.Context, *connect.Request[hummingbird.FetchReservationsRequest],
) (*connect.Response[hummingbird.FetchReservationsResponse], error) {

	if err := f.wait("fetch"); err != nil {
		return nil, err
	}
	return connect.NewResponse(&hummingbird.FetchReservationsResponse{
		Reservations: []*hummingbird.Reservation{{
			Ia: 1, IngressId: 3, EgressId: 4, ReservationId: 7,
			AuthenticationKey: make([]byte, 16),
			StartsAt:          timestamppb.New(f.startsAt),
			StopsAt:           timestamppb.New(f.stopsAt),
		}},
	}), nil
}

func (f *mockMarketplace) SearchAssets(
	_ context.Context, req *connect.Request[hummingbird.SearchAssetsRequest],
) (*connect.Response[hummingbird.SearchAssetsResponse], error) {

	if !req.Msg.Owned {
		if err := f.wait("search"); err != nil {
			return nil, err
		}
	} else {
		f.mu.Lock()
		f.requests["owned"]++
		f.mu.Unlock()
	}
	rep := &hummingbird.SearchAssetsResponse{}
	// Only the interface pair asset exists, and only the first page has content.
	pair := req.Msg.IfIdIngress != nil && req.Msg.IfIdEgress != nil
	if req.Msg.Page == 0 && (pair || req.Msg.Owned) {
		rep.Assets = []*hummingbird.SearchAsset{{
			AssetId:         []byte("asset"),
			Ia:              1,
			IfIdIngress:     proto32(1),
			IfIdEgress:      proto32(2),
			Bandwidth:       1000,
			TimeGranularity: 1,
			StartsAt:        timestamppb.New(f.startsAt.Add(-time.Hour)),
			StopsAt:         timestamppb.New(f.stopsAt.Add(time.Hour)),
		}}
	}
	return connect.NewResponse(rep), nil
}

func (f *mockMarketplace) BuyAssets(
	_ context.Context, req *connect.Request[hummingbird.BuyAssetsRequest],
) (*connect.Response[hummingbird.BuyAssetsResponse], error) {

	f.mu.Lock()
	f.bought = append(f.bought, req.Msg.Assets)
	f.mu.Unlock()
	return connect.NewResponse(&hummingbird.BuyAssetsResponse{
		Assets: []*hummingbird.BoughtAsset{{AssetId: []byte("asset")}},
	}), nil
}

func (f *mockMarketplace) RedeemAsset(
	context.Context, *connect.Request[hummingbird.RedeemAssetRequest],
) (*connect.Response[hummingbird.RedeemAssetResponse], error) {

	return connect.NewResponse(&hummingbird.RedeemAssetResponse{
		ReservationId:     9,
		AuthenticationKey: make([]byte, 16),
	}), nil
}

func proto32(v uint32) *uint32 { return &v }

// pagedMarketplace answers searches with the scripted pages, and counts the requests.
type pagedMarketplace struct {
	hummingbirdconnect.MarketplaceServiceClient

	pages    []*hummingbird.SearchAssetsResponse
	requests []uint32
}

func (p *pagedMarketplace) SearchAssets(
	_ context.Context, req *connect.Request[hummingbird.SearchAssetsRequest],
) (*connect.Response[hummingbird.SearchAssetsResponse], error) {

	p.requests = append(p.requests, req.Msg.Page)
	if int(req.Msg.Page) >= len(p.pages) {
		return connect.NewResponse(&hummingbird.SearchAssetsResponse{}), nil
	}
	return connect.NewResponse(p.pages[req.Msg.Page]), nil
}
