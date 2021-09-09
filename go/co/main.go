// Copyright 2020 Anapaya Systems
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

package main

import (
	"context"
	"path/filepath"
	"time"

	"google.golang.org/grpc"

	coli_conf "github.com/scionproto/scion/go/co/reservation/conf"
	admission "github.com/scionproto/scion/go/co/reservation/segment/admission/stateless"
	"github.com/scionproto/scion/go/co/reservationstorage"
	"github.com/scionproto/scion/go/co/reservationstore"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/coliquic"
	"github.com/scionproto/scion/go/lib/fatal"
	"github.com/scionproto/scion/go/lib/infra/infraenv"
	"github.com/scionproto/scion/go/lib/infra/modules/itopo"
	"github.com/scionproto/scion/go/lib/keyconf"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/periodic"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/topology"
	"github.com/scionproto/scion/go/pkg/app/launcher"
	colgrpc "github.com/scionproto/scion/go/pkg/co/colibri/grpc"
	"github.com/scionproto/scion/go/pkg/colibri/config"
	libgrpc "github.com/scionproto/scion/go/pkg/grpc"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
	"github.com/scionproto/scion/go/pkg/storage"
)

func main() {
	var cfg config.Config
	application := launcher.Application{
		TOMLConfig: &cfg,
		ShortName:  "SCION COLIBRI Service",
		Main: func(ctx context.Context) error {
			return realMain(ctx, &cfg)
		},
	}
	application.Run()
}

func realMain(ctx context.Context, cfg *config.Config) error {
	cfgObjs, err := setup(ctx, cfg)
	if err != nil {
		return err
	}

	manager, err := setupColibri(cfg, cfgObjs)
	if err != nil {
		return err
	}
	defer manager.Kill()

	select {
	case <-fatal.ShutdownChan():
		// Whenever we receive a SIGINT or SIGTERM we exit without an error.
		// Deferred shutdowns for all running servers run now.
		return nil
	case <-fatal.FatalChan():
		return serrors.New("shutdown on error")
	}
}

// cfgObjs contains the objects needed for the confinguration of colibri.
type cfgObjs struct {
	masterKey keyconf.Master
	stack     *coliquic.ServerStack
}

func setup(ctx context.Context, cfg *config.Config) (*cfgObjs, error) {
	topo, err := topology.FromJSONFile(cfg.General.Topology())
	if err != nil {
		return nil, serrors.WrapStr("loading topology", err)
	}
	itopo.Init(&itopo.Config{
		ID:  cfg.General.ID,
		Svc: topology.Colibri,
	})
	if err := itopo.Update(topo); err != nil {
		return nil, serrors.WrapStr("unable to set initial static topology", err)
	}
	infraenv.InitInfraEnvironment(cfg.General.Topology())

	cfgObjs, err := setupNetwork(ctx, cfg)
	if err != nil {
		return cfgObjs, serrors.WrapStr("setting network config", err)
	}

	cfgObjs.masterKey, err = keyconf.LoadMaster(filepath.Join(cfg.General.ConfigDir, "keys"))
	if err != nil {
		return nil, serrors.WrapStr("error getting master secret", err)
	}

	return cfgObjs, nil
}

func setupNetwork(ctx context.Context, cfg *config.Config) (*cfgObjs, error) {

	topo := itopo.Get()
	serverAddr := &snet.UDPAddr{
		IA:   topo.IA(),
		Host: topo.PublicAddress(addr.SvcCOL, cfg.General.ID),
	}

	stack, err := coliquic.NewServerStack(ctx, serverAddr, cfg.Daemon.Address)
	if err != nil {
		return nil, serrors.WrapStr("initializing server stack", err)
	}

	return &cfgObjs{
		stack: stack,
	}, nil
}

// setupColibri returns the running manager.
func setupColibri(cfg *config.Config, cfgObjs *cfgObjs) (*periodic.Runner, error) {
	db, err := storage.NewColibriStorage(cfg.Colibri.DB)
	if err != nil {
		return nil, serrors.WrapStr("error initializing COLIBRI DB", err)
	}

	admitter := &admission.StatelessAdmission{
		Caps:  cfg.Colibri.Capacities,
		Delta: cfg.Colibri.Delta,
	}
	colibriStore, err := reservationstore.NewStore(itopo.Get(), cfgObjs.stack.Router, cfgObjs.stack.Dialer,
		db, admitter, cfgObjs.masterKey.Key0)
	if err != nil {
		return nil, serrors.WrapStr("initializing colibri store", err)
	}

	colibriService := &colgrpc.ColibriService{
		Store: colibriStore,
	}
	colServer := coliquic.NewGrpcServer(libgrpc.UnaryServerInterceptor())
	tcpColServer := grpc.NewServer(libgrpc.UnaryServerInterceptor())
	colpb.RegisterColibriServer(colServer, colibriService)
	colpb.RegisterColibriServer(tcpColServer, colibriService)

	// run inter and intra AS servers
	topo := itopo.Get()
	go func() {
		defer log.HandlePanic()
		lis := cfgObjs.stack.QUICListener
		log.Debug("colibri grpc server listening quic", "addr", lis.Addr())
		if err := colServer.Serve(lis); err != nil {
			fatal.Fatal(err)
		}
	}()
	go func() {
		defer log.HandlePanic()
		tcpListener := cfgObjs.stack.TCPListener
		log.Debug("colibri grpc server listening tcp", "tcp_addr", tcpListener.Addr())
		if err := tcpColServer.Serve(tcpListener); err != nil {
			fatal.Fatal(err)
		}
	}()

	manager, err := colibriManager(topo, cfgObjs.stack.Router, colibriStore, cfg.Colibri.Reservations)
	if err != nil {
		return nil, serrors.WrapStr("starting colibri manager", err)
	}

	return manager, nil
}

func colibriManager(topo topology.Topology, router snet.Router, store reservationstorage.Store,
	initialRsvs *coli_conf.Reservations) (*periodic.Runner, error) {

	if store == nil {
		return nil, nil
	}
	mgr, err := reservationstore.NewColibriManager(topo.IA(), router, store, initialRsvs)
	if err != nil {
		return nil, serrors.WrapStr("could not start colibri manager", err)
	}
	return periodic.Start(mgr, 100*time.Millisecond, 5*time.Second), nil
}
