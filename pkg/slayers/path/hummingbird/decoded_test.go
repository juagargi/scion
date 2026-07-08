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

package hummingbird_test

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/scrypto"
	"github.com/scionproto/scion/pkg/slayers/path"
	"github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/pkg/slayers/path/scion"
)

func TestDecodedSerializeHbird(t *testing.T) {
	for i := range decodedPaths {
		b := make([]byte, decodedPaths[i].Len())
		assert.NoError(t, decodedPaths[i].SerializeTo(b))
		assert.Equal(t, decodedBytes[i], b)
	}
}

func TestDecodeFromBytesHbird(t *testing.T) {
	s := &hummingbird.Decoded{}
	for i := range decodedPaths {
		assert.NoError(t, s.DecodeFromBytes(decodedBytes[i]))
		assert.Equal(t, decodedPaths[i], s)
	}
}

func TestSerializeAndBack(t *testing.T) {
	for i := range decodedPaths {
		buff := make([]byte, decodedPaths[i].Len())
		assert.NoError(t, decodedPaths[i].SerializeTo(buff))
		s := &hummingbird.Decoded{}
		assert.NoError(t, s.DecodeFromBytes(buff))
		assert.Equal(t, decodedPaths[i], s)
	}
}

func TestDecodedDecodeFromBytesNoFlyovers(t *testing.T) {
	const hfExpTime = 8
	const upTimestamp = 0x01020304
	const downTimestamp = 0x05060708

	macKey := []byte("testkey_xxxxxxxx")
	// p is the scion decoded path we would observe using the Tiny topology of the
	// topology generator, when going from 111 to 112. This is one up segment with 2 hops, followed
	// by a down segment with two hops as well. There is a cross over at core 110 gluing both.
	p := &scion.Decoded{
		Base: scion.Base{
			PathMeta: scion.MetaHdr{
				CurrINF: 1,
				CurrHF:  2,
				SegLen:  [3]uint8{2, 2, 0},
			},
			NumINF:  2,
			NumHops: 4,
		},
		InfoFields: []path.InfoField{
			{
				ConsDir:   false, // up
				SegID:     0x111,
				Timestamp: upTimestamp,
			},
			{
				ConsDir:   true, // down
				SegID:     0x222,
				Timestamp: downTimestamp,
			},
		},
		HopFields: []path.HopField{
			{
				ConsIngress: 41, // 111: 0->41 up
				ConsEgress:  0,
				ExpTime:     hfExpTime,
			},
			{
				ConsIngress: 0, // 110: 1->0 up
				ConsEgress:  1,
				ExpTime:     hfExpTime,
			},
			{
				ConsIngress: 0, // 110: 0->2 down
				ConsEgress:  2,
				ExpTime:     hfExpTime,
			},
			{
				ConsIngress: 1, // 112: 1->0 down
				ConsEgress:  0,
				ExpTime:     hfExpTime,
			},
		},
	}
	p.HopFields[0].Mac = computeHopMAC(t, macKey, p.InfoFields[0], p.HopFields[0])
	p.HopFields[1].Mac = computeHopMAC(t, macKey, p.InfoFields[0], p.HopFields[1])
	p.HopFields[2].Mac = computeHopMAC(t, macKey, p.InfoFields[1], p.HopFields[2])
	p.HopFields[3].Mac = computeHopMAC(t, macKey, p.InfoFields[1], p.HopFields[3])

	// Create a hummingbird path from the scion one.
	hbird := &hummingbird.Decoded{}
	hbird.ConvertFromScionDecoded(p) // SegLen will be [6,6,0] after this

	expected := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrINF: 1,
				CurrHF:  6,
				SegLen:  [3]uint8{6, 6, 0},
			},
			NumINF:   2,
			NumLines: 12,
		},
		InfoFields: p.InfoFields,
		HopFields: []hummingbird.FlyoverHopField{
			{HopField: p.HopFields[0]},
			{HopField: p.HopFields[1]},
			{HopField: p.HopFields[2]},
			{HopField: p.HopFields[3]},
		},
		FirstHopPerSeg: [2]uint8{2, 4},
	}
	assert.Equal(t, expected, hbird)

	// Check the hummingbird path is correct by serializing and deserializing it.
	buf := make([]byte, hbird.Len())
	err := hbird.SerializeTo(buf)
	require.NoError(t, err)
	// Deserialize.
	hbird = &hummingbird.Decoded{}
	err = hbird.DecodeFromBytes(buf)
	require.NoError(t, err)
	assert.Equal(t, expected, hbird)
}

func computeHopMAC(t *testing.T, key []byte, info path.InfoField,
	hf path.HopField) [path.MacLen]byte {

	t.Helper()
	mac, err := scrypto.InitMac(key)
	require.NoError(t, err)
	return path.MAC(mac, info, hf, nil)
}

func TestDecodedReverseHbird(t *testing.T) {
	for name, tc := range pathReverseTestCases {
		name, tc := name, tc
		for i := range tc.inIdxs {
			i := i
			t.Run(fmt.Sprintf("%s case %d", name, i+1), func(t *testing.T) {
				t.Parallel()
				inputPath := mkDecodedHbirdPath(t, tc.input, uint8(tc.inIdxs[i][0]),
					uint8(tc.inIdxs[i][1]))
				wantPath := mkDecodedHbirdPath(t, tc.want, uint8(tc.wantIdxs[i][0]),
					uint8(tc.wantIdxs[i][1]))
				revPath, err := inputPath.Reverse()
				assert.NoError(t, err)
				assert.Equal(t, wantPath, revPath)
			})
		}
	}
}

func TestEmptyDecodedReverse(t *testing.T) {
	emptyDecodedTestPath := &hummingbird.Decoded{
		Base:       hummingbird.Base{},
		InfoFields: []path.InfoField{},
		HopFields:  []hummingbird.FlyoverHopField{},
	}
	_, err := emptyDecodedTestPath.Reverse()
	assert.Error(t, err)
}

func TestDecodedToRaw(t *testing.T) {
	raw, err := decodedPaths[0].ToRaw()
	assert.NoError(t, err)
	assert.Equal(t, rawHbirdTestPath, raw)
}

func TestInfIndexForHFIndex(t *testing.T) {
	cases := map[string]struct {
		path     hummingbird.Decoded
		expected []uint8 // the INF indices of each hop field in the test case
	}{
		"empty": {
			path: hummingbird.Decoded{
				Base: hummingbird.Base{
					PathMeta: hummingbird.MetaHdr{
						SegLen: [3]uint8{0, 0, 0},
					},
				},
			},
		},
		"one_segment_o": {
			path: hummingbird.Decoded{
				Base: hummingbird.Base{
					PathMeta: hummingbird.MetaHdr{
						SegLen: [3]uint8{3, 0, 0},
					},
				},
				HopFields: []hummingbird.FlyoverHopField{
					{Flyover: false},
				},
			},
			expected: []uint8{0},
		},
		// one_segment_oxx means there is one segment with three hops, first is not flyover,
		// second and third are.
		"one_segment_oxx": {
			path: hummingbird.Decoded{
				Base: hummingbird.Base{
					PathMeta: hummingbird.MetaHdr{
						SegLen: [3]uint8{13, 0, 0},
					},
				},
				HopFields: []hummingbird.FlyoverHopField{
					{Flyover: false},
					{Flyover: true},
					{Flyover: true},
				},
			},
			expected: []uint8{0, 0, 0},
		},
		"two_segments_o_oxx": {
			path: hummingbird.Decoded{
				Base: hummingbird.Base{
					PathMeta: hummingbird.MetaHdr{
						SegLen: [3]uint8{3, 13, 0},
					},
				},
				HopFields: []hummingbird.FlyoverHopField{
					{Flyover: false},
					{Flyover: false},
					{Flyover: true},
					{Flyover: true},
				},
			},
			expected: []uint8{0, 1, 1, 1},
		},
	}
	for name, tc := range cases {
		name, tc := name, tc
		t.Run(name, func(t *testing.T) {
			for i := range tc.path.HopFields {
				got := tc.path.InfIndexForHFIndex(uint8(i))
				assert.Equal(t, tc.expected[i], got)
			}
			assert.Panics(t, func() {
				tc.path.InfIndexForHFIndex(uint8(len(tc.path.HopFields)) + 1)
			})
		})
	}
}

func TestDecodedGetCurrentHopField(t *testing.T) {
	path := *decodedPaths[0]
	path.PathMeta.CurrHF = 5
	got, err := path.GetCurrentHopField()
	assert.NoError(t, err)
	assert.Equal(t, path.HopFields[1], got)

	path.PathMeta.CurrHF = 4
	_, err = path.GetCurrentHopField()
	assert.Error(t, err)
}

func TestIsCrossOver(t *testing.T) {
	dec := mkTiny2Segments(t)
	assert.Equal(t, 0, dec.IsCrossOver(0))
	assert.Equal(t, -1, dec.IsCrossOver(1))
	assert.Equal(t, +1, dec.IsCrossOver(2))
	assert.Equal(t, 0, dec.IsCrossOver(3))

	dec = mkTiny1Segment(t)
	assert.Equal(t, 0, dec.IsCrossOver(0))
	assert.Equal(t, 0, dec.IsCrossOver(1))
}

func mkTiny2Segments(t *testing.T) *hummingbird.Decoded {
	return mkDecodedHbirdPath(
		t,
		hbirdPathCase{
			infos: []bool{false, true},
			hops: [][]hbirdHopCase{
				{
					hbirdHopCase{
						ingress: 0,
						egress:  1,
						flyover: false,
					},
					hbirdHopCase{
						ingress: 41,
						egress:  0,
						flyover: false,
					},
				},
				{
					hbirdHopCase{
						ingress: 0,
						egress:  2,
						flyover: false,
					},
					hbirdHopCase{
						ingress: 1,
						egress:  0,
						flyover: false,
					},
				},
			},
		},
		0,
		0,
	)
}

func mkTiny1Segment(t *testing.T) *hummingbird.Decoded {
	return mkDecodedHbirdPath(
		t,
		hbirdPathCase{
			infos: []bool{false},
			hops: [][]hbirdHopCase{
				{
					hbirdHopCase{
						ingress: 0,
						egress:  1,
						flyover: false,
					},
					hbirdHopCase{
						ingress: 41,
						egress:  0,
						flyover: false,
					},
				},
			},
		},
		0,
		0,
	)
}

func mkDecodedHbirdPath(
	t *testing.T,
	pcase hbirdPathCase,
	infIdx uint8,
	hopIdx uint8,
) *hummingbird.Decoded {
	t.Helper()
	s := &hummingbird.Decoded{
		Base: hummingbird.Base{
			PathMeta: hummingbird.MetaHdr{
				CurrINF:   infIdx,
				CurrHF:    hopIdx,
				BaseTS:    14,
				HighResTS: 15,
			},
		},
	}
	for _, dir := range pcase.infos {
		s.InfoFields = append(s.InfoFields, path.InfoField{ConsDir: dir})
	}
	i := 0
	for j, hops := range pcase.hops {
		for _, hop := range hops {
			s.HopFields = append(s.HopFields,
				hummingbird.FlyoverHopField{
					HopField: path.HopField{
						ConsIngress: hop.ingress,
						ConsEgress:  hop.egress,
						Mac:         [6]byte{1, 2, 3, 4, 5, 6}},
					Flyover:  hop.flyover,
					Duration: 2,
				})
			if hop.flyover {
				i += 5
				s.PathMeta.SegLen[j] += 5
			} else {
				i += 3
				s.PathMeta.SegLen[j] += 3
			}
		}
	}
	s.NumINF = len(pcase.infos)
	s.NumLines = i

	// Compute the first hop per segment.
	s.FirstHopPerSeg[0] = uint8(len(s.HopFields))
	s.FirstHopPerSeg[1] = uint8(len(s.HopFields))
	switch s.NumINF {
	case 2: // Only two segments, fix the second segment start index.
		s.FirstHopPerSeg[0] = uint8(len(pcase.hops[0]))
	case 3: // Three segments, fix both the second and third segment starting index.
		s.FirstHopPerSeg[0] = uint8(len(pcase.hops[0]))
		s.FirstHopPerSeg[1] = uint8(len(pcase.hops[1])) + s.FirstHopPerSeg[0]
	}

	return s
}
