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
	"time"

	base "github.com/scionproto/scion/go/cs/reservation"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/serrors"
)

// Request is the base struct for any type of COLIBRI segment request.
// It contains a reference to the reservation it requests, or nil if not yet created.
type Request struct {
	base.MsgId
	Path        *OpaquePath  // the path to the destination. It represents the hops of the reservation.
	Reservation *Reservation // nil if no reservation yet
}

// NewRequest constructs the segment Request type.
func NewRequest(ts time.Time, id *reservation.SegmentID, idx reservation.IndexNumber,
	path *OpaquePath) (*Request, error) {

	if id == nil {
		return nil, serrors.New("new segment request with nil ID")
	}
	return &Request{
		MsgId: base.MsgId{
			Timestamp: ts,
			ID:        *id,
			Index:     idx,
		},
		Path: path,
	}, nil
}

// Validate ensures the data in the request is consistent. Calling methods on the request
// before a call to Validate may result in invalid behavior or panic.
func (r *Request) Validate() error {
	if r.Path == nil || len(r.Path.Steps) <= r.Path.CurrentStep {
		return serrors.New("bad path in request", "path", r.Path)
	}
	if r.ID.ASID == 0 {
		return serrors.New("bad AS id in request", "asid", r.ID.ASID)
	}
	return nil
}

func (r *Request) IsLastAS() bool { // override the use of the RequestMetadata.path with PathToDst
	return r.Path.CurrentStep == len(r.Path.Steps)-1
}

// Ingress returns the ingress interface of this step for this request.
// Do not call Ingress without validating the request first.
func (r *Request) Ingress() uint16 {
	p := r.Path
	return p.Steps[p.CurrentStep].Ingress
}

// Egress returns the egress interface of this step for this request.
// Do not call Egress without validating the request first.
func (r *Request) Egress() uint16 {
	p := r.Path
	return p.Steps[p.CurrentStep].Egress
}

// SetupReq is a segment reservation setup request.
// This same type is used for renewal of the segment reservation.
type SetupReq struct {
	Request

	ExpirationTime time.Time
	RLC            reservation.RLC
	PathType       reservation.PathType
	MinBW          reservation.BWCls
	MaxBW          reservation.BWCls
	SplitCls       reservation.SplitCls
	PathProps      reservation.PathEndProps
	AllocTrail     reservation.AllocationBeads
	PathAtSource   *OpaquePath // requested path (maybe different than transport)
}

func (r *SetupReq) Validate() error {
	if err := r.Request.Validate(); err != nil {
		return err
	}
	if len(r.AllocTrail) > len(r.Path.Steps) {
		return serrors.New("inconsistent trail and setup path", "trail", r.AllocTrail,
			"path", r.Path)
	}
	return nil
}

// PrevBW returns the minimum of the maximum bandwidths already granted by previous ASes.
func (r *SetupReq) PrevBW() uint64 {
	return r.AllocTrail.MinMax().ToKbps()
}

// SetupTelesReq represents a telescopic segment setup.
type SetupTelesReq struct {
	SetupReq
	BaseID reservation.SegmentID
}

// TeardownReq requests the AS to remove a given index from the DB. If this is the last index
// in the reservation, the reservation will be completely removed.
type TeardownReq struct {
	Request
}

// IndexConfirmationReq is used to change the state on an index (e.g. from temporary to pending).
type IndexConfirmationReq struct {
	Request
	State IndexState
}

// CleanupReq is used to clean an index.
type CleanupReq struct {
	Request
}
