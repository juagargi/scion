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
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/slayers/path"
	"github.com/scionproto/scion/pkg/slayers/path/scion"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// scionPathOf returns a path of n ASes with a single-segment SCION dataplane path.
func scionPathOf(t *testing.T, n int) snet.Path {
	t.Helper()
	notes := make([]string, n)
	p := pathWithNotes(notes...).(snetpath.Path)
	dec := scion.Decoded{
		Base: scion.Base{
			PathMeta: scion.MetaHdr{SegLen: [3]uint8{uint8(n), 0, 0}},
			NumINF:   1,
			NumHops:  n,
		},
		InfoFields: []path.InfoField{{ConsDir: true, Timestamp: 1}},
		HopFields:  make([]path.HopField, n),
	}
	for i := range dec.HopFields {
		dec.HopFields[i] = path.HopField{ExpTime: 63, ConsIngress: uint16(i), ConsEgress: uint16(i + 1)}
	}
	raw := make([]byte, dec.Len())
	require.NoError(t, dec.SerializeTo(raw))
	p.DataplanePath = snetpath.SCION{Raw: raw}
	return p
}

// TestOneShotReservationEmptySelection checks that selecting no AS buys nothing, contacts no
// marketplace (nil jwt, querier and topology would fail otherwise) and yields a reservation of
// the path length without flyovers.
func TestOneShotReservationEmptySelection(t *testing.T) {
	p := scionPathOf(t, 3)
	res, err := OneShotReservation(context.Background(), p, "", nil, snet.Topology{}, false,
		ReservationRequest{
			BwInKbps:        100,
			ReverseBwInKbps: 100,
			Selected:        []int{},
		})
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Len(t, res.Hops, len(res.Dec.HopFields))
	for _, hop := range res.Hops {
		assert.Nil(t, hop)
	}
	extn, err := res.EndToEndExtn()
	require.NoError(t, err)
	assert.Nil(t, extn)
}
