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

	"github.com/scionproto/scion/go/lib/xtest"
	"github.com/stretchr/testify/require"
)

func TestDecodeEncode(t *testing.T) {
	fmt.Println("ok")
}

func BenchmarkDecodeFull(b *testing.B) {
	cp := newColibriPath()
	buff := make([]byte, cp.Len())
	err := cp.SerializeTo(buff)
	require.NoError(b, err)
	var p ColibriPath
	for i := 0; i < b.N; i++ {
		err = p.DecodeFromBytes(buff)
		require.NoError(b, err)
	}
}

func BenchmarkDecodeMinimal(b *testing.B) {
	cp := newColibriPath()
	buff := make([]byte, cp.Len())
	err := cp.SerializeTo(buff)
	require.NoError(b, err)
	var p ColibriPathMinimal
	for i := 0; i < b.N; i++ {
		err = p.DecodeFromBytes(buff)
		require.NoError(b, err)
	}
}

func newColibriPath() *ColibriPath {
	cp := &ColibriPath{
		PacketTimestamp: *(*Timestamp)(xtest.MustParseHexString("0123456789abcdef")),
		InfoField: &InfoField{
			HFCount:     6,
			ResIdSuffix: make([]byte, 12),
		},
	}
	cp.HopFields = make([]*HopField, cp.InfoField.HFCount)
	for i := range cp.HopFields {
		cp.HopFields[i] = &HopField{
			Mac: make([]byte, 4),
		}
	}
	return cp
}
