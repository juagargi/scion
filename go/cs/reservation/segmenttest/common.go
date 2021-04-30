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
	"fmt"
	"time"

	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/common"
	slayerspath "github.com/scionproto/scion/go/lib/slayers/path"
	"github.com/scionproto/scion/go/lib/slayers/path/scion"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/snet/path"
	"github.com/scionproto/scion/go/lib/spath"
	"github.com/scionproto/scion/go/lib/util"
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

// NewSnetPath is invoked like:
// NewSnetPath("1-ff00:0:1", 1,  2, "1-ff00:1:2", 3,     4, "1-ff00:0:3"))
func NewSnetPath(args ...interface{}) snet.Path {
	ifaces := NewIfaces(args...)
	opaque, err := segment.NewOpaquePathFromInterfaces(ifaces)
	if err != nil {
		panic(err)
	}

	rp := scion.Decoded{
		Base: scion.Base{
			PathMeta: scion.MetaHdr{
				CurrINF: 0,
				CurrHF:  0,
				SegLen:  [3]uint8{uint8(len(opaque))},
			},
			NumINF:  1,
			NumHops: len(opaque),
		},
		InfoFields: []*slayerspath.InfoField{{
			ConsDir: true,
		}},
		HopFields: make([]*slayerspath.HopField, len(opaque)),
	}

	for i, iface := range opaque {
		rp.HopFields[i] = &slayerspath.HopField{
			ConsIngress: iface.Ingress,
			ConsEgress:  iface.Egress,
		}
	}
	buff := make([]byte, rp.Len())
	err = rp.SerializeTo(buff)
	if err != nil {
		panic(err)
	}

	return path.Path{
		Meta: snet.PathMetadata{
			Interfaces: ifaces,
		},
		SPath: spath.Path{
			Raw:  buff,
			Type: scion.PathType,
		},
	}
}

func NewReservation() *segment.Reservation {
	return NewRsv(
		WithID("ff00:0:1", "beefcafe"),
		WithPath(0, "1-ff00:0:1", 1, 1, "1-ff00:0:2", 0))
}

// ReservationMod allows the configuration of reservations via function calls, aka
// functional options.
// As this signature is used only in tests, it doesn't return an error: it assumes that
// the function implementing the option will panic if error.
type ReservationMod func(*segment.Reservation) *segment.Reservation

// NewRsv creates a reservation configured via functional options.
func NewRsv(mods ...ReservationMod) *segment.Reservation {
	rsv := segment.NewReservation()
	return ModRsv(rsv, mods...)
}

// ModRsv simply modifies an existing reservation via functional options.
func ModRsv(rsv *segment.Reservation, mods ...ReservationMod) *segment.Reservation {
	for _, mod := range mods {
		rsv = mod(rsv)
	}
	return rsv
}

// NewRsvs creates a number of reservations configured via functional options.
func NewRsvs(n int, mods ...ReservationMod) []*segment.Reservation {
	rsvs := make([]*segment.Reservation, n)
	for i := 0; i < n; i++ {
		rsvs[i] = NewRsv(mods...)
	}
	return rsvs
}

// ModRsvs modifies existing reservations  via functional options.
func ModRsvs(rsvs []*segment.Reservation, mods ...ReservationMod) {
	for i, rsv := range rsvs {
		for _, mod := range mods {
			rsv = mod(rsv)
		}
		rsvs[i] = rsv
	}
}

// WithID sets the ID specified with as and suffix to the reservation.
func WithID(as, suffix string) ReservationMod {
	as_ := xtest.MustParseAS(as)
	id, err := reservation.NewSegmentID(as_, xtest.MustParseHexString(suffix))
	if err != nil {
		panic(err)
	}
	return func(rsv *segment.Reservation) *segment.Reservation {
		rsv.ID = *id
		return rsv
	}
}

func WithPath(path ...interface{}) ReservationMod {
	transparent := NewPathFromComponents(path...)
	return func(rsv *segment.Reservation) *segment.Reservation {
		rsv.Path = transparent
		return rsv
	}
}

func WithIngressEgress(ig, eg int) ReservationMod {
	return func(rsv *segment.Reservation) *segment.Reservation {
		if ig > 0 {
			rsv.Ingress = uint16(ig)
		}
		if eg > 0 {
			rsv.Egress = uint16(eg)
		}
		return rsv
	}
}

func WithTrafficSplit(split int) ReservationMod {
	return func(rsv *segment.Reservation) *segment.Reservation {
		rsv.TrafficSplit = reservation.SplitCls(split)
		return rsv
	}
}

func WithEndProps(endProps reservation.PathEndProps) ReservationMod {
	return func(rsv *segment.Reservation) *segment.Reservation {
		rsv.PathEndProps = endProps
		return rsv
	}
}

// WithActiveIndex sets the index specified with idx as active.
func WithActiveIndex(idx int) ReservationMod {
	return func(rsv *segment.Reservation) *segment.Reservation {
		if err := rsv.SetIndexConfirmed(reservation.IndexNumber(idx)); err != nil {
			panic(err)
		}
		if err := rsv.SetIndexActive(reservation.IndexNumber(idx)); err != nil {
			panic(err)
		}
		return rsv
	}
}

func ConfirmAllIndices() ReservationMod {
	return func(rsv *segment.Reservation) *segment.Reservation {
		if rsv == nil || rsv.Indices.Len() == 0 {
			return rsv
		}
		for _, idx := range rsv.Indices {
			if idx.State() != segment.IndexActive {
				if err := rsv.SetIndexConfirmed(idx.Idx); err != nil {
					panic(err)
				}
			}
		}
		return rsv
	}
}

// IndexMod allows the creation of indices with parameters via functional configuration.
// This type doesn't return an error, thus assumes the functional option will panic or ignore
// the error.
type IndexMod func(*segment.Index)

// AddIndex adds a new index, modified via functional options, to the reservation.
func AddIndex(mods ...IndexMod) ReservationMod {
	return func(rsv *segment.Reservation) *segment.Reservation {
		expTime := util.SecsToTime(0)
		if rsv.Indices.Len() > 0 {
			expTime = rsv.Indices.GetExpiration(rsv.Indices.Len() - 1)
		}
		idx, err := rsv.NewIndexAtSource(expTime, 0, 0, 0, 0, 0)
		if err != nil {
			panic(err)
		}
		index := rsv.Index(idx)
		for _, mod := range mods {
			mod(index)
		}
		return rsv
	}
}

// ModIndex applies the functional options to the index specified.
func ModIndex(idx reservation.IndexNumber, mods ...IndexMod) ReservationMod {
	return func(rsv *segment.Reservation) *segment.Reservation {
		index := rsv.Index(idx)
		if index == nil {
			panic(fmt.Errorf("index is nil. idx = %d, len = %d", idx, rsv.Indices.Len()))
		}
		for _, mod := range mods {
			mod(index)
		}
		return rsv
	}
}

// WithBW changes the min, max and/or alloc BW if their values are > 0.
func WithBW(min, max, alloc int) IndexMod {
	return func(index *segment.Index) {
		if min > 0 {
			index.MinBW = reservation.BWCls(min)
		}
		if max > 0 {
			index.MaxBW = reservation.BWCls(max)
		}
		if alloc > 0 {
			index.AllocBW = reservation.BWCls(alloc)
		}
	}
}

// WithExpiration sets the expiration to the index (and its token).
func WithExpiration(exp time.Time) IndexMod {
	return func(index *segment.Index) {
		index.Expiration = exp
		index.Token.ExpirationTick = reservation.TickFromTime(exp)
	}
}
