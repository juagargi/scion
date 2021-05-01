// Copyright 2021 ETH Zurich, Anapaya Systems
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

package translate

import (
	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/util"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
)

func PBufSetupReq(req *segment.SetupReq) *colpb.SegmentSetupRequest {

	return &colpb.SegmentSetupRequest{
		Base: PBufBase(&req.Request),
		Params: &colpb.SegmentSetupRequest_Params{
			Minbw:    uint32(req.MinBW),
			Maxbw:    uint32(req.MaxBW),
			Splitcls: uint32(req.SplitCls),
			PropsAtStart: &colpb.PathEndProps{
				Local:    req.PathProps.StartLocal(),
				Transfer: req.PathProps.StartTransfer(),
			},
			PropsAtEnd: &colpb.PathEndProps{
				Local:    req.PathProps.EndLocal(),
				Transfer: req.PathProps.EndTransfer(),
			},
			Infofield:       req.InfoField.ToRaw(),
			Allocationtrail: PBufAllocTrail(req.AllocTrail),
		},
	}
}

func PBufID(id *reservation.SegmentID) *colpb.ReservationID {
	return &colpb.ReservationID{
		Asid:   uint64(id.ASID),
		Suffix: append(id.Suffix[:0:0], id.Suffix[:]...),
	}
}

func PBufBase(base *segment.Request) *colpb.Base {
	return &colpb.Base{
		Id:        PBufID(&base.ID),
		Index:     uint32(base.Index),
		Timestamp: util.TimeToSecs(base.Timestamp),
	}
}

func PBufAllocTrail(trail reservation.AllocationBeads) []*colpb.AllocationBead {
	beads := make([]*colpb.AllocationBead, len(trail))
	for i, bead := range trail {
		beads[i] = &colpb.AllocationBead{
			Allocbw: uint32(bead.AllocBW),
			Maxbw:   uint32(bead.MaxBW),
		}
	}
	return beads
}
