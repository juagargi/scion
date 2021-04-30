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

	"google.golang.org/grpc"

	"github.com/scionproto/scion/go/cs/reservation"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/snet/squic"
	"github.com/scionproto/scion/go/lib/topology"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
)

// ServiceClientOperator can obtain COLIBRI gRPC clients to talk to the service.
// The goal of this construction is to avoid dialing more than once to the same destination,
// if we have dialed to it before. We would have to:
// - Ensure the QUIC ID on the channel is different for different channels.
// - Ensure the QUIC ID on the channel is the same for the same channel.
// - Ensure we return a gRPC client using the correct path (the path is used at the server to
//   measure the BW used by the services).
type ServiceClientOperator struct {
	connDialer  *squic.ConnDialer
	neighbors   map[uint16]*snet.SVCAddr // XXX(juagargi) this resolves to >1 UDPAddr per neighbor!
	initialized bool
	mutex       sync.Mutex
}

func NewServiceClientOperator(topo topology.Topology, router snet.Router,
	clientConn *squic.ConnDialer) (*ServiceClientOperator, error) {

	operator := &ServiceClientOperator{
		connDialer:  clientConn,
		neighbors:   make(map[uint16]*snet.SVCAddr, len(topo.InterfaceIDs())),
		initialized: false,
	}
	operator.initialize(topo, router)

	return operator, nil
}

// ColibriClient finds or creates a ColibriClient to be used for the path argument.
func (o *ServiceClientOperator) ColibriClient(ctx context.Context, path reservation.PacketPath) (
	colpb.ColibriClient, error) {

	o.mutex.Lock()
	defer o.mutex.Unlock()
	if !o.initialized {
		return nil, serrors.New("client operator not yet initialized",
			"neighbor_count", len(o.neighbors))
	}
	// get the remote address; the fields are the same with the exception of the path
	_, egressID, err := path.IngressEgressIFIDs()
	if err != nil {
		return nil, err
	}

	rAddr, ok := o.neighbors[egressID]
	if !ok {
		return nil, serrors.New("bad packet: no neighbor on specified egress", "egress", egressID)
	}
	// // prepare remote address with the new path
	// rAddr.Path.Type = path.Path().Type()
	// rAddr.Path.Raw = make([]byte, path.Path().Len())
	// if err = path.Path().SerializeTo(rAddr.Path.Raw); err != nil {
	// 	return nil, serrors.New("bac packet: cannot serialize path", "path", path)
	// }
	log.Error("DELETEME dialing", "addr", rAddr)
	// TODO(juagargi) replace with a single connection
	quicConn, err := o.connDialer.Dial(ctx, rAddr)
	if err != nil {
		return nil, err
	}
	dialer := func(context.Context, string) (net.Conn, error) {
		return quicConn, nil
	}
	conn, err := grpc.DialContext(ctx, rAddr.String(), grpc.WithInsecure(),
		grpc.WithContextDialer(dialer))
	if err != nil {
		return nil, err
	}
	return colpb.NewColibriClient(conn), nil
}

// initialize waits in the background until this operator can obtain paths to all the remaining IAs.
func (o *ServiceClientOperator) initialize(topo topology.Topology, router snet.Router) {

	remainingIAs := make(map[uint16]addr.IA)
	for _, name := range topo.BRNames() {
		brInfo, _ := topo.BR(name)
		for ifid, info := range brInfo.IFs {
			remainingIAs[uint16(ifid)] = info.IA
		}
	}
	go func() {
		defer log.HandlePanic()
		log.Info("will initialize colibri client operator", "neighbor_len", len(remainingIAs))
		o.mutex.Lock()
		defer o.mutex.Unlock()

		for len(remainingIAs) > 0 {
			time.Sleep(2 * time.Second)
			for egress, ia := range remainingIAs {
				path, err := router.Route(context.Background(), ia)
				if err != nil || path == nil {
					continue
				}
				// XXX(juagargi) this assumes we'll use the same endhost address for SvcCOL
				o.neighbors[egress] = &snet.SVCAddr{
					IA:      ia,
					Path:    path.Path(),
					NextHop: path.UnderlayNextHop(),
					SVC:     addr.SvcCOL,
				}
				delete(remainingIAs, egress)
			}
			log.Debug("colibri client operator initializing", "remaining", len(remainingIAs))
		}
		log.Info("colibri client operator initialization complete")
		o.initialized = true
	}()
}

// 2021-04-30 15:48:25.741503+0000 INFO
