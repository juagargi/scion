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

package colibri

import (
	"context"
	"fmt"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/coliquic"
	"github.com/scionproto/scion/go/lib/colibri/reservation"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
	sdpb "github.com/scionproto/scion/go/pkg/proto/daemon"
	"google.golang.org/grpc"
)

type DaemonClient struct {
	LocalIA     addr.IA
	Dialer      coliquic.GRPCClientDialer
	SrvResolver coliquic.ColSrvResolver
}

func NewServiceClient(localIA addr.IA, dialer coliquic.GRPCClientDialer,
	router snet.Router) *DaemonClient {

	return &DaemonClient{
		LocalIA: localIA,
		Dialer:  dialer,
		SrvResolver: &coliquic.DiscoveryColSrvRes{
			Dialer: dialer,
			Router: router,
		},
	}
}

// ListReservations will dial first to the intra AS colibri service to get the list of rsvs.
// It may dial two times more to two external AS colibri services, to get core and down
// rsvs lists.
func (c *DaemonClient) ListReservations(ctx context.Context, req *sdpb.ColibriListRequest) (
	*sdpb.ColibriListResponse, error) {

	if req == nil {
		return nil, serrors.New("bad nil request")
	}
	dstIA := addr.IAInt(req.Base.DstIa).IA()
	response := &sdpb.ColibriListResponse{
		Up:   make([]*colpb.ListResponse_Reservations_ReservationLooks, 0),
		Core: make([]*colpb.ListResponse_Reservations_ReservationLooks, 0),
		Down: make([]*colpb.ListResponse_Reservations_ReservationLooks, 0),
	}
	localIsdCores := make(map[addr.IA]struct{})
	farIsdCores := make(map[addr.IA]struct{})

	// to core ASes of the local ISD
	conn, err := c.Dialer.Dial(ctx, addr.SvcCOL)
	if err != nil {
		return nil, serrors.WrapStr("dialing intra AS colibri service", err)
	}
	response.Up, err = listRsvs(ctx, conn, &addr.IA{I: c.LocalIA.I, A: 0}, reservation.UpPath)
	if err != nil {
		return nil, err
	}
	for _, r := range response.Up {
		localIsdCores[addr.IAInt(r.DstIa).IA()] = struct{}{}
	}

	// TODO(juagargi) run all this in parallel with go routines
	// from core of local ISD to core of destination ISD:
	for ia := range localIsdCores {
		colSrvAddr, err := c.SrvResolver.ResolveColibriService(ctx, &ia)
		if err != nil {
			return nil, serrors.WrapStr("discovering colibri service", err, "ia", c.LocalIA.String())
		}
		conn, err = c.Dialer.Dial(ctx, colSrvAddr)
		if err != nil {
			return nil, serrors.WrapStr("dialing colibri service", err, "ia", c.LocalIA.String())
		}
		response.Core, err = listRsvs(ctx, conn, &addr.IA{I: ia.I, A: 0}, reservation.CorePath)
		if err != nil {
			return nil, err
		}
		for _, r := range response.Core {
			farIsdCores[addr.IAInt(r.DstIa).IA()] = struct{}{}
		}
	}
	// from core of destination ISD to final destination:
	for ia := range farIsdCores {
		colSrvAddr, err := c.SrvResolver.ResolveColibriService(ctx, &ia)
		if err != nil {
			return nil, serrors.WrapStr("discovering colibri service", err, "ia", c.LocalIA.String())
		}
		conn, err = c.Dialer.Dial(ctx, colSrvAddr)
		if err != nil {
			return nil, serrors.WrapStr("dialing colibri service", err, "ia", c.LocalIA.String())
		}
		response.Down, err = listRsvs(ctx, conn, &dstIA, reservation.DownPath)
		if err != nil {
			return nil, err
		}
	}

	return response, nil
}

func listRsvs(ctx context.Context, conn *grpc.ClientConn, dstIA *addr.IA,
	pathType reservation.PathType) ([]*colpb.ListResponse_Reservations_ReservationLooks, error) {

	client := colpb.NewColibriClient(conn)
	colReq := &colpb.ListRequest{
		DstIa:    uint64(dstIA.IAInt()),
		PathType: uint32(pathType),
	}
	colRes, err := client.ListReservations(ctx, colReq)
	if err != nil {
		return nil, serrors.WrapStr("rpc list_reservations", err, "ia", dstIA.String())
	}
	if fail, ok := colRes.SuccessFailure.(*colpb.ListResponse_FailureMessage); ok {
		err := fmt.Errorf(fail.FailureMessage)
		return nil, serrors.WrapStr("rpc list_reservations failure", err, "ia", dstIA.String())
	}
	return colRes.SuccessFailure.(*colpb.ListResponse_Reservations_).Reservations.Reservations, nil
}
