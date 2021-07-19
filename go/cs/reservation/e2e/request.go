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

package e2e

import (
	base "github.com/scionproto/scion/go/cs/reservation"
	col "github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/serrors"
)

// SetupReq is an e2e setup/renewal request, that has been so far accepted.
type SetupReq struct {
	base.Request
	SegmentRsvs []col.ID
	// SegmentRsvASCount        []uint8 // how many ASes per segment reservation
	RequestedBW              col.BWCls
	AllocationTrail          []col.BWCls
	FailureInfo              *SetupFailureInfo // or nil if successful
	totalASCount             int
	currentASSegmentRsvIndex int // the index in SegmentRsv for the current AS
	isTransfer               bool
}

type SetupFailureInfo struct {
	NodeIndex int
	Message   string
}

// NewSetupRequest creates and initializes an e2e setup request common for both success and failure.
func NewSetupRequest(r *base.Request, segRsvs []col.ID, segRsvCount []uint8,
	requestedBW col.BWCls, allocTrail []col.BWCls) (*SetupReq, error) {

	if len(segRsvs) != len(segRsvCount) || len(segRsvs) == 0 {
		return nil, serrors.New("e2e setup request invalid", "seg_rsv_len", len(segRsvs),
			"seg_rsv_count_len", len(segRsvCount))
	}
	totalASCount := 0
	currASindex := -1
	isTransfer := false
	n := len(allocTrail) - 1
	for i, c := range segRsvCount {
		totalASCount += int(c)
		n -= int(c) - 1
		if i == len(segRsvCount)-1 {
			n-- // the last segment spans 1 more AS
		}
		if n < 0 && currASindex < 0 {
			currASindex = i
			isTransfer = i < len(segRsvCount)-1 && n == -1 // dst AS is no transfer
		}
	}
	totalASCount -= len(segRsvCount) - 1
	if currASindex < 0 {
		return nil, serrors.New("error initializing e2e request",
			"alloc_trail_len", len(allocTrail), "seg_rsv_count", segRsvCount)
	}
	return &SetupReq{
		Request:     *r,
		SegmentRsvs: segRsvs,
		// SegmentRsvASCount:        segRsvCount,
		RequestedBW:              requestedBW,
		AllocationTrail:          allocTrail,
		totalASCount:             totalASCount,
		currentASSegmentRsvIndex: currASindex,
		isTransfer:               isTransfer,
	}, nil
}

func (r *SetupReq) Success() bool {
	return r.FailureInfo == nil
}

func (r *SetupReq) Transfer() bool {
	return r.isTransfer
}

type PathLocation int

const (
	Source PathLocation = iota
	Transit
	Destination
)

func (l PathLocation) String() string {
	switch l {
	case Source:
		return "source"
	case Transit:
		return "trantit"
	case Destination:
		return "destination"
	}
	return "unknown path location"
}

// Location returns the location of this node in the path of the request.
func (r *SetupReq) Location() PathLocation {
	switch len(r.AllocationTrail) {
	case 0:
		return Source
	case r.totalASCount:
		return Destination
	default:
		return Transit
	}
}

// SegmentRsvIDsForThisAS returns the segment reservation ID this AS belongs to. Iff this
// AS is a transfer AS (stitching point), there will be two reservation IDs returned, in the
// order of traversal.
func (r *SetupReq) SegmentRsvIDsForThisAS() []col.ID {
	indices := make([]col.ID, 1, 2)
	indices[0] = r.SegmentRsvs[r.currentASSegmentRsvIndex]
	if r.isTransfer {
		indices = append(indices, r.SegmentRsvs[r.currentASSegmentRsvIndex+1])
	}
	return indices
}
