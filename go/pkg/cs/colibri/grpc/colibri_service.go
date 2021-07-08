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

package grpc

import (
	"context"

	"google.golang.org/grpc/peer"
	"google.golang.org/protobuf/proto"

	base "github.com/scionproto/scion/go/cs/reservation"
	"github.com/scionproto/scion/go/cs/reservation/translate"
	"github.com/scionproto/scion/go/cs/reservationstorage"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/coliquic"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
)

type ColibriService struct {
	Store reservationstorage.Store
}

var _ colpb.ColibriServer = (*ColibriService)(nil)

func (s *ColibriService) SetupSegment(ctx context.Context, msg *colpb.SegmentSetupRequest) (
	*colpb.SegmentSetupResponse, error) {

	msg.Base.Path.CurrentStep++
	sizeeeeeeeeeeee := proto.Size(msg)
	log.Info("DELETEME received call on SetupSegment()", "size", sizeeeeeeeeeeee, "setup_path", msg.Base.Path)

	// path, err := extractPath(ctx)
	// if err != nil {
	// 	log.Error("setup segment", "err", err)
	// 	return nil, err
	// }
	req, err := translate.SetupReq(msg)
	if err != nil {
		log.Error("error unmarshalling", "err", err)
		// should send a message?
		return nil, err
	}
	log.Info("deleteme path after translation", "path", req.Path)
	res, err := s.Store.AdmitSegmentReservation(ctx, req)
	if err != nil {
		log.Error("colibri store returned an error", "err", err)
		// should send a message?
		return nil, err
	}
	log.Info("deleteme after store", "res", res)
	pbRes := translate.PBufSetupResponse(res)
	log.Info("deleteme", "pbres", pbRes)
	return pbRes, nil
}

func (s *ColibriService) ConfirmSegmentIndex(ctx context.Context, msg *colpb.Request) (
	*colpb.Response, error) {

	msg.Path.CurrentStep++
	req, err := translate.Request(msg)
	if err != nil {
		log.Error("error unmarshalling", "err", err)
		return nil, err
	}
	log.Info("deleteme path after translation", "path", req.Path)
	res, err := s.Store.ConfirmSegmentReservation(ctx, req)
	if err != nil {
		log.Error("colibri store returned an error", "err", err)
		return nil, err
	}
	pbRes := translate.PBufResponse(res)
	log.Info("deleteme", "pbres", pbRes)

	return pbRes, nil
}

func (s *ColibriService) ActivateSegmentIndex(ctx context.Context, msg *colpb.Request) (
	*colpb.Response, error) {

	msg.Path.CurrentStep++
	req, err := translate.Request(msg)
	if err != nil {
		log.Error("error unmarshalling", "err", err)
		return nil, err
	}
	log.Info("deleteme path after translation", "path", req.Path)
	res, err := s.Store.ActivateSegmentReservation(ctx, req)
	if err != nil {
		log.Error("colibri store returned an error", "err", err)
		return nil, err
	}
	pbRes := translate.PBufResponse(res)
	log.Info("deleteme", "pbres", pbRes)

	return pbRes, nil
}

func (s *ColibriService) TeardownSegment(ctx context.Context, msg *colpb.Request) (
	*colpb.Response, error) {

	log.Info("DELETEME received call on TeardownSegment()")
	msg.Path.CurrentStep++
	req, err := translate.Request(msg)
	if err != nil {
		log.Error("error unmarshalling", "err", err)
		return nil, err
	}
	log.Info("deleteme path after translation", "path", req.Path)
	res, err := s.Store.TearDownSegmentReservation(ctx, req)
	if err != nil {
		log.Error("colibri store returned an error", "err", err)
		return nil, err
	}
	pbRes := translate.PBufResponse(res)
	log.Info("deleteme", "pbres", pbRes)

	return pbRes, nil
}

func (s *ColibriService) CleanupSegmentIndex(ctx context.Context, msg *colpb.Request) (
	*colpb.Response, error) {

	msg.Path.CurrentStep++
	req, err := translate.Request(msg)
	if err != nil {
		log.Error("error unmarshalling", "err", err)
		return nil, err
	}
	log.Info("deleteme path after translation", "path", req.Path)
	res, err := s.Store.CleanupSegmentReservation(ctx, req)
	if err != nil {
		log.Error("colibri store returned an error", "err", err)
		return nil, err
	}
	pbRes := translate.PBufResponse(res)
	log.Info("deleteme", "pbres", pbRes)

	return pbRes, nil
}

func (s *ColibriService) ListReservations(ctx context.Context, msg *colpb.ListRequest) (
	*colpb.ListResponse, error) {

	log.Info("deleteme ListReservations", "dst", addr.IAInt(msg.DstIa).IA().String(),
		"type", reservation.PathType(msg.PathType))
	dstIA := addr.IAInt(msg.DstIa).IA()
	//
	// deleteme
	//
	deletemeRsvs, err := s.Store.GetReservationsAtSource(ctx, dstIA)
	log.Info("deleteme all rsvs from this AS", "count", len(deletemeRsvs), "err", err)
	//
	//
	looks, err := s.Store.ListReservations(ctx, dstIA, reservation.PathType(msg.PathType))
	if err != nil {
		log.Error("colibri store while listing rsvs", "err", err)
		return &colpb.ListResponse{
			SuccessFailure: &colpb.ListResponse_FailureMessage{
				FailureMessage: err.Error(),
			},
		}, nil
	}
	log.Info("deleteme ListReservations returning", "count", len(looks))
	return translate.PBufListResponse(looks), nil
}

func (s *ColibriService) SetupE2E(ctx context.Context, msg *colpb.E2ESetupRequest) (
	*colpb.E2ESetupResponse, error) {

	return nil, nil
}

func (s *ColibriService) CleanupE2EIndex(ctx context.Context, msg *colpb.Request) (
	*colpb.Response, error) {

	msg.Path.CurrentStep++
	req, err := translate.Request(msg)
	if err != nil {
		log.Error("error unmarshalling", "err", err)
		return nil, err
	}
	log.Info("deleteme path after translation", "path", req.Path)
	res, err := s.Store.CleanupE2EReservation(ctx, req)
	if err != nil {
		log.Error("colibri store returned an error", "err", err)
		return nil, err
	}
	pbRes := translate.PBufResponse(res)
	log.Info("deleteme", "pbres", pbRes)

	return pbRes, nil
}

// extractPath returns the PacketPath, ingress and egress used with this RPC.
func extractPath(ctx context.Context) (base.PacketPath, error) {
	// TODO(juagargi) move from PacketPath to TransparentPath
	// TODO(juagargi) call this function to check that the transport path matches that
	// of base.Request.Path if the transport path is of colibri type.
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		log.Error("deleteme no peer found")
		return nil, serrors.New("no peer found")
	}
	raddr, ok := p.Addr.(*snet.UDPAddr)
	if !ok || raddr == nil {
		log.Error("deleteme no scion address found")
		return nil, serrors.New("no valid scion address found", "addr", p.Addr)
	}
	log.Info("deleteme scion address", "addr", raddr)
	path, err := base.NewPacketPath(raddr.Path)
	if err != nil {
		return path, err
	}
	log.Info("deleteme path and interfaces", "path_type", raddr.Path.Type,
		"packet_path", path)
	usage, ok, err := coliquic.UsageFromContext(ctx)
	_, _, _ = usage, ok, err
	return path, err
}
