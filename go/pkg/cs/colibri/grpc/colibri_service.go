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
	"fmt"

	"google.golang.org/grpc/peer"

	base "github.com/scionproto/scion/go/cs/reservation"
	"github.com/scionproto/scion/go/cs/reservation/translate"
	"github.com/scionproto/scion/go/cs/reservationstorage"
	"github.com/scionproto/scion/go/lib/colibri/coliquic"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
)

type ColibriService struct {
	MyAddr *snet.UDPAddr
	Store  reservationstorage.Store
}

var _ colpb.ColibriServer = (*ColibriService)(nil)

func (s *ColibriService) TestPeer(ctx context.Context, msg *colpb.TestingMessage) (
	*colpb.TestingMessage, error) {

	log.Info("DELETEME received call on TestPeer()")
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		log.Info("DELETEME weird, no peer", "peer", p)
		return nil, serrors.New("no peer found")
	}
	raddr, ok := p.Addr.(*snet.UDPAddr)
	if !ok || raddr == nil {
		log.Info("DELETEME weird error, raddr is what?", "raddr", raddr, "ok", ok)
		return nil, serrors.New("no valid raddr found")
	}
	// require.IsType(t, &snet.UDPAddr{}, p.Addr)
	// require.Equal(t, colibri.PathType, p.Addr.(*snet.UDPAddr).Path.Type)
	log.Info("DELETEME so far so good", "path_type", raddr.Path.Type)
	usage, ok, err := coliquic.UsageFromContext(ctx)
	_, _, _ = usage, ok, err
	return &colpb.TestingMessage{
		Message: fmt.Sprintf("server address is %s", s.MyAddr),
		Data:    p.Addr.(*snet.UDPAddr).Path.Raw,
	}, nil
}

func (s *ColibriService) SetupSegment(ctx context.Context, msg *colpb.SegmentSetupRequest) (
	*colpb.SegmentSetupResponse, error) {

	path, ingress, egress, err := extractPath(ctx)
	if err != nil {
		log.Error("setup segment", "err", err)
		return nil, err
	}
	req, err := translate.SetupReq(msg, path, ingress, egress)
	if err != nil {
		log.Error("error unmarshalling", "err", err)
		// should send a message?
		return nil, err
	}
	res, err := s.Store.AdmitSegmentReservation(ctx, req)
	if err != nil {
		// should send a message?
		return nil, err
	}
	_ = res
	return nil, nil
}

// extractPath returns the PacketPath, ingress and egress used with this RPC.
func extractPath(ctx context.Context) (base.PacketPath, uint16, uint16, error) {
	p, ok := peer.FromContext(ctx)
	if !ok || p == nil {
		return nil, 0, 0, serrors.New("no peer found")
	}
	raddr, ok := p.Addr.(*snet.UDPAddr)
	if !ok || raddr == nil {
		return nil, 0, 0, serrors.New("no valid scion address found", "addr", p.Addr)
	}
	path, err := base.NewPacketPath(raddr.Path)
	if err != nil {
		return path, 0, 0, err
	}
	ingress, egress, err := path.IngressEgressIFIDs()
	return path, ingress, egress, err
}
