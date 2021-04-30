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

	"github.com/scionproto/scion/go/lib/colibri/coliquic"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
)

type ColibriService struct {
	MyAddr    *snet.UDPAddr
	Neighbors map[uint16]*snet.UDPAddr // egress ID to neighbor
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
