// Copyright 2020 ETH Zurich
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
	"bytes"
	"encoding/binary"
	"fmt"
	"unsafe"

	"github.com/scionproto/scion/go/lib/slayers/path"
)

const LenFastInfoField = 8 + LenInfoField

type FastInfoField struct {
	Timestamp      Timestamp
	Flags          uint8
	Index          uint8
	CurrHF         uint8
	HFCount        uint8
	Suffix         [12]byte
	ExpTick        uint32
	BwCls          uint8
	Rlc            uint8
	OrigPayloadLen uint16
}

type FastHF struct {
	raw [8]byte
}

func (hf FastHF) Ingress() uint16 {
	return binary.BigEndian.Uint16(hf.raw[:2])
}

func (hf FastHF) Egress() uint16 {
	return binary.BigEndian.Uint16(hf.raw[2:4])
}

func (hf FastHF) Mac() [4]byte {
	return *((*[4]byte)(hf.raw[4:8]))
}

type ColibriFast struct {
	FastInfoField
	HopFields []FastHF
}

func (c *ColibriFast) DecodeFromBytes(b []byte) error {
	c.Timestamp = *((*[8]byte)(b[:8]))
	*(*uint8)(unsafe.Pointer(&c.Flags)) = b[8]
	*(*uint8)(unsafe.Pointer(&c.Index)) = b[9]
	*(*uint8)(unsafe.Pointer(&c.CurrHF)) = b[10]
	*(*uint8)(unsafe.Pointer(&c.HFCount)) = b[11]

	c.Suffix = *((*[12]byte)(b[12:24]))

	c.ExpTick = binary.BigEndian.Uint32(b[24:28])
	*(*uint8)(unsafe.Pointer(&c.BwCls)) = b[28]
	*(*uint8)(unsafe.Pointer(&c.Rlc)) = b[29]
	c.OrigPayloadLen = binary.BigEndian.Uint16(b[24:28])

	// TODO(juagargi) we can generate code to ensure that the structure
	// aligns correctly on each platform. Otherwise this below is unsafe:
	// *((*[32]byte)(unsafe.Pointer(c))) = *((*[32]byte)(b[:LenFastInfoField]))

	offset := LenFastInfoField
	c.HopFields = make([]FastHF, c.HFCount)
	// TODO(juagargi) we can generate code for the 1..64 possible hop fields
	// like the code below, and it should replace the regular loop on the
	// hop fields, which is faster (test with e.g. 23 hop fields, etc):
	// switch c.HFCount {
	// case 6:
	// 	*((*[6 * 8]byte)(unsafe.Pointer((*[6]FastHF)(c.HopFields[:6])))) =
	// 		*((*[6 * 8]byte)(b[offset : offset+6*8]))
	// 	return nil
	// case 23:
	// 	*((*[23 * 8]byte)(unsafe.Pointer((*[23]FastHF)(c.HopFields[:23])))) =
	// 		*((*[23 * 8]byte)(b[offset : offset+23*8]))
	// 	return nil
	// default:
	// }
	for i := 0; i < int(c.HFCount); i++ {
		*((*[8]byte)(c.HopFields[i].raw[:])) = *((*[8]byte)(b[offset : offset+8]))
		offset += 8
	}
	return nil
}

func (c *ColibriFast) DecodeFromBytes_old(b []byte) error {
	reader := bytes.NewReader(b[:LenFastInfoField])
	if err := binary.Read(reader, binary.BigEndian, &c.FastInfoField); err != nil {
		return fmt.Errorf("error parsing info field: %w", err)
	}
	// c.HopFields = make([]FastHF, c.HFCount)

	// reader = bytes.NewReader(b[LenFastInfoField:])
	// return binary.Read(reader, binary.BigEndian, c.HopFields)
	return nil
}

func (c *ColibriFast) SerializeTo(b []byte) error {
	return nil
}

func (c *ColibriFast) Reverse() (path.Path, error) {
	return nil, nil
}

func (c *ColibriFast) Len() int {
	if c == nil {
		return 0
	}
	return 1
}

func (c *ColibriFast) Type() path.Type {
	return PathType
}
