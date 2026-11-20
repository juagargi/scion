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
	"math"
	"sort"

	"github.com/scionproto/scion/pkg/slayers/path/hummingbird"
)

const (
	// Codepoints is the number of bandwidths the encoding expresses, i.e. the
	// number of values that the bandwidth field of a flyover can take.
	Codepoints = 1 << hummingbird.BwBits

	// MinBwKbps and MaxBwKbps are the bandwidths of the first and last codepoint.
	MinBwKbps = 10
	MaxBwKbps = 10_000_000

	// Up to this codepoint the encoding is linear, in steps of one kbps;
	// from there on it is geometric, so that the whole range up to MaxBwKbps is covered.
	// It is the smallest start that still keeps every codepoint distinct.
	logEncodingStart = 60
)

// bandwidths holds the bandwidth of every codepoint. It is a table rather than a
// computation because efficiency.
var bandwidths = buildBandwidths()
var encodingPoints = toOrderedSlice(bandwidths)

func buildBandwidths() [Codepoints]uint32 {
	var table [Codepoints]uint32
	step := math.Pow(MaxBwKbps/(MinBwKbps+float64(logEncodingStart)),
		1.0/float64(Codepoints-logEncodingStart-1))
	for codepoint := 0; codepoint < Codepoints; codepoint++ {
		var kbps float64
		if codepoint < logEncodingStart {
			kbps = MinBwKbps + float64(codepoint)
		} else {
			kbps = (MinBwKbps + float64(logEncodingStart)) *
				math.Pow(step, float64(codepoint-logEncodingStart))
		}
		table[codepoint] = uint32(math.Ceil(kbps))
	}
	return table
}

func toOrderedSlice(table [Codepoints]uint32) []uint32 {
	points := make([]uint32, Codepoints)
	for i := 0; i < Codepoints; i++ {
		points[i] = table[i]
	}
	return points
}

// decodeEncodedBandwidthWithLogStart returns the bandwidth of a codepoint in kbps..
//
// The codepoint is 10 bits wide, anything wider is truncated to it.
func decodeEncodedBandwidthWithLogStart(codepoint uint16) uint32 {
	return bandwidths[codepoint&(Codepoints-1)]
}

func encodeBandwidthWithLogStart(bw uint32) (uint32, uint16) {
	if len(encodingPoints) == 0 {
		panic("encoding points empty")
	}
	point := min(uint16(len(encodingPoints)-1), uint16(sort.Search(len(encodingPoints), func(i int) bool {
		return encodingPoints[i] >= bw
	})))
	return encodingPoints[point], point
}
