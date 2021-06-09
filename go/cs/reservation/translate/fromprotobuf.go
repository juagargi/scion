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
	"fmt"
	"time"

	base "github.com/scionproto/scion/go/cs/reservation"
	"github.com/scionproto/scion/go/cs/reservation/segment"
	"github.com/scionproto/scion/go/lib/addr"
	col "github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/slayers/path"
	"github.com/scionproto/scion/go/lib/spath"
	"github.com/scionproto/scion/go/lib/util"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
)

func SetupReq(msg *colpb.SegmentSetupRequest) (*segment.SetupReq, error) {
	if msg == nil || msg.Base == nil || msg.Params == nil {
		return nil, serrors.New("incomplete message", "msg", msg)
	}
	base, err := Request(msg.Base)
	if err != nil {
		return nil, err
	}
	expTime, rlc, pathType, minbw, maxbw, splitcls, pathProps, allocTrail, err :=
		segmentSetupRequest_Params(msg.Params)
	if err != nil {
		return nil, err
	}
	req := &segment.SetupReq{
		Request:        *base,
		ExpirationTime: expTime,
		RLC:            rlc,
		PathType:       pathType,
		MinBW:          minbw,
		MaxBW:          maxbw,
		SplitCls:       splitcls,
		PathProps:      pathProps,
		AllocTrail:     allocTrail,
	}
	return req, nil
}

func SetupResponse(msg *colpb.SegmentSetupResponse) (segment.SegmentSetupResponse, error) {
	var res segment.SegmentSetupResponse
	switch oneof := msg.SuccessFailure.(type) {
	case *colpb.SegmentSetupResponse_Token:
		tok, err := col.TokenFromRaw(oneof.Token)
		if err != nil {
			return nil, err
		}
		res = &segment.SegmentSetupResponseSuccess{
			Token: *tok,
		}
	case *colpb.SegmentSetupResponse_Failure_:
		expTime, rlc, pathType, minbw, maxbw, splitcls, pathProps, allocTrail, err :=
			segmentSetupRequest_Params(oneof.Failure.Request)
		if err != nil {
			return nil, err
		}
		res = &segment.SegmentSetupResponseFailure{
			FailedRequest: &segment.SetupReq{ // without base request
				ExpirationTime: expTime,
				RLC:            rlc,
				PathType:       pathType,
				MinBW:          minbw,
				MaxBW:          maxbw,
				SplitCls:       splitcls,
				PathProps:      pathProps,
				AllocTrail:     allocTrail,
			},
			Message: oneof.Failure.Failure.Message,
		}
	}
	return res, nil
}

func Request(msg *colpb.Request) (*segment.Request, error) {
	ID, err := ID(msg.Id)
	if err != nil {
		return nil, err
	}
	idx, err := Index(msg.Index)
	if err != nil {
		return nil, err
	}
	timestamp := util.SecsToTime(msg.Timestamp)
	return &segment.Request{
		MsgId: base.MsgId{
			ID:        *ID,
			Index:     idx,
			Timestamp: timestamp,
		},
		Path: OpaquePath(msg.Opaque),
	}, nil
}

func Response(msg *colpb.Response) base.Response {
	switch r := msg.SuccessFailure.(type) {
	case *colpb.Response_Success_:
		return &base.ResponseSuccess{}
	case *colpb.Response_Failure_:
		return &base.ResponseFailure{
			ErrorCode: r.Failure.ErrorCode,
			Message:   r.Failure.Message,
		}
	default:
		panic(fmt.Sprintf("unknown type %s", common.TypeOf(msg.SuccessFailure)))
	}
}

func Index(msg uint32) (col.IndexNumber, error) {
	idx := col.IndexNumber(msg)
	if uint32(idx) != msg {
		return 0, serrors.New("index is out of range", "idx", msg)
	}
	return idx, idx.Validate()
}

func RLC(msg uint32) (col.RLC, error) {
	rlc := col.RLC(msg)
	if uint32(rlc) != msg {
		return 0, serrors.New("rlc is out of range", "rlc", rlc)
	}
	return rlc, rlc.Validate()
}

func PathType(msg uint32) (col.PathType, error) {
	pt := col.PathType(msg)
	if uint32(pt) != msg {
		return 0, serrors.New("path type is out of range", "path_type", pt)
	}
	return pt, pt.Validate()
}

func BW(msg uint32) (col.BWCls, error) {
	bw := col.BWCls(msg)
	if uint32(bw) != msg {
		return 0, serrors.New("bw class is out of range", "bw", msg)
	}
	return bw, bw.Validate()
}

func SplitCls(msg uint32) (col.SplitCls, error) {
	sc := col.SplitCls(msg)
	if uint32(sc) != msg {
		return 0, serrors.New("split class is out of range", "class", msg)
	}
	return sc, nil
}

func ID(msg *colpb.ReservationID) (*col.ID, error) {
	id := &col.ID{
		ASID:   addr.AS(msg.Asid),
		Suffix: append([]byte{}, msg.Suffix...),
	}
	return id, id.Validate()
}

func Token(msg *colpb.SegmentSetupResponse_Token) (*col.Token, error) {
	return col.TokenFromRaw(msg.Token)
}

func AllocTrail(msg []*colpb.AllocationBead) col.AllocationBeads {
	trail := make(col.AllocationBeads, len(msg))
	for i, bead := range msg {
		trail[i] = col.AllocationBead{
			AllocBW: col.BWCls(bead.Allocbw),
			MaxBW:   col.BWCls(bead.Maxbw),
		}
	}
	return trail
}

func OpaquePath(msg *colpb.OpaquePath) *segment.OpaquePath {
	if msg == nil {
		return nil
	}
	opaque := &segment.OpaquePath{
		CurrentStep: int(msg.CurrentStep),
		Steps:       make([]segment.PathStep, len(msg.Steps)),
		Spath: spath.Path{
			Type: path.Type(msg.SpathType),
			Raw:  msg.SpathRaw,
		},
	}
	for i, step := range msg.Steps {
		opaque.Steps[i].IA = addr.IAInt(step.Ia).IA()
		opaque.Steps[i].Ingress = uint16(step.Ingress)
		opaque.Steps[i].Egress = uint16(step.Egress)
	}
	return opaque
}

func segmentSetupRequest_Params(msg *colpb.SegmentSetupRequest_Params) (expTime time.Time,
	rlc col.RLC, pathType col.PathType, minbw col.BWCls, maxbw col.BWCls, splitcls col.SplitCls,
	pathProps col.PathEndProps, allocTrail col.AllocationBeads, err error) {

	expTime = util.SecsToTime(msg.ExpirationTime)
	rlc, err = RLC(msg.Rlc)
	if err != nil {
		return
	}
	pathType, err = PathType(msg.PathType)
	if err != nil {
		return
	}
	minbw, err = BW(msg.Minbw)
	if err != nil {
		return
	}
	maxbw, err = BW(msg.Maxbw)
	if err != nil {
		return
	}
	splitcls, err = SplitCls(msg.Splitcls)
	if err != nil {
		return
	}
	pathProps = col.NewPathEndProps(
		msg.PropsAtStart.Local,
		msg.PropsAtStart.Transfer,
		msg.PropsAtEnd.Local,
		msg.PropsAtEnd.Transfer)
	allocTrail = AllocTrail(msg.Allocationtrail)
	return
}
