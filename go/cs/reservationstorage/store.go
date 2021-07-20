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

package reservationstorage

import (
	"context"
	"time"

	base "github.com/scionproto/scion/go/cs/reservation"
	"github.com/scionproto/scion/go/cs/reservation/e2e"
	sgt "github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
)

// Store is the interface to interact with the reservation store.
type Store interface {
	// ListReservations is used to get segments to other ASes.
	ListReservations(ctx context.Context, dstIA addr.IA, pt reservation.PathType) (
		[]*colibri.ReservationLooks, error)
	AdmitSegmentReservation(ctx context.Context, req *sgt.SetupReq) (
		sgt.SegmentSetupResponse, error)
	ConfirmSegmentReservation(ctx context.Context, req *base.Request) (
		base.Response, error)
	ActivateSegmentReservation(ctx context.Context, req *base.Request) (
		base.Response, error)
	CleanupSegmentReservation(ctx context.Context, req *base.Request) (
		base.Response, error)
	TearDownSegmentReservation(ctx context.Context, req *base.Request) (
		base.Response, error)
	AdmitE2EReservation(ctx context.Context, req *e2e.SetupReq) (
		e2e.SetupResponse, error)
	CleanupE2EReservation(ctx context.Context, req *base.Request) (
		base.Response, error)

	// DeleteExpiredIndices returns the number of indices deleted, and the time for the
	// next expiration
	DeleteExpiredIndices(ctx context.Context) (int, time.Time, error)

	// as the source of reservations:

	// GetReservationsAtSource is used by a reservation manager or keeper to know all
	// reservations they must keep updated.
	GetReservationsAtSource(ctx context.Context, dstIA addr.IA) ([]*sgt.Reservation, error)
	// ListStitchableSegments is used by the endhosts. It will rely on calls to ListReservations
	// to this AS and other ASes.
	ListStitchableSegments(ctx context.Context, dst addr.IA) (*colibri.StitchableSegments, error)
	// InitSegmentReservation starts a new segment reservation.
	InitSegmentReservation(ctx context.Context, req *sgt.SetupReq) error
}
