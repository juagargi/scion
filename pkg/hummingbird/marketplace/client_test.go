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
	"testing"
	"time"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"github.com/scionproto/scion/pkg/snet"
	"github.com/stretchr/testify/assert"
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
					AssetId:         "1",
					StartsAtExactly: timestamppb.New(start),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 1)),
					BandwidthExact:  1,
				},
				{
					AssetId:         "2",
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 1)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 2)),
					BandwidthExact:  1,
				},
			},
			assets: []*hummingbird.SearchAsset{
				{
					AssetId:         "1",
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start),
					StopsAt:         timestamppb.New(start.Add(time.Second * 1)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         "2",
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
					AssetId:         "1",
					StartsAtExactly: timestamppb.New(start),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 1)),
					BandwidthExact:  1,
				},
				{
					AssetId:         "3",
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 1)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 2)),
					BandwidthExact:  1,
				},
			},
			assets: []*hummingbird.SearchAsset{
				{
					AssetId:         "1",
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start),
					StopsAt:         timestamppb.New(start.Add(time.Second * 1)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         "2",
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 2)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           2,
				},
				{
					AssetId:         "3",
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
					AssetId:         "1",
					StartsAtExactly: timestamppb.New(start),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 1)),
					BandwidthExact:  1,
				},
				{
					AssetId:         "2",
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 1)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 2)),
					BandwidthExact:  1,
				},
				{
					AssetId:         "3",
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 2)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 4)),
					BandwidthExact:  1,
				},
				{
					AssetId:         "4",
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 4)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 5)),
					BandwidthExact:  1,
				},
			},
			assets: []*hummingbird.SearchAsset{
				{
					AssetId:         "1",
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start),
					StopsAt:         timestamppb.New(start.Add(time.Second * 1)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         "2",
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 2)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         "3",
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 4)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         "4",
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
					AssetId:         "1",
					StartsAtExactly: timestamppb.New(start),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 1)),
					BandwidthExact:  1,
				},
				{
					AssetId:         "3",
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 1)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 4)),
					BandwidthExact:  1,
				},
				{
					AssetId:         "4",
					StartsAtExactly: timestamppb.New(start.Add(time.Second * 4)),
					StopsAtExactly:  timestamppb.New(start.Add(time.Second * 5)),
					BandwidthExact:  1,
				},
			},
			assets: []*hummingbird.SearchAsset{
				{
					AssetId:         "1",
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start),
					StopsAt:         timestamppb.New(start.Add(time.Second * 1)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         "2",
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 2)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         "3",
					Bandwidth:       1,
					StartsAt:        timestamppb.New(start.Add(time.Second * 1)),
					StopsAt:         timestamppb.New(start.Add(time.Second * 4)),
					TimeGranularity: 1,
					BandwidthMin:    1,
					TimeMinDuration: 1,
					Price:           1,
				},
				{
					AssetId:         "4",
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
	c := &MarketplaceClient{}
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
