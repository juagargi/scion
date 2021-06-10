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
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/slayers/path"
	"github.com/scionproto/scion/go/lib/slayers/path/colibri"
	"github.com/scionproto/scion/go/lib/slayers/path/scion"
	"github.com/scionproto/scion/go/lib/spath"
)

// Capacities describes what a capacity description must offer.
type Capacities interface {
	IngressInterfaces() []uint16
	EgressInterfaces() []uint16
	CapacityIngress(ingress uint16) uint64
	CapacityEgress(egress uint16) uint64
}

// PacketPath is the path the request or response have.
// Usually will be a colibri path, but for a segment setup it could be a regular scion path.
// The two concrete types supperted at the moment are colibriPath and scionPath
type PacketPath interface {
	Copy() PacketPath
	Reverse() error
	NumberOfHops() int
	IndexOfCurrentHop() int
	IngressEgressIFIDs() (uint16, uint16, error)
	Path() path.Path
}

func NewPacketPath(spath spath.Path) (PacketPath, error) {
	switch spath.Type {
	case scion.PathType:
		p := scion.Decoded{}
		if err := p.DecodeFromBytes(spath.Raw); err != nil {
			return nil, err
		}
		return NewScionPath(p), nil
	case colibri.PathType:
		p := colibri.ColibriPath{}
		if err := p.DecodeFromBytes(spath.Raw); err != nil {
			return nil, err
		}
		return NewColibriPath(p), nil
	default:
		return nil, serrors.New("unsupported path type", "type", spath.Type)
	}
}

type colibriPath struct {
	colibri.ColibriPath
}

func NewColibriPath(p colibri.ColibriPath) *colibriPath {
	return &colibriPath{p}
}

type scionPath struct {
	// scion.Raw  // TODO(juagargi) turn to Raw after debugging
	Raw scion.Decoded
}

func NewScionPath(p scion.Decoded) *scionPath {
	return &scionPath{p}
}

func (p *colibriPath) Copy() PacketPath {
	c := *p
	return &c
}

func (p *colibriPath) Reverse() error {
	_, err := p.ColibriPath.Reverse() // Reverse works directly on the receiver object
	return err
}

func (p *colibriPath) NumberOfHops() int {
	return int(p.ColibriPath.InfoField.HFCount)
}
func (p *colibriPath) IndexOfCurrentHop() int {
	return int(p.ColibriPath.InfoField.CurrHF)
}
func (p *colibriPath) IngressEgressIFIDs() (uint16, uint16, error) {
	hf := p.ColibriPath.HopFields[p.ColibriPath.InfoField.CurrHF]
	return hf.IngressId, hf.EgressId, nil
}

func (p *colibriPath) Path() path.Path {
	return &p.ColibriPath
}

func (p *scionPath) Copy() PacketPath {
	c := *p
	return &c
}

func (p *scionPath) Reverse() error {
	_, err := p.Raw.Reverse() // Reverse works directly on the receiver object
	return err
}

func (p *scionPath) NumberOfHops() int {
	return p.Raw.NumHops
}

func (p *scionPath) IndexOfCurrentHop() int {
	return int(p.Raw.PathMeta.CurrHF)
}

func (p *scionPath) IngressEgressIFIDs() (uint16, uint16, error) {
	hf := p.Raw.HopFields[int(p.Raw.PathMeta.CurrHF)]
	inf := p.Raw.InfoFields[(p.Raw.PathMeta.CurrINF)]
	if inf.ConsDir {
		return hf.ConsIngress, hf.ConsEgress, nil
	}
	return hf.ConsEgress, hf.ConsIngress, nil
}

func (p *scionPath) Path() path.Path {
	return &p.Raw
}
