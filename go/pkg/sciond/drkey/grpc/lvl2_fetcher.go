// Copyright 2020 ETH Zurich
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
	"time"

	"github.com/golang/protobuf/ptypes"

	"github.com/scionproto/scion/go/lib/addr"
	ctrl "github.com/scionproto/scion/go/lib/ctrl/drkey"
	"github.com/scionproto/scion/go/lib/drkey"
	"github.com/scionproto/scion/go/lib/scrypto/cppki"
	"github.com/scionproto/scion/go/lib/serrors"
	sc_grpc "github.com/scionproto/scion/go/pkg/grpc"
	"github.com/scionproto/scion/go/pkg/proto/daemon"
	dkpb "github.com/scionproto/scion/go/pkg/proto/drkey"
	sd_drkey "github.com/scionproto/scion/go/pkg/sciond/drkey"
	"github.com/scionproto/scion/go/pkg/trust"
)

// DRKeyFetcher obtains Lvl2 DRKey from the local CS.
type DRKeyFetcher struct {
	Dialer sc_grpc.Dialer
	Router trust.Router
}

var _ sd_drkey.Fetcher = (*DRKeyFetcher)(nil)

// GetDRKeyLvl2 fetches the Lvl2Key corresponding to the metadata by requesting
// the CS.
func (f DRKeyFetcher) GetDRKeyLvl2(ctx context.Context, lvl2meta drkey.Lvl2Meta,
	dstIA addr.IA, valTime time.Time) (drkey.Lvl2Key, error) {

	csServer, err := f.Router.ChooseServer(ctx, dstIA.I)
	if err != nil {
		return drkey.Lvl2Key{}, serrors.WrapStr("choosing server", err)
	}

	// grpc.DialContext, using credentials +  remote addr.
	conn, err := f.Dialer.Dial(ctx, csServer)
	if err != nil {
		return drkey.Lvl2Key{}, serrors.WrapStr("dialing", err)
	}
	defer conn.Close()
	client := daemon.NewDaemonServiceClient(conn)
	lvl2req := ctrl.NewLvl2ReqFromMeta(lvl2meta, valTime)
	req, err := lvl2reqToProtoRequest(lvl2req)
	if err != nil {
		return drkey.Lvl2Key{},
			serrors.WrapStr("parsing lvl2 request to protobuf", err)
	}
	rep, err := client.DRKeyLvl2(ctx, req)
	if err != nil {
		return drkey.Lvl2Key{}, serrors.WrapStr("requesting level 2 key", err)
	}

	lvl2Key, err := getLvl2KeyFromReply(rep, lvl2meta)
	if err != nil {
		return drkey.Lvl2Key{}, serrors.WrapStr("obtaining level 2 key from reply", err)
	}

	return lvl2Key, nil
}

func lvl2reqToProtoRequest(req ctrl.Lvl2Req) (*dkpb.DRKeyLvl2Request, error) {
	valTime, err := ptypes.TimestampProto(req.ValTime)
	if err != nil {
		return nil, err
	}
	return &dkpb.DRKeyLvl2Request{
		Protocol: req.Protocol,
		ReqType:  req.ReqType,
		Dst_IA:   uint64(req.DstIA.IAInt()),
		Src_IA:   uint64(req.DstIA.IAInt()),
		ValTime:  valTime,
		SrcHost: &dkpb.DRKeyLvl2Request_DRKeyHost{
			Type: uint32(req.SrcHost.Type),
			Host: req.SrcHost.Host,
		},
		DstHost: &dkpb.DRKeyLvl2Request_DRKeyHost{
			Type: uint32(req.DstHost.Type),
			Host: req.DstHost.Host,
		},
	}, nil
}

// getLvl2KeyFromReply decrypts and extracts the level 1 drkey from the reply.
func getLvl2KeyFromReply(rep *dkpb.DRKeyLvl2Response, meta drkey.Lvl2Meta) (drkey.Lvl2Key, error) {

	epochBegin, err := ptypes.Timestamp(rep.EpochBegin)
	if err != nil {
		return drkey.Lvl2Key{}, err
	}
	epochEnd, err := ptypes.Timestamp(rep.EpochEnd)
	if err != nil {
		return drkey.Lvl2Key{}, err
	}
	epoch := drkey.Epoch{
		Validity: cppki.Validity{
			NotBefore: epochBegin,
			NotAfter:  epochEnd,
		},
	}
	return drkey.Lvl2Key{
		Lvl2Meta: drkey.Lvl2Meta{
			KeyType:  meta.KeyType,
			Protocol: meta.Protocol,
			SrcIA:    meta.SrcIA,
			DstIA:    meta.DstIA,
			SrcHost:  meta.SrcHost,
			DstHost:  meta.DstHost,
			Epoch:    epoch,
		},
		Key: drkey.DRKey(rep.Drkey),
	}, nil
}
