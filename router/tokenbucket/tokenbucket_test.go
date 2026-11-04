// Copyright 2025 ETH Zurich
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

package tokenbucket_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/hummingbird/bwencoding"
	"github.com/scionproto/scion/router/tokenbucket"
)

// TestTokenBucketAlgorithm checks that the token bucket implementation detects if a given
// amount of bytes exceed the allowance or not.
func TestTokenBucketAlgorithm(t *testing.T) {
	var startTime = time.Unix(0, 0)

	type entry struct {
		length      int
		arrivalTime time.Time
		result      bool
	}

	tests := []struct {
		name    string
		entries []entry
		bucket  *tokenbucket.TokenBucket
	}{
		{
			name:   "TestApplyDoesAllowArrivalBehindTheLastArrival",
			bucket: tokenbucket.NewTokenBucket(startTime.Add(1), 1024, 1024),
			entries: []entry{
				{
					length:      1,
					arrivalTime: startTime,
					result:      true,
				},
			},
		},
		{
			name:   "FullBandwidthCanBeConsumedAtOnce",
			bucket: tokenbucket.NewTokenBucket(startTime, 1024, 1024),
			entries: []entry{
				{
					length:      1024,
					arrivalTime: startTime,
					result:      true,
				},
				{
					length:      1,
					arrivalTime: startTime,
					result:      false,
				},
			},
		},
		{
			name:   "FullBandwidthCanBeConsumedOverMultiplePackets",
			bucket: tokenbucket.NewTokenBucket(startTime, 1024, 1024),
			entries: []entry{
				{
					length:      512,
					arrivalTime: startTime,
					result:      true,
				},
				{
					length:      512,
					arrivalTime: startTime,
					result:      true,
				},
				{
					length:      1,
					arrivalTime: startTime,
					result:      false,
				},
			},
		},
		{
			name:   "CurrentTokensRegenerate",
			bucket: tokenbucket.NewTokenBucket(startTime, 1024, 1024),
			entries: []entry{
				{
					length:      1024,
					arrivalTime: startTime,
					result:      true,
				},
				{
					length:      512,
					arrivalTime: startTime.Add(500 * time.Millisecond),
					result:      true,
				},
				{
					length:      1,
					arrivalTime: startTime.Add(500 * time.Millisecond),
					result:      false,
				},
			},
		},
		{
			name:   "CurrentTokensIsLimitedByCBS",
			bucket: tokenbucket.NewTokenBucket(startTime, 2048, 1024),
			entries: []entry{
				{
					length:      2049,
					arrivalTime: startTime.Add(1 * time.Second),
					result:      false,
				},
				{
					length:      2048,
					arrivalTime: startTime.Add(1 * time.Second),
					result:      true,
				},
				{
					length:      1,
					arrivalTime: startTime.Add(1 * time.Second),
					result:      false,
				},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for _, en := range tc.entries {
				assert.Equal(t, en.result, tc.bucket.Apply(en.length, en.arrivalTime), tc.name)
			}
		})
	}
}

func TestReconfigureAndApply(t *testing.T) {
	start := time.Unix(0, 0)
	bucket := tokenbucket.NewTokenBucket(start, 64, 64)
	require.True(t, bucket.Apply(64, start))
	require.False(t, bucket.Apply(1, start))

	// Reconfiguration is applied atomically with the packet. Advancing the clock
	// by one second refills at the new rate and observes the new burst limit.
	require.True(t, bucket.ReconfigureAndApply(128, start.Add(time.Second), 128, 128))
	require.False(t, bucket.Apply(1, start.Add(time.Second)))
}

func TestHighBandwidthRefillDoesNotOverflow(t *testing.T) {
	type testCase struct {
		codepoint uint16
		idleTime  time.Duration
	}
	var testCases []testCase
	for codepoint := uint16(1000); codepoint <= 1023; codepoint++ {
		for _, idleTime := range []time.Duration{2 * time.Second, 10 * time.Second, time.Minute} {
			testCases = append(testCases, testCase{
				codepoint: codepoint,
				idleTime:  idleTime,
			})
		}
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("codepoint=%d/idle_time=%s", tc.codepoint, tc.idleTime),
			func(t *testing.T) {
				start := time.Unix(0, 0)
				rate := tokenbucket.ConvertBW(tc.codepoint)
				bucket := tokenbucket.NewTokenBucket(start, rate, rate)
				bucket.CurrentTokens = 0

				// Multiplying the idle interval in nanoseconds by these rates exceeds
				// math.MaxInt64. The bucket should saturate before accepting the packet.
				require.True(t, bucket.Apply(96, start.Add(tc.idleTime)))
				require.Equal(t, rate-96, bucket.CurrentTokens)
			})
	}
}

// TestConvertBW checks that a bandwidth codepoint becomes the rate of the token
// bucket, which counts bytes per second rather than kbps.
func TestConvertBW(t *testing.T) {
	const bytesPerSecondPerKbps = 125 // 1Kbps = 1000 bits per second = 125 bytes per second.

	for _, codepoint := range []uint16{0, 1, 59, 60, 89, 512, bwencoding.Codepoints - 1} {
		t.Run(fmt.Sprintf("codepoint=%d", codepoint), func(t *testing.T) {
			assert.Equal(t,
				int64(bwencoding.DecodeEncodedBandwidth(codepoint))*bytesPerSecondPerKbps,
				tokenbucket.ConvertBW(codepoint))
		})
	}

	// The smallest and the largest reservation, in bytes per second.
	assert.Equal(t, int64(bwencoding.MinBwKbps*bytesPerSecondPerKbps), tokenbucket.ConvertBW(0))
	assert.Equal(t, int64(bwencoding.MaxBwKbps)*bytesPerSecondPerKbps,
		tokenbucket.ConvertBW(bwencoding.Codepoints-1))
}

func TestPrintAllCodePoints(t *testing.T) {
	for codepoint := 0; codepoint < bwencoding.Codepoints; codepoint++ {
		t.Logf("%d\t\t%d", codepoint, tokenbucket.ConvertBW(uint16(codepoint)))
	}
}
