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

package reservation

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/serrors"
	slayerspath "github.com/scionproto/scion/go/lib/slayers/path"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/spath"
)

// TransparentPath is used in e.g. setup requests, where the IAs should not be visible.
type TransparentPath struct {
	CurrentStep int
	Steps       []PathStep // could contain IAs
	Spath       spath.Path // from slayers
}

func TransparentPathFromSnet(path snet.Path) (*TransparentPath, error) {
	if path == nil {
		return nil, nil
	}
	transp, err := TransparentPathFromInterfaces(path.Metadata().Interfaces)
	if err != nil {
		return transp, err
	}
	transp.Spath = spath.Path{
		Type: path.Path().Type,
		Raw:  append([]byte{}, path.Path().Raw...),
	}
	return transp, nil
}

// TransparentPathFromInterfaces constructs an TransparentPath given a list of snet.PathInterface .
// from a scion path e.g. 1-1#1, 1-2#33, 1-2#44, i-3#2
func TransparentPathFromInterfaces(ifaces []snet.PathInterface) (*TransparentPath, error) {
	if len(ifaces)%2 != 0 {
		return nil, serrors.New("wrong number of interfaces, not even", "ifaces", ifaces)
	}
	if len(ifaces) == 0 {
		return &TransparentPath{Steps: []PathStep{}}, nil
	}
	transp := &TransparentPath{
		Steps: make([]PathStep, len(ifaces)/2+1),
	}

	for i := 0; i < len(transp.Steps)-1; i++ {
		transp.Steps[i].Egress = uint16(ifaces[i*2].ID)
		transp.Steps[i].IA = ifaces[i*2].IA
		transp.Steps[i+1].Ingress = uint16(ifaces[i*2+1].ID)
	}
	transp.Steps[len(transp.Steps)-1].IA = ifaces[len(ifaces)-1].IA
	return transp, nil
}

func (p *TransparentPath) Interfaces() []snet.PathInterface {
	if p == nil {
		return []snet.PathInterface{}
	}
	ifaces := make([]snet.PathInterface, len(p.Steps)*2) // it has two too many
	for i := 0; i < len(p.Steps); i++ {
		ifaces[i*2].ID = common.IFIDType(p.Steps[i].Ingress)
		ifaces[i*2].IA = p.Steps[i].IA
		ifaces[i*2+1].ID = common.IFIDType(p.Steps[i].Egress)
		ifaces[i*2+1].IA = p.Steps[i].IA
	}
	return ifaces[1 : len(ifaces)-1]
}

func (p *TransparentPath) Copy() *TransparentPath {
	return &TransparentPath{
		Steps:       append(p.Steps[:0:0], p.Steps...),
		CurrentStep: p.CurrentStep,
		Spath:       p.Spath.Copy(),
	}
}

func (p *TransparentPath) String() string {
	if p == nil {
		return "<nil>"
	}
	strs := make([]string, len(p.Steps))
	for i, s := range p.Steps {
		if s.IA.IsZero() {
			strs[i] = fmt.Sprintf("%d,%d", s.Ingress, s.Egress)
		} else {
			strs[i] = fmt.Sprintf("%d,%s,%d", s.Ingress, s.IA, s.Egress)
		}
	}
	str := strings.Join(strs, " > ")
	if len(str) > 0 {
		str += " "
	}
	str += fmt.Sprintf("[curr.step = %d]", p.CurrentStep)
	return str
}

func (p *TransparentPath) ToRaw() []byte {
	if p == nil {
		return []byte{}
	}
	// currentStep + len(steps) + steps + spath_type + spath_raw
	length := 2 + 2 + len(p.Steps)*pathStepLen + 1 + len(p.Spath.Raw)
	buff := make([]byte, length)
	initialBuff := buff
	binary.BigEndian.PutUint16(buff, uint16(p.CurrentStep))
	buff = buff[2:]
	binary.BigEndian.PutUint16(buff, uint16(len(p.Steps)))
	buff = buff[2:]
	for _, step := range p.Steps {
		binary.BigEndian.PutUint16(buff, step.Ingress)
		binary.BigEndian.PutUint16(buff[2:], step.Egress)
		binary.BigEndian.PutUint64(buff[4:], uint64(step.IA.IAInt()))
		buff = buff[12:]
	}
	buff[0] = byte(p.Spath.Type)
	n := copy(buff[1:], p.Spath.Raw)
	if n != len(p.Spath.Raw) {
		panic("internal logic error")
	}
	return initialBuff
}

func TransparentPathFromRaw(raw []byte) (*TransparentPath, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	// currentStep + len(steps) + steps + spath_type + spath_raw
	if len(raw) < 5 {
		return nil, serrors.New("buffer too small")
	}
	currStep := int(binary.BigEndian.Uint16(raw))
	raw = raw[2:]
	stepCount := int(binary.BigEndian.Uint16(raw))
	raw = raw[2:]
	if len(raw) < stepCount*pathStepLen {
		return nil, serrors.New("buffer too small for these path", "step_count", stepCount,
			"len", len(raw))
	}
	steps := make([]PathStep, stepCount)
	for i := 0; i < stepCount; i++ {
		steps[i].Ingress = binary.BigEndian.Uint16(raw)
		steps[i].Egress = binary.BigEndian.Uint16(raw[2:])
		steps[i].IA = addr.IAInt(binary.BigEndian.Uint64(raw[4:])).IA()
		raw = raw[12:]
	}
	rawSpath := append([]byte{}, raw[1:]...)
	if len(rawSpath) == 0 {
		rawSpath = nil
	}
	return &TransparentPath{
		CurrentStep: currStep,
		Steps:       steps,
		Spath: spath.Path{
			Type: slayerspath.Type(raw[0]),
			Raw:  rawSpath,
		},
	}, nil
}

func (p *TransparentPath) SrcIA() addr.IA {
	if p == nil {
		return addr.IA{}
	}
	return p.Steps[0].IA
}

func (p *TransparentPath) DstIA() addr.IA {
	if p == nil || len(p.Steps) == 0 {
		return addr.IA{}
	}
	return p.Steps[len(p.Steps)-1].IA
}

func (p *TransparentPath) Validate() error {
	if p == nil {
		return nil
	}
	// sometimes we'll have requests with one step only (e.g. teardown after bad setup)
	if len(p.Steps) < 1 {
		return serrors.New("wrong number of steps", "count", len(p.Steps))
	}
	if p.CurrentStep >= len(p.Steps) {
		return serrors.New("current step out of bounds", "curr_step", p.CurrentStep,
			"count", len(p.Steps))
	}
	return nil
}

func (p *TransparentPath) Reverse() error {
	if p == nil {
		return nil
	}
	rev := make([]PathStep, len(p.Steps))
	for i, s := range p.Steps {
		s.Ingress, s.Egress = s.Egress, s.Ingress
		rev[len(rev)-i-1] = s
	}
	p.Steps = rev
	if p.CurrentStep < len(rev) { // if curr step is past the last item, leave it as is.
		p.CurrentStep = len(rev) - p.CurrentStep - 1
	}
	return p.Spath.Reverse()
}

// PathStep is one hop of the TransparentPath.
// For a source AS: Ingress will be invalid. Conversely for dst.
// So as opposed to snet.Path, these paths have length = number of ASes in the path.
type PathStep struct {
	Ingress uint16
	Egress  uint16
	IA      addr.IA
}

const pathStepLen = 2 + 2 + 8
