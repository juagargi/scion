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
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
)

type ReservationLooks struct {
	Id    reservation.ID
	DstIA addr.IA
}

// StitchableSegments is a collection of up, core and down segments that could be stitched
// to reach a destination, after a combination process.
type StitchableSegments struct {
	Up, Core, Down []*ReservationLooks
}

// FullTrip is a set of stitched segment reservations that would allow to setup an E2E rsv.
// The length of a fulltrip is 1, 2 or 3 segments.
type FullTrip []*ReservationLooks // in order

// Combine will attempt to create full reservations that have two stitching points, from
// an up, core and down slices of reservations.
func Combine(segments *StitchableSegments) []*FullTrip {
	return nil
}
