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
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/util"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
)

func SetupReq(msg *colpb.SegmentSetupRequest,
	path base.PacketPath, ingress, egress uint16,
) (*segment.SetupReq, error) {

	if msg == nil || msg.Base == nil || msg.Params == nil {
		return nil, serrors.New("incomplete message", "msg", msg)
	}
	ID, err := SegmentID(msg.Base.Id)
	if err != nil {
		return nil, err
	}
	idx, err := Index(msg.Base.Index)
	if err != nil {
		return nil, err
	}
	rlc, err := RLC(msg.Params.Rlc)
	if err != nil {
		return nil, err
	}
	pathType, err := PathType(msg.Params.PathType)
	if err != nil {
		return nil, err
	}
	minbw, err := BW(msg.Params.Minbw)
	if err != nil {
		return nil, err
	}
	maxbw, err := BW(msg.Params.Maxbw)
	if err != nil {
		return nil, err
	}
	splitcls, err := SplitCls(msg.Params.Splitcls)
	if err != nil {
		return nil, err
	}
	req := &segment.SetupReq{
		Request: segment.Request{
			ID:        *ID,
			Index:     idx,
			Timestamp: util.SecsToTime(msg.Base.Timestamp),
			Ingress:   ingress,
			Egress:    egress,
		},
		ExpirationTime: util.SecsToTime(msg.Params.ExpirationTime),
		RLC:            rlc,
		PathType:       pathType,
		MinBW:          minbw,
		MaxBW:          maxbw,
		SplitCls:       splitcls,
		PathProps: reservation.NewPathEndProps(
			msg.Params.PropsAtStart.Local,
			msg.Params.PropsAtStart.Transfer,
			msg.Params.PropsAtEnd.Local,
			msg.Params.PropsAtEnd.Transfer),
		AllocTrail: AllocTrail(msg.Params.Allocationtrail),
	}
	req.SetPacketPath(path)
	return req, nil
}

func Index(msg uint32) (reservation.IndexNumber, error) {
	idx := reservation.IndexNumber(msg)
	if uint32(idx) != msg {
		return 0, serrors.New("index is out of range", "idx", msg)
	}
	return idx, idx.Validate()
}

func RLC(msg uint32) (reservation.RLC, error) {
	rlc := reservation.RLC(msg)
	if uint32(rlc) != msg {
		return 0, serrors.New("rlc is out of range", "rlc", rlc)
	}
	return rlc, rlc.Validate()
}

func PathType(msg uint32) (reservation.PathType, error) {
	pt := reservation.PathType(msg)
	if uint32(pt) != msg {
		return 0, serrors.New("path type is out of range", "path_type", pt)
	}
	return pt, pt.Validate()
}

func BW(msg uint32) (reservation.BWCls, error) {
	bw := reservation.BWCls(msg)
	if uint32(bw) != msg {
		return 0, serrors.New("bw class is out of range", "bw", msg)
	}
	return bw, bw.Validate()
}

func SplitCls(msg uint32) (reservation.SplitCls, error) {
	sc := reservation.SplitCls(msg)
	if uint32(sc) != msg {
		return 0, serrors.New("split class is out of range", "class", msg)
	}
	return sc, nil
}

func SegmentID(msg *colpb.ReservationID) (*reservation.SegmentID, error) {
	if len(msg.Suffix) != 4 {
		return nil, serrors.New("bad suffix; must be 4 bytes", "len", len(msg.Suffix))
	}
	a, b, c, d := msg.Suffix[0], msg.Suffix[1], msg.Suffix[2], msg.Suffix[3]
	return &reservation.SegmentID{
		ASID:   addr.AS(msg.Asid),
		Suffix: [4]byte{a, b, c, d},
	}, nil
}

func Token(msg *colpb.SegmentSetupResponse_Token) (*reservation.Token, error) {
	return reservation.TokenFromRaw(msg.Token)
}

func AllocTrail(msg []*colpb.AllocationBead) reservation.AllocationBeads {
	trail := make(reservation.AllocationBeads, len(msg))
	for i, bead := range msg {
		trail[i] = reservation.AllocationBead{
			AllocBW: reservation.BWCls(bead.Allocbw),
			MaxBW:   reservation.BWCls(bead.Maxbw),
		}
	}
	return trail
}
