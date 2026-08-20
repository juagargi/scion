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

package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/marketplace/db"
)

// TestSplitAsset covers carving a purchase out of an asset: what the buyer gets,
// and what goes back on the market. The remainders must never overlap the
// purchase, otherwise the same capacity could be sold twice.
func TestSplitAsset(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	stop := start.Add(time.Hour)
	asset := func() *db.DBAsset {
		return &db.DBAsset{
			Bandwidth: 1000,
			StartAt:   start,
			StopsAt:   stop,
		}
	}

	testCases := map[string]struct {
		split      RequestedSplit
		expected   AssetSegment
		remainders []AssetSegment
	}{
		"whole asset": {
			split:    RequestedSplit{ExactFrom: start, ExactTo: stop, ExactBandwidth: 1000},
			expected: AssetSegment{StartsAt: start, StopsAt: stop, Bandwidth: 1000},
		},
		"tail, only a left remainder": {
			split: RequestedSplit{
				ExactFrom:      start.Add(10 * time.Minute),
				ExactTo:        stop,
				ExactBandwidth: 1000,
			},
			expected: AssetSegment{
				StartsAt:  start.Add(10 * time.Minute),
				StopsAt:   stop,
				Bandwidth: 1000,
			},
			remainders: []AssetSegment{
				{StartsAt: start, StopsAt: start.Add(10 * time.Minute), Bandwidth: 1000},
			},
		},
		"head, only a right remainder": {
			split: RequestedSplit{
				ExactFrom:      start,
				ExactTo:        start.Add(10 * time.Minute),
				ExactBandwidth: 1000,
			},
			expected: AssetSegment{
				StartsAt:  start,
				StopsAt:   start.Add(10 * time.Minute),
				Bandwidth: 1000,
			},
			remainders: []AssetSegment{
				{StartsAt: start.Add(10 * time.Minute), StopsAt: stop, Bandwidth: 1000},
			},
		},
		"middle, a left and a right remainder": {
			split: RequestedSplit{
				ExactFrom:      start.Add(10 * time.Minute),
				ExactTo:        start.Add(20 * time.Minute),
				ExactBandwidth: 1000,
			},
			expected: AssetSegment{
				StartsAt:  start.Add(10 * time.Minute),
				StopsAt:   start.Add(20 * time.Minute),
				Bandwidth: 1000,
			},
			remainders: []AssetSegment{
				{StartsAt: start, StopsAt: start.Add(10 * time.Minute), Bandwidth: 1000},
				{StartsAt: start.Add(20 * time.Minute), StopsAt: stop, Bandwidth: 1000},
			},
		},
		"part of the bandwidth of the whole asset": {
			split:    RequestedSplit{ExactFrom: start, ExactTo: stop, ExactBandwidth: 400},
			expected: AssetSegment{StartsAt: start, StopsAt: stop, Bandwidth: 400},
			remainders: []AssetSegment{
				{StartsAt: start, StopsAt: stop, Bandwidth: 600},
			},
		},
		"part of the bandwidth of a middle slice": {
			split: RequestedSplit{
				ExactFrom:      start.Add(10 * time.Minute),
				ExactTo:        start.Add(20 * time.Minute),
				ExactBandwidth: 400,
			},
			expected: AssetSegment{
				StartsAt:  start.Add(10 * time.Minute),
				StopsAt:   start.Add(20 * time.Minute),
				Bandwidth: 400,
			},
			remainders: []AssetSegment{
				// The bandwidth that was not bought, for the whole window.
				{StartsAt: start, StopsAt: stop, Bandwidth: 600},
				{StartsAt: start, StopsAt: start.Add(10 * time.Minute), Bandwidth: 400},
				{StartsAt: start.Add(20 * time.Minute), StopsAt: stop, Bandwidth: 400},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			result, err := SplitAsset(asset(), tc.split)
			require.NoError(t, err)
			assert.Equal(t, tc.expected, result.Split)
			assert.ElementsMatch(t, tc.remainders, result.Remainders)

			// What is sold and what is left must add up to the asset, so no
			// remainder may overlap the purchase in both time and bandwidth.
			for _, remainder := range result.Remainders {
				overlapsInTime := remainder.StartsAt.Before(result.Split.StopsAt) &&
					remainder.StopsAt.After(result.Split.StartsAt)
				if !overlapsInTime {
					continue
				}
				assert.Equal(t, asset().Bandwidth, remainder.Bandwidth+result.Split.Bandwidth,
					"remainder %v overlaps the purchase %v in time, so together they must "+
						"not exceed the bandwidth of the asset", remainder, result.Split)
			}
		})
	}
}

// TestSplitAssetRejects checks that SplitAsset returns error when the asset cannot be split into
// the required parts.
func TestSplitAssetRejects(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	stop := start.Add(time.Hour)
	asset := &db.DBAsset{Bandwidth: 1000, StartAt: start, StopsAt: stop}

	testCases := map[string]RequestedSplit{
		"starts before the asset": {
			ExactFrom: start.Add(-time.Second), ExactTo: stop, ExactBandwidth: 1000,
		},
		"stops after the asset": {
			ExactFrom: start, ExactTo: stop.Add(time.Second), ExactBandwidth: 1000,
		},
		"empty range": {
			ExactFrom: start, ExactTo: start, ExactBandwidth: 1000,
		},
		"reversed range": {
			ExactFrom: stop, ExactTo: start, ExactBandwidth: 1000,
		},
		"sub second precision": {
			ExactFrom: start.Add(time.Millisecond), ExactTo: stop, ExactBandwidth: 1000,
		},
		"more bandwidth than the asset has": {
			ExactFrom: start, ExactTo: stop, ExactBandwidth: 1001,
		},
	}

	for name, split := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := SplitAsset(asset, split)
			assert.Error(t, err)
		})
	}
}
