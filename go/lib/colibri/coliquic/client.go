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

package coliquic

import (
	"context"
	"net"
	"sync"
	"time"

	base "github.com/scionproto/scion/go/cs/reservation"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/common"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/slayers/path/colibri"
	"github.com/scionproto/scion/go/lib/slayers/path/scion"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/topology"
	libgrpc "github.com/scionproto/scion/go/pkg/grpc"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
	dpb "github.com/scionproto/scion/go/pkg/proto/discovery"
)

type GRPCClientDialer interface {
	libgrpc.Dialer
}

// ServiceClientOperator can obtain COLIBRI gRPC clients to talk to the service.
// The goal of this construction is to avoid dialing more than once to the same destination,
// if we have dialed to it before. We would have to:
// - Ensure the QUIC ID on the channel is different for different channels.
// - Ensure the QUIC ID on the channel is the same for the same channel.
// - Ensure we return a gRPC client using the correct path (the path is used at the server to
//   measure the BW used by the services).
type ServiceClientOperator struct {
	connDialer  GRPCClientDialer
	neighbors   map[uint16]*snet.UDPAddr
	initialized bool
	srvResolver ColSrvResolver
	mutex       sync.Mutex
}

func NewServiceClientOperator(topo topology.Topology, router snet.Router,
	clientConn GRPCClientDialer) (*ServiceClientOperator, error) {

	operator := &ServiceClientOperator{
		connDialer:  clientConn,
		neighbors:   make(map[uint16]*snet.UDPAddr, len(topo.InterfaceIDs())),
		initialized: false,
		srvResolver: &DiscoveryColSrvRes{
			Router: router,
			Dialer: clientConn,
		},
	}
	operator.initialize(topo)

	return operator, nil
}

// ColibriClient finds or creates a ColibriClient to be used for the path argument.
func (o *ServiceClientOperator) ColibriClient(ctx context.Context, transp *base.TransparentPath) (
	colpb.ColibriClient, error) {

	o.mutex.Lock()
	defer o.mutex.Unlock()

	if !o.initialized {
		return nil, serrors.New("client operator not yet initialized",
			"neighbor_count", len(o.neighbors))
	}

	egressID := transp.Steps[transp.CurrentStep].Egress
	spath := transp.Spath
	rAddr, ok := o.neighbors[egressID]
	if !ok {
		return nil, serrors.New("bad packet: no neighbor on specified egress", "egress", egressID)
	}
	rAddr = rAddr.Copy() // preserve the original data

	// prepare remote address with the new path
	switch spath.Type {
	case scion.PathType: // don't touch the service path
	case colibri.PathType:
		// TODO(juagargi): reactivate use of reservations for control traffic
		// // replace the service path with the colibri one.
		// The source must also be the original one
		// rAddr.Path = spath.Copy()
		// rAddr.IA = transp.SrcIA()
		// TODO(juagargi) check if the colibri path is expired, and don't use it in that case
	}

	log.Info("DELETEME dialing", "addr", rAddr)
	conn, err := o.connDialer.Dial(ctx, rAddr)
	if err != nil {
		log.Debug("error dialing a grpc connection", "addr", rAddr, "err", err)
		return nil, err
	}
	return colpb.NewColibriClient(conn), nil
}

// initialize waits in the background until this operator can obtain paths to all the remaining IAs.
func (o *ServiceClientOperator) initialize(topo topology.Topology) {

	remainingIAs := neighbors(topo)
	go func() {
		defer log.HandlePanic()
		log.Info("will initialize colibri client operator", "neighbor_count", len(remainingIAs))
		o.mutex.Lock()
		defer o.mutex.Unlock()

		for len(remainingIAs) > 0 {
			time.Sleep(2 * time.Second)
			log.Debug("colibri client operator initializing", "remaining", len(remainingIAs))
			remainingIAs = o.findNeighbors(o.neighbors, remainingIAs)
		}
		log.Info("colibri client operator initialization complete")
		o.initialized = true
		go func() {
			defer log.HandlePanic()
			o.periodicResolveNeighbors(topo)
		}()
	}()
}

// periodicResolveNeighbors scans the topology and gets new paths for the neighbors.
func (o *ServiceClientOperator) periodicResolveNeighbors(topo topology.Topology) {

	neighbors := neighbors(topo)
	for {
		time.Sleep(15 * time.Minute)
		log.Debug("colibri client operator periodically findind neighbors",
			"count", len(neighbors))
		newAddrBook := make(map[uint16]*snet.UDPAddr)
		_ = o.findNeighbors(newAddrBook, neighbors)
		log.Info("deleteme PERIODIC neighbor find", "found_count", len(newAddrBook))
		o.mutex.Lock()
		o.neighbors = newAddrBook
		o.mutex.Unlock()
	}
}

// neighbors returns the neighboring IAs by egress interface ID.
func neighbors(topo topology.Topology) map[uint16]addr.IA {
	neighbors := make(map[uint16]addr.IA)
	for _, name := range topo.BRNames() {
		brInfo, _ := topo.BR(name)
		for ifid, info := range brInfo.IFs {
			neighbors[uint16(ifid)] = info.IA
		}
	}
	return neighbors
}

// findNeighbors sets the address of the neighbors in the addrBook parameter.
// Returns the neighbors for which it could not find an address.
func (o *ServiceClientOperator) findNeighbors(addrBook map[uint16]*snet.UDPAddr,
	neighbors map[uint16]addr.IA) map[uint16]addr.IA {

	missingNeighbors := make(map[uint16]addr.IA)
	for egress, ia := range neighbors {
		colAddr, err := o.resolveAddr(&ia)
		if err != nil {
			log.Debug("error resolving address for colibri service", "err", err)
			missingNeighbors[egress] = ia
			continue
		}
		log.Info("deleteme findNeighbors", "ia", ia.String(), "addr", colAddr.String())
		addrBook[egress] = colAddr
	}
	return missingNeighbors
}

func (o *ServiceClientOperator) resolveAddr(ia *addr.IA) (
	*snet.UDPAddr, error) {

	ctx, cancelCtx := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelCtx()

	return o.srvResolver.ResolveColibriService(ctx, ia)
}

type ColSrvResolver interface {
	ResolveColibriService(ctx context.Context, ia *addr.IA) (*snet.UDPAddr, error)
}

type AnycastColSrvRes struct {
	Router snet.Router
	Arw    libgrpc.AddressRewriter
}

func (r *AnycastColSrvRes) ResolveColibriService(ctx context.Context, ia *addr.IA) (
	*snet.UDPAddr, error) {

	path, err := r.Router.Route(context.Background(), *ia)
	if err != nil || path == nil {
		return nil, serrors.New("no route to IA", "ia", ia, "err", err, "path", path)
	}

	svcAddr := &snet.SVCAddr{
		IA:      *ia,
		Path:    path.Path(),
		NextHop: path.UnderlayNextHop(),
		SVC:     addr.SvcCOL,
	}
	quicAddr, ok, err := r.Arw.RedirectToQUIC(ctx, svcAddr)
	if !ok || err != nil {
		return nil, serrors.New("cannot resolve service", "svc", svcAddr.SVC, "err", err)
	}
	if _, ok := quicAddr.(*snet.UDPAddr); !ok {
		return nil, serrors.New("resolved address is not snet.UDPAddr", "addr", quicAddr,
			"type", common.TypeOf(quicAddr))
	}
	return &snet.UDPAddr{ // TODO(juagargi) should be a SVCAddr instead
		IA:      *ia,
		Path:    path.Path(),
		NextHop: path.UnderlayNextHop(),
		Host:    quicAddr.(*snet.UDPAddr).Host,
	}, nil
}

type DiscoveryColSrvRes struct {
	Router snet.Router
	Dialer libgrpc.Dialer
}

func (r *DiscoveryColSrvRes) ResolveColibriService(ctx context.Context, ia *addr.IA) (
	*snet.UDPAddr, error) {

	path, err := r.Router.Route(context.Background(), *ia)
	if err != nil || path == nil {
		return nil, serrors.New("no route to IA", "ia", ia, "err", err, "path", path)
	}

	ds := &snet.SVCAddr{
		IA:      *ia,
		Path:    path.Path(),
		NextHop: path.UnderlayNextHop(),
		SVC:     addr.SvcDS,
	}
	conn, err := r.Dialer.Dial(ctx, ds)
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	client := dpb.NewDiscoveryServiceClient(conn)
	rep, err := client.ColibriServices(ctx, &dpb.ColibriServicesRequest{}, libgrpc.RetryProfile...)
	if err != nil {
		return nil, serrors.WrapStr("discovering colibri services", err)
	}
	if len(rep.Address) == 0 {
		return nil, serrors.New("no colibri services discovered", "ia", ia.String())
	}

	host, err := net.ResolveUDPAddr("udp", rep.Address[0])
	if err != nil {
		return nil, serrors.WrapStr("parsing udp address for colibri service", err,
			"udp", rep.Address[0])
	}

	return &snet.UDPAddr{ // TODO(juagargi) should be a SVCAddr instead
		IA:      *ia,
		Path:    path.Path(),
		NextHop: path.UnderlayNextHop(),
		Host:    host,
	}, nil
}
