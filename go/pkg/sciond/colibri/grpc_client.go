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

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/pkg/grpc"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
	sdpb "github.com/scionproto/scion/go/pkg/proto/daemon"
)

type DaemonClient struct {
	Dialer grpc.Dialer
}

func (c *DaemonClient) ListReservations(ctx context.Context, req *sdpb.ColibriListRequest) (
	*sdpb.ColibriListResponse, error) {

	if req == nil {
		return nil, serrors.New("bad nil request")
	}

	// //
	// // deleteme
	// //
	// log.Info("deleteme DaemonClient 2")
	// conn1, err := c.Dialer.Dial(ctx, addr.SvcCS)
	// log.Info("deleteme DaemonClient 3", "err", err)
	// if err != nil {
	// 	return nil, serrors.WrapStr("deleteme error dialing", err)
	// }
	// deletemeclient := deletemepb.NewSegmentLookupServiceClient(conn1)
	// deletemeReq := &deletemepb.SegmentsRequest{
	// 	SrcIsdAs: req.Base.DstIa + 1,
	// 	DstIsdAs: req.Base.DstIa,
	// }
	// res, err := deletemeclient.Segments(ctx, deletemeReq)
	// if err != nil {
	// 	return nil, serrors.WrapStr("deleteme error requesting segments", err)
	// }
	// log.Info("deleteme, segments", "res", res.Segments)
	// //
	// //

	conn, err := c.Dialer.Dial(ctx, addr.SvcCOL)
	if err != nil {
		return nil, serrors.WrapStr("dialing daemon", err)
	}
	client := colpb.NewColibriClient(conn)
	colReq := &colpb.ListRequest{
		DstIa: req.Base.DstIa,
	}
	colRes, err := client.ListReservations(ctx, colReq)
	if err != nil {
		return nil, serrors.WrapStr("rpc list_reservations", err)
	}
	return &sdpb.ColibriListResponse{
		Base: colRes,
	}, nil
}
