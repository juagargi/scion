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

package segmenttest

import (
	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/xtest"
)

func NewPathFromComponents(chain ...interface{}) segment.ReservationTransparentPath {
	if len(chain)%3 != 0 {
		panic("wrong number of arguments")
	}
	p := segment.ReservationTransparentPath{}
	for i := 0; i < len(chain); i += 3 {
		p = append(p, segment.PathStepWithIA{
			PathStep: segment.PathStep{
				Ingress: uint16(chain[i].(int)),
				Egress:  uint16(chain[i+2].(int)),
			},
			IA: xtest.MustParseIA(chain[i+1].(string)),
		})
	}
	return p
}

func NewOpaquePathFromComponents(ids ...uint16) segment.OpaquePath {
	if len(ids)%2 != 0 {
		panic("wrong number of arguments")
	}
	p := make(segment.OpaquePath, len(ids)/2)
	for i := 0; i < len(ids); i += 2 {
		p[i/2].Ingress = ids[i]
		p[i/2].Egress = ids[i+1]
	}
	return p
}

// NewIfaces is invoked like:
// NewIfaces("1-ff00:0:1",1,   2, "1-ff00:1:2", 3,   4, "1-ff00:0:3") .
func NewIfaces(args ...interface{}) []snet.PathInterface {
	if len(args) == 0 {
		return []snet.PathInterface{}
	}
	if (len(args)+2)%3 != 0 {
		panic("wrong number of arguments")
	}
	list := make([]snet.PathInterface, (len(args)+2)/3*2-2)
	list[0].IA = xtest.MustParseIA(args[0].(string))
	list[0].ID = common.IFIDType(args[1].(int))
	for i := 2; i < len(args)-2; i += 3 {
		ingress := args[i].(int)
		ia := xtest.MustParseIA(args[i+1].(string))
		egress := args[i+2].(int)
		// two hops: first ingress, then egress
		list[(i-2)/3+1].IA = ia
		list[(i-2)/3+1].ID = common.IFIDType(ingress)
		list[(i-2)/3+2].IA = ia
		list[(i-2)/3+2].ID = common.IFIDType(egress)
	}
	list[len(list)-1].ID = common.IFIDType(args[len(args)-2].(int))
	list[len(list)-1].IA = xtest.MustParseIA(args[len(args)-1].(string))
	return list
}

func NewReservation() *segment.Reservation {
	segID, err := reservation.NewSegmentID(xtest.MustParseAS("ff00:0:1"),
		xtest.MustParseHexString("beefcafe"))
	if err != nil {
		panic(err)
	}
	r := segment.NewReservation()
	r.ID = *segID
	r.Path = NewPathFromComponents(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0)
	return r
}
