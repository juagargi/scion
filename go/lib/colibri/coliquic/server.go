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

// Package coliquic implements QUIC on top of COLIBRI.
// Inspired on squic.
package coliquic

import (
	"bytes"
	"context"
	"net"
	"sync"

	"github.com/lucas-clemente/quic-go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/stats"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/infra/infraenv"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/slayers/path/colibri"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/snet/squic"
	"github.com/scionproto/scion/go/lib/sock/reliable"
	"github.com/scionproto/scion/go/lib/topology"
)

// GetColibriPath returns the (last) COLIBRI path used with this quic Session, or nil if none.
func GetColibriPath(session quic.Session) (*colibri.ColibriPath, error) {
	// TODO(juagargi) currently, the same session can receive packets from multitude of
	// COLIBRI paths (or non colibri), which should not be allowed. To enforce that the limits
	// of the reservation are respected, only one colibri path must be allowed thru the
	// life of the session. For now we assume no malicious parties.
	var colPath *colibri.ColibriPath
	netAddr := session.RemoteAddr()
	addr, _ := netAddr.(*snet.UDPAddr)
	if addr != nil && addr.Path.Type == colibri.PathType {
		colPath = new(colibri.ColibriPath)
		if err := colPath.DecodeFromBytes(addr.Path.Raw); err != nil {
			return nil, err
		}
	}
	return colPath, nil
}

func ColibriListener(topo topology.Topology) (net.Listener, error) {
	// as seen in NetworkConfig.initQUICSockets:
	dispatcherService := reliable.NewDispatcher("")
	serverNet := &snet.SCIONNetwork{
		LocalIA: topo.IA(),
		Dispatcher: &snet.DefaultPacketDispatcherService{
			Dispatcher:  dispatcherService,
			SCMPHandler: ignoreSCMP{},
		},
	}
	// topo.PublicAddress(addr.SvcCS, cfg.General.ID)
	serverAddr, err := topo.Anycast(addr.SvcCS)
	// TODO(juagargi) should find the PublicAddress of SvcCOL
	// TODO(juagargi) read it from topo file and pass it along
	// serverAddr, err := net.ResolveUDPAddr("udp", "localhost:4321")
	if err != nil {
		return nil, err
	}
	serverAddr.Port = 4321
	log.Info("deleteme deleteme server address will be", "addr", serverAddr)
	packetConn, err := serverNet.Listen(context.Background(), "udp", serverAddr, addr.SvcCOL)
	// packetConn, err := serverNet.Listen(context.Background(), "udp", serverAddr, addr.SvcNone)
	if err != nil {
		return nil, err
	}
	tlsConfig, err := infraenv.GenerateTLSConfig()
	if err != nil {
		return nil, err
	}
	quicListener, err := quic.Listen(packetConn, tlsConfig, nil)
	if err != nil {
		return nil, err
	}
	return squic.NewConnListener(quicListener), nil
}

type ignoreSCMP struct{}

func (ignoreSCMP) Handle(pkt *snet.Packet) error {
	// Always reattempt reads from the socket.
	return nil
}

func NewConnListener(listener quic.Listener) net.Listener {
	// TODO(juagargi) check squic.NewConnListener as it has weird error semantics for its Accept()
	return squic.NewConnListener(listener)
}

func NewGrpcServer(opt ...grpc.ServerOption) *grpc.Server {
	h := &statsHandler{
		usage: make(map[string]uint64),
	}
	opts := append(opt, grpc.StatsHandler(h))
	return grpc.NewServer(opts...)
}

// UsageFromContext returns a bool saying if this peer was using colibri and
// an approximation of the bandwidth used in that case.
// the peer was not COLIBRI.
func UsageFromContext(ctx context.Context) (usage uint64, isColibri bool, err error) {
	// the context has a pointer to the statsHandler
	var handler *statsHandler
	if sh := ctx.Value(statsHandlerKey{}); sh != nil {
		handler = sh.(*statsHandler)
	}
	if handler == nil {

		err = serrors.New("could not retrieve handler from context",
			"raw_handler", ctx.Value(statsHandlerKey{}))
		return
	}
	peer, ok := peer.FromContext(ctx)
	if !ok {
		err = serrors.New("could not retrieve peer from context")
		return
	}

	if peer.Addr.(*snet.UDPAddr) != nil && peer.Addr.(*snet.UDPAddr).Path.Type == colibri.PathType {
		isColibri = true
		usage, ok = handler.popUsage(peer.Addr.(*snet.UDPAddr).Path.Raw)
		if !ok {
			err = serrors.New("could not retrieve this peer from stats handler",
				"peer", peer.Addr)
			isColibri = false
			return
		}
	}
	return
}

// statsHandlerKey is used as key inside context to store the pointer to its statsHandler.
type statsHandlerKey struct{}

// bandwidthKey used to store and retrieve the bandwidth used by a gRPC call.
type bandwidthKey struct{}

type statsHandler struct {
	usage map[string]uint64 // map of raw path to usage, incremented on each request
	m     sync.Mutex
}

func (h *statsHandler) TagRPC(ctx context.Context, info *stats.RPCTagInfo) context.Context {
	return ctx
}

func (h *statsHandler) HandleRPC(ctx context.Context, st stats.RPCStats) {
	logger := log.FromCtx(ctx)
	peer, ok := peer.FromContext((ctx))
	if !ok || peer.Addr.(*snet.UDPAddr) == nil ||
		peer.Addr.(*snet.UDPAddr).Path.Type != colibri.PathType {
		// not a SCION family address
		return
	}
	// internal checks: we should have stored in the ct the peer address and the pointer to h
	prev := ctx.Value(bandwidthKey{})
	if prev == nil || prev.(*snet.UDPAddr) == nil ||
		!bytes.Equal(prev.(*snet.UDPAddr).Path.Raw, peer.Addr.(*snet.UDPAddr).Path.Raw) {
		logger.Error("value from context not matching stats handler",
			"ctx_value", prev, "peer_addr", peer.Addr)
		return
	}
	handlerCtx := ctx.Value(statsHandlerKey{})
	if handlerCtx == nil || handlerCtx != h {
		logger.Error("handler from context not matching stats handler",
			"ctx_handler", handlerCtx, "handler", h, "peer", peer.Addr)
		return
	}
	// estimate the size from the statistics
	var size int
	switch s := st.(type) {
	case *stats.Begin:
	case *stats.End:
	case *stats.OutHeader: // for some reason there's no length in it
	case *stats.InHeader:
		size = s.WireLength
	case *stats.InPayload:
		size = s.WireLength
	case *stats.InTrailer:
		size = s.WireLength
	case *stats.OutPayload:
		size = s.WireLength
	case *stats.OutTrailer:
		// this case is special: its wirelength is deprecated and never set
	default:
		logger.Error("unknown stats type", "type", common.TypeOf(st))
		return
	}
	if size < 0 {
		logger.Error("stats size is negative", "size", size, "peer", peer.Addr)
		return
	}
	h.addUsage(prev.(*snet.UDPAddr).Path.Raw, uint64(size))
}

func (h *statsHandler) TagConn(ctx context.Context, info *stats.ConnTagInfo) context.Context {
	if info.RemoteAddr.(*snet.UDPAddr) != nil &&
		info.RemoteAddr.(*snet.UDPAddr).Path.Type == colibri.PathType {

		addr := info.RemoteAddr.(*snet.UDPAddr)
		h.addUsage(addr.Path.Raw, 0)
		// store a pointer to this stats handler in the context so that we can retrieve the
		// usage from the service calls themselves.
		ctx = context.WithValue(ctx, statsHandlerKey{}, h)
		// and for this context, add the address (should always match that of the peer)
		ctx = context.WithValue(ctx, bandwidthKey{}, addr)
	}
	return ctx
}

func (h *statsHandler) HandleConn(ctx context.Context, st stats.ConnStats) {}

// popUsage returns the usage and the found indicator for a raw path in this statsHandler.
// if found, it will be removed.
// The call is intended to be used by one gRPC service call before its end.
func (h *statsHandler) popUsage(rawPath []byte) (uint64, bool) {
	key := string(rawPath)
	h.m.Lock()
	defer h.m.Unlock()
	usage, found := h.usage[key]
	if found {
		delete(h.usage, key)
	}
	return usage, found
}

func (h *statsHandler) addUsage(rawColibriPath []byte, usage uint64) {
	h.m.Lock()
	defer h.m.Unlock()
	h.usage[string(rawColibriPath)] += usage
}
