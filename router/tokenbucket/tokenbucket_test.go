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

	"github.com/scionproto/scion/router/tokenbucket"
)

type entry struct {
	length      int
	arrivalTime time.Time
	result      bool
}
type test struct {
	name    string
	entries []entry
	bucket  *tokenbucket.TokenBucket
}

// TestTokenBucketAlgorithm checks that the token bucket implementation detects if a given
// amount of bytes exceed the allowance or not.
func TestTokenBucketAlgorithm(t *testing.T) {

	var startTime = time.Unix(0, 0)

	tests := []test{
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

// TestConvertBW checks that ConvertBW(BW uint16) works as expected.
// The 10 bits of BW are divided into 5 for mantissa, and 5 for exponent.
// Always positive and integer.
// The expected value comes from this definition:
//
// result = mantissa							iff exponent = 0
// result = (mantissa+32) * 2^(exponent - 1)	iff exponent > 0
func TestConvertBW(t *testing.T) {
	testCases := map[string]struct {
		bw       uint16
		expected int64
	}{}

	addCase := func(name string, mantissa uint16, exponent uint16, expected int64) {
		bw := (exponent << 5) | mantissa
		testCases[name] = struct {
			bw       uint16
			expected int64
		}{
			bw:       bw,
			expected: expected,
		}
	}

	// All cases for exponent=0:
	// result = mantissa
	for mantissa := uint16(0); mantissa < 32; mantissa++ {
		addCase(fmt.Sprintf("m%d_e0", mantissa), mantissa, 0, int64(mantissa))
	}

	// All cases for exponent=1:
	// result = (mantissa+32) * 2^(1-1) = (mantissa+32) * 1
	for mantissa := uint16(0); mantissa < 32; mantissa++ {
		addCase(fmt.Sprintf("m%d_e1", mantissa), mantissa, 1, int64(mantissa+32))
	}

	// Edge mantissas across all exponents:
	// mantissa = 0   -> result = (0+32)  * 2^(e-1), for e>0
	// mantissa = 31  -> result = (31+32) * 2^(e-1), for e>0
	for exponent := uint16(0); exponent < 32; exponent++ {
		if exponent == 0 {
			addCase("m0_e0_edge", 0, 0, 0)
			addCase("m31_e0_edge", 31, 0, 31)
			continue
		}
		addCase(fmt.Sprintf("m0_e%d", exponent), 0, exponent, int64(32)<<(exponent-1))
		addCase(fmt.Sprintf("m31_e%d", exponent), 31, exponent, int64(63)<<(exponent-1))
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tc.expected, tokenbucket.ConvertBW(tc.bw))
		})
	}
}

// TestRealBwToEncodedRoundTrip verifies real->encoded->real behavior under ceil quantization.
func TestRealBwToEncodedRoundTrip(t *testing.T) {
	// Include boundaries and interior values that are often non-representable.
	realBws := []int64{
		0, 1, 2, 30, 31, 32, 33, 62, 63, 64, 65, 66,
		127, 128, 129, 1023, 1024, 1025,
		tokenbucket.ConvertBW(1023) - 1,
		tokenbucket.ConvertBW(1023),
	}

	for _, realBw := range realBws {
		t.Run(fmt.Sprintf("%d", realBw), func(t *testing.T) {
			encoded, err := tokenbucket.RealBwToEncoded(realBw)
			require.NoError(t, err)

			decoded := tokenbucket.ConvertBW(encoded)
			// Ceil guarantee: never under-encode requested bandwidth.
			assert.GreaterOrEqual(t, decoded, realBw, "real=%d encoded=%d decoded=%d",
				realBw, encoded, decoded)

			if encoded > 0 {
				// Minimality guarantee: previous codepoint is strictly below requested bandwidth.
				prevDecoded := tokenbucket.ConvertBW(encoded - 1)
				assert.Less(t, prevDecoded, realBw, "real=%d encoded=%d prevDecoded=%d",
					realBw, encoded, prevDecoded)
			}
		})
	}
}

// TestEncodedToRealToEncodedIdempotent verifies that already-representable
// values stay stable through real->encoded conversion.
func TestEncodedToRealToEncodedIdempotent(t *testing.T) {
	for encoded := uint16(0); encoded < 1024; encoded++ {
		t.Run(fmt.Sprintf("%d", encoded), func(t *testing.T) {
			realBw := tokenbucket.ConvertBW(encoded)
			encoded2, err := tokenbucket.RealBwToEncoded(realBw)
			require.NoError(t, err)
			assert.Equal(t, encoded, encoded2, "encoded=%d real=%d", encoded, realBw)
		})
	}
}

func TestRealBwToEncodedErrors(t *testing.T) {
	t.Run("-1", func(t *testing.T) {
		_, err := tokenbucket.RealBwToEncoded(-1)
		require.Error(t, err)
	})

	t.Run("1023", func(t *testing.T) {
		maxRealBw := tokenbucket.ConvertBW(1023)
		_, err := tokenbucket.RealBwToEncoded(maxRealBw + 1)
		require.Error(t, err)
	})
}
