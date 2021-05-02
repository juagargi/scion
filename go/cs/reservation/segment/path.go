// Copyright 2020 ETH Zurich, Anapaya Systems
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

package segment

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
)

// OpaquePath is used in e.g. setup requests, where the IAs should not be visible.
// type OpaquePath struct {
// 	Steps []PathStep
// }

type OpaquePath []PathStep

// NewOpaquePathFromInterfaces constructs an OpaquePath given a list of snet.PathInterface .
// from a scion path e.g. 1-1#1, 1-2#33, 1-2#44, i-3#2
func NewOpaquePathFromInterfaces(ifaces []snet.PathInterface) (OpaquePath, error) {
	if len(ifaces)%2 != 0 {
		return nil, serrors.New("wrong number of interfaces, not even", "ifaces", ifaces)
	}
	if len(ifaces) == 0 {
		return OpaquePath{}, nil
	}
	opaque := make(OpaquePath, len(ifaces)/2+1)
	for i := 0; i < len(opaque)-1; i++ {
		opaque[i].Egress = uint16(ifaces[i*2].ID)
		opaque[i+1].Ingress = uint16(ifaces[i*2+1].ID)
	}
	return opaque, nil
}

func (p OpaquePath) String() string {
	strs := make([]string, len(p))
	for i, s := range p {
		strs[i] = fmt.Sprintf("%d,%d", s.Ingress, s.Egress)
	}
	return strings.Join(strs, " > ")
}

// TransparentPath represents a reservation path, in the reservation order.
// This path is seen only in the source of a segment reservation.
// TODO(juagargi) there exists a ColibriPath that could be used instead, if we only
// need equality. If we need to know the IDs of the transit ASes, it won't be possible.
type TransparentPath []PathStepWithIA

var _ snet.PathInterfacesHaver = (*TransparentPath)(nil)

var _ io.Reader = (*TransparentPath)(nil)

// NewPathFromRaw constructs a new Path from the byte representation.
func NewPathFromRaw(buff []byte) (TransparentPath, error) {
	if len(buff)%PathStepWithIALen != 0 {
		return nil, serrors.New("buffer input is not a multiple of a path step", "len", len(buff))
	}
	steps := len(buff) / PathStepWithIALen
	p := make(TransparentPath, steps)
	for i := 0; i < steps; i++ {
		offset := i * PathStepWithIALen
		p[i].Ingress = binary.BigEndian.Uint16(buff[offset:])
		p[i].Egress = binary.BigEndian.Uint16(buff[offset+2:])
		p[i].IA = addr.IAFromRaw(buff[offset+4:])
	}
	return p, nil
}

func NewTransparentPathFromInterfaces(ifaces []snet.PathInterface) (TransparentPath, error) {
	if len(ifaces)%2 != 0 {
		return nil, serrors.New("wrong number of interfaces, not even", "ifaces", ifaces)
	}
	if len(ifaces) == 0 {
		return TransparentPath{}, nil
	}
	transparent := make(TransparentPath, len(ifaces)/2+1)
	for i := 0; i < len(transparent)-1; i++ {
		transparent[i].Egress = uint16(ifaces[i*2].ID)
		transparent[i].IA = ifaces[i*2].IA
		transparent[i+1].Ingress = uint16(ifaces[i*2+1].ID)
	}
	return transparent, nil
}

// Validate returns an error if there is invalid data.
func (p TransparentPath) Validate() error {
	if len(p) < 2 {
		return serrors.New("invalid path length", "len", len(p))
	}
	if p[0].Ingress != 0 {
		return serrors.New("wrong ingress interface for source", "ingress", p[0].Ingress)
	}
	if p[len(p)-1].Egress != 0 {
		return serrors.New("wrong egress interface for destination",
			"egress ID", p[len(p)-1].Ingress)
	}
	return nil
}

// Equal returns true if both TransparentPath contain the same values.
func (p TransparentPath) Equal(o TransparentPath) bool {
	if len(p) != len(o) {
		return false
	}
	for i := 0; i < len(p); i++ {
		if p[i] != o[i] {
			return false
		}
	}
	return true
}

// Interfaces returns the interfaces in this transparent path.
// The expected convention for a list of interfaces always go egress and then ingress.
// So a transparent path like:
// 0 > 1-1 > 1  , 2 > 1-2 > 3 . 4 > 1-3 > 0
// becomes a list of snet.PathInterfaces like:
// 1-1#1 , 1-2#2 , 1-2#3 , 1-3#4
func (p TransparentPath) Interfaces() []snet.PathInterface {
	if len(p) < 2 {
		return []snet.PathInterface{}
	}
	ifaces := make([]snet.PathInterface, len(p)*2-2)
	for i := 0; i < len(ifaces); i += 2 {
		ifaces[i].IA = p[(i+1)/2].IA
		ifaces[i].ID = common.IFIDType(p[(i+1)/2].Egress)
		ifaces[i+1].IA = p[i/2+1].IA
		ifaces[i+1].ID = common.IFIDType(p[i/2+1].Ingress)
	}
	return ifaces
}

// GetSrcIA returns the source IA in the path or a zero IA if the path is nil (it's not the
// source AS of the reservation and has no access to the path of the reservation).
// If the Path is not nil, it assumes is valid, i.e. it has at least length 2.
func (p TransparentPath) GetSrcIA() addr.IA {
	if len(p) == 0 {
		return addr.IA{}
	}
	return p[0].IA
}

// GetDstIA returns the source IA in the path or a zero IA if the path is nil (it's not the
// source AS of the reservation and has no access to the path of the reservation).
// If the path is not nil, it assumes is valid, i.e. it has at least length 2.
func (p TransparentPath) GetDstIA() addr.IA {
	if len(p) == 0 {
		return addr.IA{}
	}
	return p[len(p)-1].IA
}

// Len returns the length of this path in bytes, when serialized.
func (p TransparentPath) Len() int {
	if len(p) == 0 {
		return 0
	}
	return len(p) * PathStepWithIALen
}

func (p TransparentPath) Read(buff []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if len(buff) < p.Len() {
		return 0, serrors.New("buffer too small", "min_size", p.Len(), "actual_size", len(buff))
	}
	for i, s := range p {
		offset := i * PathStepWithIALen
		binary.BigEndian.PutUint16(buff[offset:], s.Ingress)
		binary.BigEndian.PutUint16(buff[offset+2:], s.Egress)
		binary.BigEndian.PutUint64(buff[offset+4:], uint64(s.IA.IAInt()))
	}
	return p.Len(), nil
}

// ToRaw returns a buffer representing this TransparentPath.
func (p TransparentPath) ToRaw() []byte {
	if len(p) == 0 {
		return nil
	}
	buff := make([]byte, p.Len())
	p.Read(buff)
	return buff
}

func (p TransparentPath) String() string {
	strs := make([]string, len(p))
	for i, s := range p {
		strs[i] = s.String()
	}
	return strings.Join(strs, " > ")
}

func (p TransparentPath) Opaque() OpaquePath {
	opaque := make(OpaquePath, len(p))
	for i, step := range p {
		opaque[i] = step.PathStep
	}
	return opaque
}

// PathStep is one hop of the OpaquePath.
// For a source AS: Ingress will be invalid. Conversely for dst.
// So as opposed to snet.Path, these paths have length = number of ASes in the path.
type PathStep struct {
	Ingress uint16
	Egress  uint16
}

// PathStepWithIA is one step of the TransparentPath.
// These steps are specified at the source AS.
type PathStepWithIA struct {
	PathStep
	IA addr.IA
}

// PathStepWithIALen amounts for Ingress+Egress+IA.
const PathStepWithIALen = 2 + 2 + 8

func (s *PathStepWithIA) String() string {
	return fmt.Sprintf("%s#%d,%d", s.IA.String(), s.Ingress, s.Egress)
}
