// Copyright 2021 ETH Zurich
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

// Package colibri contains methods for the creation and verification of the colibri packet
// timestamp and validation fields.
package colibri

import (
	"fmt"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/serrors"
)

// FullTrip is a set of stitched segment reservations that would allow to setup an E2E rsv.
// The length of a fulltrip is 1, 2 or 3 segments.
type FullTrip []*ReservationLooks // in order

func (t FullTrip) Validate() error {
	if len(t) < 1 || len(t) > 3 {
		return serrors.New("bad reservation full trip", "segment_count", len(t))
	}
	return nil
}

func (t FullTrip) SrcIA() addr.IA {
	return t[0].SrcIA
}

func (t FullTrip) DstIA() addr.IA {
	return t[len(t)-1].DstIA
}

func (t FullTrip) Copy() *FullTrip {
	tmp := t
	return &tmp
}

// CombineAll will attempt to create full reservations that have two stitching points, from
// an up, core and down slices of reservations.
func CombineAll(stitchable *StitchableSegments) []*FullTrip {
	fullTrips := combineAll(stitchable)
	for i, t := range fullTrips {
		assert(t.SrcIA() == stitchable.SrcIA,
			fmt.Sprintf("src AS not valid at trip %d / %d", i, len(fullTrips)))
		assert(t.DstIA() == stitchable.DstIA,
			fmt.Sprintf("dst AS not valid at trip %d / %d", i, len(fullTrips)))
		err := t.Validate()
		assert(err == nil,
			fmt.Sprintf("invalid trip at %d / %d : %s", i, len(fullTrips), err.Error()))
	}
	return fullTrips
}

// combineAll will create all possible combinations of up-core-down segment reservations to
// reach the destination from the source. Some segments might be omitted in some of the trips,
// e.g. some trips could consist of an up segment only, or up-down, or core, or core-down.
func combineAll(stitchable *StitchableSegments) []*FullTrip {
	fulltrips := make([]*FullTrip, 0)
	// check which of the up segments do not reach the destination
	ups := make([]*ReservationLooks, 0, len(stitchable.Up))
	for _, s := range stitchable.Up {
		if s.DstIA == stitchable.DstIA {
			trip := &FullTrip{s.Copy()}
			fulltrips = append(fulltrips, trip)
		} else {
			ups = append(ups, s)
		}
	}
	if len(ups) == 0 && len(stitchable.Up) > 0 {
		// we had up segments but no more -> won't be able to reach dst without them,
		// return already; This is true as long as core ASes cannot create reservations other
		// than core segment type.
		return fulltrips
	}

	// create maps to classify the core segments by source and down segments by source
	cores := segmentsToMap(stitchable.Core)
	downs := segmentsToMap(stitchable.Down)

	if stitchable.SrcIA.I == stitchable.DstIA.I {
		// if the ISD is the same, try to stitch up + down
		for _, up := range ups {
			if downSegs, ok := downs[up.DstIA]; ok {
				for _, down := range downSegs {
					trip := &FullTrip{up.Copy(), down.Copy()}
					fulltrips = append(fulltrips, trip)
				}
			}
		}
	}

	// for local and non local ISDs, temporarily stitch ups and cores
	tempTrips := make([]*FullTrip, 0)
	dstLooksLikeCoreAS := false
	if len(ups) == 0 {
		if coreSegs, ok := cores[stitchable.SrcIA]; ok {
			// src might be a core AS
			for _, core := range coreSegs {
				trip := &FullTrip{core.Copy()}
				tempTrips = append(tempTrips, trip)
				dstLooksLikeCoreAS = dstLooksLikeCoreAS || (core.DstIA == stitchable.DstIA)
			}
		}
	}
	for _, up := range ups {
		if coreSegs, ok := cores[up.DstIA]; ok {
			for _, core := range coreSegs {
				trip := &FullTrip{up.Copy(), core.Copy()}
				tempTrips = append(tempTrips, trip)
				dstLooksLikeCoreAS = dstLooksLikeCoreAS || (core.DstIA == stitchable.DstIA)
			}
		}
	}
	if dstLooksLikeCoreAS {
		// We have at least one full trip src-dst ending in a core segment rsv.
		// This means the dst is a core AS, and we won't reach it with a down segment.
		for _, t := range tempTrips {
			assert(t.SrcIA() == stitchable.SrcIA, "source ASes are different!")
			if t.DstIA() == stitchable.DstIA {
				fulltrips = append(fulltrips, t)
			}
		}
		return fulltrips
	}

	// In tempTrips we have partial, temporary trips up->core or core that don't reach the dst.
	// We will attempt to complete these temporary trips with a down segment.
	for _, t := range tempTrips {
		if downSegs, ok := downs[t.DstIA()]; ok {
			for _, down := range downSegs {
				trip := t.Copy()
				*trip = append(*trip, down)
				fulltrips = append(fulltrips, trip)
			}
		}
	}
	return fulltrips
}

func segmentsToMap(sgmts []*ReservationLooks) map[addr.IA][]*ReservationLooks {
	segsPerSrc := make(map[addr.IA][]*ReservationLooks, len(sgmts))
	for _, r := range sgmts {
		segsPerSrc[r.SrcIA] = append(segsPerSrc[r.SrcIA], r)
	}
	return segsPerSrc
}

// assert is a hard check on an invariant. If the assert fails, a bug is present that
// will taint other part of the system and make it crash later.
// TODO(juagargi) though a violation of the invariant, in this particular algorithm the bug
// causing the violation could just prevent some solutions from being returned, and not crash
// the system. Maybe a very noticeable log entry should suffice.
func assert(cond bool, msg string) {
	if !cond {
		panic(msg)
	}
}
