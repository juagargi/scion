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
	Ingress uint16
	Egress  uint16
	Mac     [4]byte
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

	*(*[12]byte)(unsafe.Pointer(&c.Suffix)) = *((*[12]byte)(b[12:24]))

	*(*uint32)(unsafe.Pointer(&c.ExpTick)) = binary.BigEndian.Uint32(b[24:28])
	*(*uint8)(unsafe.Pointer(&c.BwCls)) = b[28]
	*(*uint8)(unsafe.Pointer(&c.Rlc)) = b[29]
	*(*uint16)(unsafe.Pointer(&c.OrigPayloadLen)) = binary.BigEndian.Uint16(b[24:28])

	offset := LenFastInfoField
	c.HopFields = make([]FastHF, c.HFCount)
	for i := 0; i < int(c.HFCount); i++ {
		*(*uint16)(unsafe.Pointer(&c.HopFields[i].Ingress)) = binary.BigEndian.Uint16(b[offset:])
		*(*uint16)(unsafe.Pointer(&c.HopFields[i].Egress)) = binary.BigEndian.Uint16(b[offset+2:])
		c.HopFields[i].Mac = *((*[4]byte)(b[offset+4 : offset+8]))
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
