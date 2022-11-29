// Copyright 2022 ETH Zurich
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

package colibri

import (
	"fmt"
	"testing"
	"unsafe"

	"github.com/scionproto/scion/go/lib/xtest"
	"github.com/stretchr/testify/require"
)

func TestDecodeEncode(t *testing.T) {
	fmt.Println("ok")
	expected := newColibriPath()
	buff := make([]byte, expected.Len())
	err := expected.SerializeTo(buff)
	require.NoError(t, err)

	cp := &ColibriPath{}
	err = cp.DecodeFromBytes2(buff)
	require.NoError(t, err)
	require.Equal(t, expected.PacketTimestamp, cp.PacketTimestamp)
	require.Equal(t, expected.InfoField, cp.InfoField)

	//
	//
	require.Equal(t, uintptr(32), unsafe.Sizeof(FastInfoField{}))
	require.Equal(t, uintptr(8), unsafe.Sizeof(FastHF{}))
	//
	//
	fast := &ColibriFast{}
	err = fast.DecodeFromBytes(buff)
	require.NoError(t, err)
	require.Equal(t, expected.PacketTimestamp, fast.Timestamp)
	require.Equal(t, expected.InfoField.Ver, fast.Index)
	require.Equal(t, expected.InfoField.CurrHF, fast.CurrHF)
	require.Equal(t, expected.InfoField.HFCount, fast.HFCount)
	require.Equal(t, expected.InfoField.ResIdSuffix, fast.Suffix[:])
	require.Equal(t, expected.InfoField.ExpTick, fast.ExpTick)
	require.Equal(t, expected.InfoField.BwCls, fast.BwCls)
	require.Equal(t, expected.InfoField.Rlc, fast.Rlc)
	require.Equal(t, expected.InfoField.OrigPayLen, fast.OrigPayloadLen)
	for i := 0; i < int(expected.InfoField.HFCount); i++ {
		require.Equal(t, expected.HopFields[i].IngressId, fast.HopFields[i].Ingress())
		require.Equal(t, expected.HopFields[i].EgressId, fast.HopFields[i].Egress())
		mac := fast.HopFields[i].Mac()
		require.Equal(t, expected.HopFields[i].Mac, mac[:])
	}
}

func BenchmarkDecodeFast(b *testing.B) {
	cp := newColibriPath()
	buff := make([]byte, cp.Len())
	err := cp.SerializeTo(buff)
	require.NoError(b, err)

	b.ResetTimer()
	var p ColibriFast
	for i := 0; i < b.N; i++ {
		err = p.DecodeFromBytes(buff)
		// require.NoError(b, err)
	}
}

func BenchmarkDecodeFull(b *testing.B) {
	cp := newColibriPath()
	buff := make([]byte, cp.Len())
	err := cp.SerializeTo(buff)
	require.NoError(b, err)

	b.ResetTimer()
	var p ColibriPath
	for i := 0; i < b.N; i++ {
		err = p.DecodeFromBytes(buff)
		// require.NoError(b, err)
	}
}

func BenchmarkDecodeMinimal(b *testing.B) {
	cp := newColibriPath()
	buff := make([]byte, cp.Len())
	err := cp.SerializeTo(buff)
	require.NoError(b, err)

	b.ResetTimer()
	var p ColibriPathMinimal
	for i := 0; i < b.N; i++ {
		err = p.DecodeFromBytes(buff)
		// require.NoError(b, err)
	}
}

func newColibriPath() *ColibriPath {
	cp := &ColibriPath{
		PacketTimestamp: *(*Timestamp)(xtest.MustParseHexString("0123456789abcdef")),
		InfoField: &InfoField{
			HFCount:     6,
			ResIdSuffix: []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 0xa, 0xb},
		},
	}
	cp.HopFields = make([]*HopField, cp.InfoField.HFCount)
	for i := range cp.HopFields {
		cp.HopFields[i] = &HopField{
			IngressId: uint16(2 * i),
			EgressId:  uint16(2*i + 1),
			Mac:       []byte{byte(0 + i), byte(1 + i), byte(2 + i), byte(3 + i)},
		}
	}
	return cp
}

func newColibriFast() *ColibriFast {
	return &ColibriFast{}
}
