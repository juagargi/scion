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
)

// Store is the interface to interact with the reservation store.
type Store interface {
	AdmitSegmentReservation(ctx context.Context, req *sgt.SetupReq) (
		sgt.SegmentSetupResponse, error)
	ConfirmSegmentReservation(ctx context.Context, req *sgt.Request) (
		base.Response, error)
	CleanupSegmentReservation(ctx context.Context, req *sgt.Request) (
		base.Response, error)
	TearDownSegmentReservation(ctx context.Context, req *sgt.Request) (
		base.Response, error)
	AdmitE2EReservation(ctx context.Context, req e2e.SetupRequest) (
		base.Response, error)
	CleanupE2EReservation(ctx context.Context, req *e2e.CleanupReq) (
		base.Response, error)

	// DeleteExpiredIndices returns the number of indices deleted, and the time for the
	// next expiration
	DeleteExpiredIndices(ctx context.Context) (int, time.Time, error)

	// as the source of reservations:

	// InitSegmentReservation starts a new segment reservation.
	InitSegmentReservation(ctx context.Context, req *sgt.SetupReq) error
	GetSegmentRsvsFromSrcDstIA(ctx context.Context, src, dst addr.IA) ([]*sgt.Reservation, error)
}
