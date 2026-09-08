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

package hummingbird

import (
	"testing"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/slayers"
	dppath "github.com/scionproto/scion/pkg/slayers/path"
	dpscion "github.com/scionproto/scion/pkg/slayers/path/scion"
	"github.com/scionproto/scion/pkg/snet/path"
	"github.com/stretchr/testify/require"
)

func TestBuildReverseReservationExtn(t *testing.T) {
	// Two ASes directly connected.
	srcIA := addr.MustParseIA("1-ff00:0:111")
	dstIA := addr.MustParseIA("1-ff00:0:112")
	forwardPath, err := path.NewSCIONFromDecoded(dpscion.Decoded{
		Base: dpscion.Base{
			PathMeta: dpscion.MetaHdr{
				SegLen: [3]uint8{2, 0, 0},
			},
			NumINF:  1,
			NumHops: 2,
		},
		InfoFields: []dppath.InfoField{{ConsDir: true}},
		HopFields: []dppath.HopField{
			{
				ConsIngress: 0,
				ConsEgress:  21,
				Mac:         [dppath.MacLen]byte{1, 2, 3, 4, 5, 6},
			},
			{
				ConsIngress: 11,
				ConsEgress:  0,
				Mac:         [dppath.MacLen]byte{6, 5, 4, 3, 2, 1},
			},
		},
	})
	require.NoError(t, err)
	originalPath := append([]byte(nil), forwardPath.Raw...)

	reverseHops := []*path.Hop{
		{
			BaseHop: path.BaseHop{IA: dstIA, Ingress: 0, Egress: 11},
			Flyover: &path.FlyoverData{ResID: 1, Bw: 7, StartTime: 123, Duration: 11},
		},
		{
			BaseHop: path.BaseHop{IA: srcIA, Ingress: 21, Egress: 0},
			Flyover: &path.FlyoverData{ResID: 2, Bw: 9, StartTime: 123, Duration: 13},
		},
	}

	extn, err := BuildReverseReservationExtn(forwardPath, srcIA, reverseHops)
	require.NoError(t, err)
	require.Len(t, extn.Options, 1)
	require.Equal(t, slayers.OptTypeReversePath, extn.Options[0].OptType)

	// Build the expected serialized state from the explicitly reversed SCION path.
	reversedPath, err := path.NewSCIONFromDecoded(dpscion.Decoded{
		Base: dpscion.Base{
			PathMeta: dpscion.MetaHdr{
				CurrHF: 1,
				SegLen: [3]uint8{2, 0, 0},
			},
			NumINF:  1,
			NumHops: 2,
		},
		InfoFields: []dppath.InfoField{{ConsDir: false}},
		HopFields: []dppath.HopField{
			{
				ConsIngress: 11,
				ConsEgress:  0,
				Mac:         [dppath.MacLen]byte{6, 5, 4, 3, 2, 1},
			},
			{
				ConsIngress: 0,
				ConsEgress:  21,
				Mac:         [dppath.MacLen]byte{1, 2, 3, 4, 5, 6},
			},
		},
	})
	require.NoError(t, err)
	expectedReservation, err := path.NewReservation(
		path.WithDataplanePath(reversedPath, srcIA, reverseHops),
	)
	require.NoError(t, err)
	expectedState := make([]byte, expectedReservation.SerializedLen())
	require.NoError(t, expectedReservation.Serialize(expectedState))
	require.Equal(t, expectedState, extn.Options[0].OptData)
	require.Equal(t, originalPath, forwardPath.Raw)

	_, err = BuildReverseReservationExtn(forwardPath, 0, reverseHops)
	require.Error(t, err)
}
