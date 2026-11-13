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

package bwencoding

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestEncodeBandwidthWithLogStart checks the default encoding: the first
// codepoints are one kbps apart, starting at the minimum bandwidth, and the
// remaining ones grow geometrically up to the maximum bandwidth.
func TestEncodeBandwidthWithLogStart(t *testing.T) {
	t.Run("linear part", func(t *testing.T) {
		for codepoint := uint16(0); codepoint < logEncodingStart; codepoint++ {
			assert.Equal(t, uint32(MinBwKbps+codepoint), DecodeEncodedBandwidth(codepoint),
				"codepoint %d", codepoint)
		}
	})

	t.Run("bounds", func(t *testing.T) {
		assert.Equal(t, uint32(MinBwKbps), DecodeEncodedBandwidth(0))
		assert.Equal(t, uint32(MaxBwKbps), DecodeEncodedBandwidth(Codepoints-1))
		// The geometric part continues where the linear one stopped.
		assert.Equal(t, uint32(MinBwKbps+logEncodingStart), DecodeEncodedBandwidth(logEncodingStart))
	})

	t.Run("strictly increasing", func(t *testing.T) {
		// Every codepoint stands for a different bandwidth, which is what the
		// start of the geometric part was chosen for.
		previous := uint32(0)
		for codepoint := uint16(0); codepoint < Codepoints; codepoint++ {
			current := DecodeEncodedBandwidth(codepoint)
			assert.Greater(t, current, previous, "codepoint %d", codepoint)
			previous = current
		}
	})

	t.Run("codepoints are 10 bits", func(t *testing.T) {
		// Should use 10 bits.
		assert.Equal(t, 1<<10, Codepoints)
		// Anything wider is truncated to the field the dataplane carries.
		assert.Equal(t, DecodeEncodedBandwidth(0), DecodeEncodedBandwidth(Codepoints))
		assert.Equal(t, DecodeEncodedBandwidth(1), DecodeEncodedBandwidth(Codepoints+1))
	})
}
