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

	"github.com/scionproto/scion/go/cs/reservation/translate"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/coliquic"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/serrors"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
	sdpb "github.com/scionproto/scion/go/pkg/proto/daemon"
)

type DaemonClient struct {
	Dialer coliquic.GRPCClientDialer
}

// ListReservations will dial to the intra AS colibri service to get the list of rsvs.
func (c *DaemonClient) ListReservations(ctx context.Context, req *sdpb.ColibriListRequest) (
	*sdpb.ColibriListResponse, error) {

	log.Info("deleteme about to dial colibri service", "req", req)
	if req == nil {
		return nil, serrors.New("bad nil request")
	}
	conn, err := c.Dialer.Dial(ctx, addr.SvcCOL)
	log.Info("deleteme dialed", "err", err)
	if err != nil {
		return nil, err
	}
	client := colpb.NewColibriClient(conn) // TODO(juagargi) cache the client
	response, err := client.ListStitchables(ctx, req.Base)
	log.Info("deleteme back from listing", "err", err)
	return &sdpb.ColibriListResponse{Base: response}, err
}

// SetupReservation will dial to the intra AS colibri service to setup an e2e reservation.
func (c *DaemonClient) SetupReservation(ctx context.Context, req *sdpb.ColibriSetupRequest) (
	*sdpb.ColibriSetupResponse, error) {

	log.Debug("setting up e2e reservation", "id", translate.ID(req.Base.Id))
	if req == nil {
		return nil, serrors.New("bad nil request")
	}
	conn, err := c.Dialer.Dial(ctx, addr.SvcCOL)
	log.Info("deleteme dialed", "err", err)
	if err != nil {
		return nil, err
	}
	client := colpb.NewColibriClient(conn) // TODO(juagargi) cache the client
	response, err := client.SetupReservation(ctx, req.Base)
	log.Info("deleteme back from setup reservation", "err", err)
	return &sdpb.ColibriSetupResponse{Base: response}, err
}
