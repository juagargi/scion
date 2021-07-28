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

package conf

import (
	"encoding/json"
	"io/ioutil"
	"sort"

	base "github.com/scionproto/scion/go/co/reservation"
	"github.com/scionproto/scion/go/lib/serrors"
)

// internal structure used to serialize to and from json.
type capacities struct {
	// ingress capacities
	CapIn map[uint16]uint64 `json:"ingress_kbps"`
	// egress capacities
	CapEg map[uint16]uint64 `json:"egress_kbps"`
}

// Capacities aka capacity matrix.
type Capacities struct {
	c capacities
	// derived fields from the above ones:
	inIfs []uint16
	egIfs []uint16
}

var _ base.Capacities = (*Capacities)(nil)
var _ json.Unmarshaler = (*Capacities)(nil)
var _ json.Marshaler = (*Capacities)(nil)

func (c *Capacities) IngressInterfaces() []uint16           { return c.inIfs }
func (c *Capacities) EgressInterfaces() []uint16            { return c.egIfs }
func (c *Capacities) CapacityIngress(ingress uint16) uint64 { return c.c.CapIn[ingress] }
func (c *Capacities) CapacityEgress(egress uint16) uint64   { return c.c.CapEg[egress] }

func CapacitiesFromFile(filename string) (*Capacities, error) {
	b, err := ioutil.ReadFile(filename)
	if err != nil {
		return nil, serrors.WrapStr("error loading capacities", err, "filename", filename)
	}
	cap := &Capacities{}
	err = cap.UnmarshalJSON(b)
	if err != nil {
		return nil, serrors.WrapStr("error parsing capabilities", err, "filename", filename)
	}
	return cap, nil
}

// UnmarshalJSON deserializes into the json-aware internal data structure.
func (c *Capacities) UnmarshalJSON(b []byte) error {
	if err := json.Unmarshal(b, &c.c); err != nil {
		return err
	}
	return c.init()
}

// MarshalJSON serializes the internal json-friendly structure.
func (c Capacities) MarshalJSON() ([]byte, error) {
	return json.Marshal(c.c)
}

func (c *Capacities) init() error {
	// init list of ingress interfaces
	c.inIfs = make([]uint16, len(c.c.CapIn))
	i := 0
	for ifid := range c.c.CapIn {
		if _, ok := c.c.CapEg[ifid]; !ok {
			return serrors.New("missing egress interface", "ifid", ifid)
		}
		c.inIfs[i] = ifid
		i++
	}
	// init list of egress interfaces
	c.egIfs = make([]uint16, len(c.c.CapEg))
	i = 0
	for ifid := range c.c.CapEg {
		if _, ok := c.c.CapIn[ifid]; !ok {
			return serrors.New("missing ingress interface", "ifid", ifid)
		}
		c.egIfs[i] = ifid
		i++
	}
	// sort them just to simplify debugging
	sort.Slice(c.inIfs, func(i, j int) bool { return c.inIfs[i] < c.inIfs[j] })
	sort.Slice(c.egIfs, func(i, j int) bool { return c.egIfs[i] < c.egIfs[j] })

	return nil
}
