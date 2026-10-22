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

package marketplace_test

import (
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/marketplace"
	"github.com/scionproto/scion/marketplace/db"
)

const day = 24 * time.Hour

// statsBase is the reference instant of the tests,
// aligned to a day so that it needs no rounding of its own.
var statsBase = time.Date(2026, time.August, 28, 0, 0, 0, 0, time.UTC)

// TestBandwidthUtilization covers the share reported for an interval,
// and above all the interval in which nothing was published:
// it must be zero and never NaN or infinite,
// because protobuf JSON writes those as strings and they poison any aggregation.
func TestBandwidthUtilization(t *testing.T) {
	testCases := map[string]struct {
		bought    uint64
		published uint64
		expected  float64
	}{
		"nothing published, nothing bought": {bought: 0, published: 0, expected: 0},
		"nothing published, some bought":    {bought: 5000, published: 0, expected: 0},
		"nothing bought":                    {bought: 0, published: 4000, expected: 0},
		"a quarter bought":                  {bought: 1000, published: 4000, expected: 0.25},
		"everything bought":                 {bought: 4000, published: 4000, expected: 1},
		"more bought than published":        {bought: 8000, published: 4000, expected: 2},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := marketplace.BandwidthUtilization(tc.bought, tc.published)
			assert.False(t, math.IsNaN(got), "utilization must never be NaN")
			assert.False(t, math.IsInf(got, 0), "utilization must never be infinite")
			assert.Equal(t, tc.expected, got)
		})
	}
}

// TestStatisticsWindow covers the rounding of the requested window,
// and the window given backwards, which is turned around so that its length is preserved
// instead of yielding a negative interval count.
func TestStatisticsWindow(t *testing.T) {
	testCases := map[string]struct {
		start       time.Time
		end         time.Time
		step        time.Duration
		granularity time.Duration
		wantStart   time.Time
		wantEnd     time.Time
		wantN       int
	}{
		"aligned window": {
			start: statsBase, end: statsBase.Add(3 * day),
			step: day, granularity: day,
			wantStart: statsBase, wantEnd: statsBase.Add(3 * day), wantN: 3,
		},
		"start is truncated down, end is rounded up": {
			start: statsBase.Add(5 * time.Hour), end: statsBase.Add(day + time.Hour),
			step: day, granularity: day,
			wantStart: statsBase, wantEnd: statsBase.Add(2 * day), wantN: 2,
		},
		"end before start is turned around": {
			start: statsBase.Add(3 * day), end: statsBase,
			step: day, granularity: day,
			wantStart: statsBase, wantEnd: statsBase.Add(3 * day), wantN: 3,
		},
		"end before start keeps the length when unaligned": {
			start: statsBase.Add(day + time.Hour), end: statsBase.Add(5 * time.Hour),
			step: day, granularity: day,
			wantStart: statsBase, wantEnd: statsBase.Add(2 * day), wantN: 2,
		},
		"empty window": {
			start: statsBase, end: statsBase,
			step: day, granularity: day,
			wantStart: statsBase, wantEnd: statsBase, wantN: 0,
		},
		"step larger than the window": {
			start: statsBase, end: statsBase.Add(day),
			step: 7 * day, granularity: day,
			wantStart: statsBase, wantEnd: statsBase.Add(day), wantN: 0,
		},
		"a step of zero yields no intervals rather than dividing by zero": {
			start: statsBase, end: statsBase.Add(3 * day),
			step: 0, granularity: day,
			wantStart: statsBase, wantEnd: statsBase.Add(3 * day), wantN: 0,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotStart, gotEnd, gotN := marketplace.StatisticsWindow(
				tc.start, tc.end, tc.step, tc.granularity)
			assert.Equal(t, tc.wantStart, gotStart, "window start")
			assert.Equal(t, tc.wantEnd, gotEnd, "window end")
			assert.Equal(t, tc.wantN, gotN, "number of intervals")
			assert.GreaterOrEqual(t, gotN, 0, "the interval count must never be negative")
			assert.False(t, gotEnd.Before(gotStart), "the window must not be backwards")
		})
	}
}

// TestBandwidthPerIntervalGaps checks that an interval with nothing published and
// two assets with a gap between them report a zero utilization.
func TestBandwidthPerIntervalGaps(t *testing.T) {
	// Two one-day assets, days 0 and 4 of a six day window.
	assets := []*db.DBStat{
		{Bandwidth: 100, Price: 2, StartsAt: statsBase.Unix(), StopsAt: statsBase.Add(day).Unix()},
		{
			Bandwidth: 100, Price: 2,
			StartsAt: statsBase.Add(4 * day).Unix(), StopsAt: statsBase.Add(5 * day).Unix(),
		},
	}
	windowStart, windowEnd, n := marketplace.StatisticsWindow(
		statsBase, statsBase.Add(6*day), day, day)
	require.Equal(t, 6, n)

	published, _ := marketplace.BandwidthPerInterval(assets, windowStart, windowEnd, day, n)
	require.Len(t, published, 6)

	perDay := uint64(100) * uint64(day.Seconds())
	expected := []uint64{perDay, 0, 0, 0, perDay, 0}
	assert.Equal(t, expected, published)

	// Nothing was bought, so every interval reports zero, including the gaps, and no
	// interval reports NaN.
	for i, p := range published {
		got := marketplace.BandwidthUtilization(0, p)
		assert.False(t, math.IsNaN(got), "interval %d must not be NaN", i)
		assert.Equal(t, float64(0), got, "interval %d", i)
	}
}

// TestBandwidthPerIntervalEmptyWindow checks that no assets means no panic and no entries,
// which is what a window outside every asset's validity produces.
func TestBandwidthPerIntervalEmptyWindow(t *testing.T) {
	windowStart, windowEnd, n := marketplace.StatisticsWindow(
		statsBase, statsBase.Add(3*day), day, day)
	published, income := marketplace.BandwidthPerInterval(nil, windowStart, windowEnd, day, n)
	require.Len(t, published, 3)
	require.Len(t, income, 3)
	for i := range published {
		assert.Zero(t, published[i])
		assert.Zero(t, income[i])
		assert.Equal(t, float64(0), marketplace.BandwidthUtilization(0, published[i]))
	}
}

// TestBandwidthPerIntervalRevenue checks the revenue that accompanies the bandwidth,
// so that the extraction of the loop is covered for both of its outputs.
func TestBandwidthPerIntervalRevenue(t *testing.T) {
	assets := []*db.DBStat{
		{Bandwidth: 100, Price: 3, StartsAt: statsBase.Unix(), StopsAt: statsBase.Add(day).Unix()},
	}
	windowStart, windowEnd, n := marketplace.StatisticsWindow(
		statsBase, statsBase.Add(day), day, day)
	require.Equal(t, 1, n)
	bandwidth, income := marketplace.BandwidthPerInterval(assets, windowStart, windowEnd, day, n)
	perDay := uint64(100) * uint64(day.Seconds())
	assert.Equal(t, []uint64{perDay}, bandwidth)
	assert.Equal(t, []uint64{perDay * 3}, income)
}
