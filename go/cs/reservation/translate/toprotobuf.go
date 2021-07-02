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
	base "github.com/scionproto/scion/go/cs/reservation"
	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/lib/colibri"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/util"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
)

func PBufSetupReq(req *segment.SetupReq) *colpb.SegmentSetupRequest {

	return &colpb.SegmentSetupRequest{
		Base:   PBufRequest(&req.Request),
		Params: PBufSetupRequestParams(req),
	}
}

func PBufSetupResponse(res segment.SegmentSetupResponse) *colpb.SegmentSetupResponse {
	pbRes := &colpb.SegmentSetupResponse{}

	switch r := res.(type) {
	case *segment.SegmentSetupResponseSuccess:
		pbRes.SuccessFailure = &colpb.SegmentSetupResponse_Token{
			Token: r.Token.ToRaw(),
		}
	case *segment.SegmentSetupResponseFailure:
		pbRes.SuccessFailure = &colpb.SegmentSetupResponse_Failure_{
			Failure: &colpb.SegmentSetupResponse_Failure{
				Request: PBufSetupRequestParams(r.FailedRequest),
				Failure: &colpb.Response_Failure{
					Message: r.Message,
				},
			},
		}
	}
	return pbRes
}

func PBufRequest(req *base.Request) *colpb.Request {
	return &colpb.Request{
		Id:        PBufID(&req.ID),
		Index:     uint32(req.Index),
		Timestamp: util.TimeToSecs(req.Timestamp),
		Path:      PBufPath(req.Path),
	}
}

func PBufSetupRequestParams(req *segment.SetupReq) *colpb.SegmentSetupRequest_Params {
	return &colpb.SegmentSetupRequest_Params{
		ExpirationTime: util.TimeToSecs(req.ExpirationTime),
		Rlc:            uint32(req.RLC),
		PathType:       uint32(req.PathType),
		Minbw:          uint32(req.MinBW),
		Maxbw:          uint32(req.MaxBW),
		Splitcls:       uint32(req.SplitCls),
		PropsAtStart: &colpb.PathEndProps{
			Local:    req.PathProps.StartLocal(),
			Transfer: req.PathProps.StartTransfer(),
		},
		PropsAtEnd: &colpb.PathEndProps{
			Local:    req.PathProps.EndLocal(),
			Transfer: req.PathProps.EndTransfer(),
		},
		Allocationtrail: PBufAllocTrail(req.AllocTrail),
	}
}

func PBufResponse(res base.Response) *colpb.Response {
	switch r := res.(type) {
	case *base.ResponseSuccess:
		return &colpb.Response{SuccessFailure: &colpb.Response_Success_{}}
	case *base.ResponseFailure:
		return &colpb.Response{
			SuccessFailure: &colpb.Response_Failure_{
				Failure: &colpb.Response_Failure{
					ErrorCode: r.ErrorCode,
					Message:   r.Message,
				},
			},
		}
	default:
		return nil
	}
}

func PBufListResponse(res []*colibri.ReservationLooks) *colpb.ListResponse {
	looks := make([]*colpb.ListResponse_Reservations_ReservationLooks, len(res))
	for i, l := range res {
		looks[i] = &colpb.ListResponse_Reservations_ReservationLooks{
			ID:    PBufID(&l.Id),
			DstIa: uint64(l.DstIA.IAInt()),
		}
	}
	return &colpb.ListResponse{
		SuccessFailure: &colpb.ListResponse_Reservations_{
			Reservations: &colpb.ListResponse_Reservations{
				Reservations: looks,
			},
		},
	}
}

func PBufID(id *reservation.ID) *colpb.ReservationID {
	return &colpb.ReservationID{
		Asid:   uint64(id.ASID),
		Suffix: append(id.Suffix[:0:0], id.Suffix...),
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

func PBufPath(transp *base.TransparentPath) *colpb.TransparentPath {
	if transp == nil {
		return &colpb.TransparentPath{
			Steps: []*colpb.PathStep{},
		}
	}
	steps := make([]*colpb.PathStep, len(transp.Steps))
	for i, step := range transp.Steps {
		steps[i] = &colpb.PathStep{
			Ia:      uint64(step.IA.IAInt()),
			Ingress: uint32(step.Ingress),
			Egress:  uint32(step.Egress),
		}
	}
	return &colpb.TransparentPath{
		CurrentStep: uint32(transp.CurrentStep),
		Steps:       steps,
		SpathType:   uint32(transp.Spath.Type),
		SpathRaw:    transp.Spath.Raw,
	}

}
