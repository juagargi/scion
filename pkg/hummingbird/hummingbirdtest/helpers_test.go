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

package hummingbirdtest

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/addr"
	dppath "github.com/scionproto/scion/pkg/slayers/path"
	dpscion "github.com/scionproto/scion/pkg/slayers/path/scion"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

func TestReverseBaseHops(t *testing.T) {
	srcIA := addr.MustParseIA("1-ff00:0:111")
	transitIA := addr.MustParseIA("1-ff00:0:112")
	dstIA := addr.MustParseIA("1-ff00:0:113")
	forward := []snetpath.BaseHop{
		{IA: srcIA, Ingress: 0, Egress: 11},
		{IA: transitIA, Ingress: 21, Egress: 22},
		{IA: dstIA, Ingress: 31, Egress: 0},
	}

	reversed := reverseBaseHops(forward)

	require.Equal(t, []snetpath.BaseHop{
		{IA: dstIA, Ingress: 0, Egress: 31},
		{IA: transitIA, Ingress: 22, Egress: 21},
		{IA: srcIA, Ingress: 11, Egress: 0},
	}, reversed)
}

func TestSerializeReversedReservationState(t *testing.T) {
	srcIA := addr.MustParseIA("1-ff00:0:111")
	dstIA := addr.MustParseIA("1-ff00:0:112")
	forward := []snetpath.BaseHop{
		{IA: srcIA, Ingress: 0, Egress: 11},
		{IA: dstIA, Ingress: 21, Egress: 0},
	}
	reversed := reverseBaseHops(forward)
	scionPath, err := snetpath.NewSCIONFromDecoded(dpscion.Decoded{
		Base: dpscion.Base{
			PathMeta: dpscion.MetaHdr{
				SegLen: [3]uint8{2, 0, 0},
			},
			NumINF:  1,
			NumHops: 2,
		},
		InfoFields: []dppath.InfoField{
			{ConsDir: true},
		},
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

	hops := []*snetpath.Hop{
		{
			BaseHop: reversed[0],
			Flyover: &snetpath.FlyoverData{
				ResID:     1,
				Ak:        [16]byte{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1},
				Bw:        7,
				StartTime: uint32(time.Unix(123, 0).Unix()),
				Duration:  11,
			},
		},
		{
			BaseHop: reversed[1],
			Flyover: &snetpath.FlyoverData{
				ResID:     2,
				Ak:        [16]byte{2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2, 2},
				Bw:        9,
				StartTime: uint32(time.Unix(123, 0).Unix()),
				Duration:  13,
			},
		},
	}

	reservation, err := snetpath.NewReservation(
		snetpath.WithDataplanePath(scionPath, srcIA, hops),
		snetpath.WithNow(func() time.Time { return time.Unix(123, 0) }),
	)
	require.NoError(t, err)

	buff := make([]byte, reservation.SerializedLen())
	require.NoError(t, reservation.Serialize(buff))

	var got snetpath.Reservation
	err = got.Deserialize(buff)
	require.NoError(t, err)

	roundTrip := make([]byte, got.SerializedLen())
	require.NoError(t, got.Serialize(roundTrip))
	require.Equal(t, buff, roundTrip)
}
