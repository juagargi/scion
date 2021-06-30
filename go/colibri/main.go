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
	"net"
	"path/filepath"
	"time"

	"google.golang.org/grpc"

	"github.com/scionproto/scion/go/cs/config"
	coli_conf "github.com/scionproto/scion/go/cs/reservation/conf"
	admission "github.com/scionproto/scion/go/cs/reservation/segment/admission/stateless"
	"github.com/scionproto/scion/go/cs/reservationstorage"
	"github.com/scionproto/scion/go/cs/reservationstore"
	"github.com/scionproto/scion/go/cs/segreq"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/coliquic"
	"github.com/scionproto/scion/go/lib/fatal"
	"github.com/scionproto/scion/go/lib/infra/infraenv"
	"github.com/scionproto/scion/go/lib/infra/messenger"
	"github.com/scionproto/scion/go/lib/infra/modules/itopo"
	segfetchergrpc "github.com/scionproto/scion/go/lib/infra/modules/segfetcher/grpc"
	"github.com/scionproto/scion/go/lib/keyconf"
	"github.com/scionproto/scion/go/lib/log"
	"github.com/scionproto/scion/go/lib/pathdb"
	"github.com/scionproto/scion/go/lib/periodic"
	"github.com/scionproto/scion/go/lib/revcache"
	"github.com/scionproto/scion/go/lib/serrors"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/topology"
	"github.com/scionproto/scion/go/pkg/app/launcher"
	"github.com/scionproto/scion/go/pkg/cs"
	colgrpc "github.com/scionproto/scion/go/pkg/cs/colibri/grpc"
	libgrpc "github.com/scionproto/scion/go/pkg/grpc"
	colpb "github.com/scionproto/scion/go/pkg/proto/colibri"
	"github.com/scionproto/scion/go/pkg/storage"
	"github.com/scionproto/scion/go/pkg/trust"
	"github.com/scionproto/scion/go/pkg/trust/compat"
	trustgrpc "github.com/scionproto/scion/go/pkg/trust/grpc"
)

func main() {
	var cfg config.Config
	application := launcher.Application{
		TOMLConfig: &cfg,
		ShortName:  "SCION COLIBRI Service",
		Main: func() error {
			return realMain(&cfg)
		},
	}
	application.Run()
}

func realMain(cfg *config.Config) error {

	cfgObjs, err := setup(cfg)
	if err != nil {
		return err
	}
	defer cfgObjs.closeFcn()

	manager, err := setupColibri(&cfg.Colibri, cfgObjs)
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

type cfgObjs struct {
	masterKey keyconf.Master
	revCache  revcache.RevCache
	pathDB    pathdb.PathDB
	trustDB   storage.TrustDB
	nc        *infraenv.NetworkConfig
	quicStack *infraenv.QUICStack
	tcpStack  net.Listener
	dialer    *libgrpc.QUICDialer
	router    snet.Router
	closeFcn  func() // defer a call to this function
}

func setup(cfg *config.Config) (*cfgObjs, error) {
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

	cfgObjs, err := setupNetwork(cfg)
	if err != nil {
		return cfgObjs, serrors.WrapStr("setting network config", err)
	}

	cfgObjs.masterKey, err = keyconf.LoadMaster(filepath.Join(cfg.General.ConfigDir, "keys"))
	if err != nil {
		return nil, serrors.WrapStr("error getting master secret", err)
	}

	return cfgObjs, nil
}

func setupNetwork(cfg *config.Config) (*cfgObjs, error) {
	revCache := storage.NewRevocationStorage()
	pathDB, err := storage.NewPathStorage(cfg.PathDB)
	if err != nil {
		return nil, serrors.WrapStr("initializing path storage", err)
	}
	pathDB = pathdb.WithMetrics(string(storage.BackendSqlite), pathDB)

	trustDB, err := storage.NewTrustStorage(cfg.TrustDB)
	if err != nil {
		return nil, serrors.WrapStr("initializing trust storage", err)
	}

	topo := itopo.Get()
	nc := &infraenv.NetworkConfig{
		IA:                    topo.IA(),
		Public:                topo.PublicAddress(addr.SvcCOL, cfg.General.ID),
		ReconnectToDispatcher: cfg.General.ReconnectToDispatcher,
		QUIC: infraenv.QUIC{
			Address: cfg.QUIC.Address,
		},
		SVCRouter: messenger.NewSVCRouter(itopo.Provider()),
		SCMPHandler: snet.DefaultSCMPHandler{
			RevocationHandler: cs.RevocationHandler{RevCache: revCache},
		},
	}
	quicStack, err := nc.QUICStack()
	if err != nil {
		return nil, serrors.WrapStr("initializing QUIC stack", err)
	}
	tcpStack, err := nc.TCPStack()
	if err != nil {
		return nil, serrors.WrapStr("initializing TCP stack", err)
	}

	dialer := &libgrpc.QUICDialer{
		Rewriter: nc.AddressRewriter(nil),
		Dialer:   quicStack.Dialer,
	}

	cfgObjs := &cfgObjs{
		revCache:  revCache,
		pathDB:    pathDB,
		trustDB:   trustDB,
		nc:        nc,
		quicStack: quicStack,
		tcpStack:  tcpStack,
		dialer:    dialer,
		closeFcn: func() {
			defer revCache.Close()
			defer quicStack.RedirectCloser()
			defer pathDB.Close()
		},
	}

	return withRouter(cfg, cfgObjs)
}

func withRouter(cfg *config.Config, cfgObjs *cfgObjs) (*cfgObjs, error) {

	topo := itopo.Get()
	trustengineCache := cfg.TrustEngine.Cache.New()
	inspector := trust.CachingInspector{
		Inspector: trust.DBInspector{
			DB: cfgObjs.trustDB,
		},
		// CacheHits:          cacheHits,
		MaxCacheExpiration: cfg.TrustEngine.Cache.Expiration,
		Cache:              trustengineCache,
	}
	provider := trust.FetchingProvider{
		DB: cfgObjs.trustDB,
		Fetcher: trustgrpc.Fetcher{
			IA:     topo.IA(),
			Dialer: cfgObjs.dialer,
			// Requests: libmetrics.NewPromCounter(trustmetrics.RPC.Fetches),
		},
		Recurser: trust.ASLocalRecurser{IA: topo.IA()},
	}
	verifier := compat.Verifier{
		Verifier: trust.Verifier{
			Engine: provider,
			// CacheHits:          cacheHits,
			MaxCacheExpiration: cfg.TrustEngine.Cache.Expiration,
			Cache:              trustengineCache,
		},
	}

	fetcherCfg := segreq.FetcherConfig{
		IA:            itopo.Get().IA(),
		PathDB:        cfgObjs.pathDB,
		RevCache:      cfgObjs.revCache,
		QueryInterval: cfg.PS.QueryInterval.Duration,
		RPC: &segfetchergrpc.Requester{
			Dialer: cfgObjs.dialer,
		},
		Inspector:    inspector,
		TopoProvider: itopo.Provider(),
		Verifier:     verifier,
	}

	cfgObjs.router = segreq.NewRouter(fetcherCfg)
	provider.Router = trust.AuthRouter{
		ISD:    topo.IA().I,
		DB:     cfgObjs.trustDB,
		Router: cfgObjs.router,
	}
	return cfgObjs, nil
}

// setupColibri returns the running manager.
func setupColibri(cfg *config.ColibriConfig, cfgObjs *cfgObjs) (*periodic.Runner, error) {
	db, err := storage.NewColibriStorage(cfg.DB)
	if err != nil {
		return nil, serrors.WrapStr("error initializing COLIBRI DB", err)
	}

	admitter := &admission.StatelessAdmission{
		Caps:  cfg.Capacities,
		Delta: cfg.Delta,
	}
	// colDialer := &libgrpc.QUICDialer{
	// 	Rewriter: cfgObjs.nc.AddressRewriter(nil),
	// 	Dialer:   cfgObjs.quicStack.Dialer,
	// }

	colibriStore, err := reservationstore.NewStore(itopo.Get(), cfgObjs.router, cfgObjs.nc.AddressRewriter(nil),
		cfgObjs.dialer, db, admitter, cfgObjs.masterKey.Key0)
	if err != nil {
		return nil, serrors.WrapStr("initializing colibri store", err)
	}

	colibriService := &colgrpc.ColibriService{
		Store: colibriStore,
	}
	// colpb.RegisterColibriServer(quicServer, colibriService)
	colServer := coliquic.NewGrpcServer(libgrpc.UnaryServerInterceptor())
	tcpColServer := grpc.NewServer(libgrpc.UnaryServerInterceptor())
	colpb.RegisterColibriServer(colServer, colibriService)
	colpb.RegisterColibriServer(tcpColServer, colibriService)

	// run inter and intra AS servers
	topo := itopo.Get()
	// TODO(juagargi) integrate TCP and QUIC with just one listener in coliquic.ColibriListener
	go func() {
		defer log.HandlePanic()
		lis := cfgObjs.quicStack.Listener
		log.Info("DELETEME %%%%%%%%% colibri grpc server listening", "addr", lis.Addr())
		if err := colServer.Serve(lis); err != nil {
			fatal.Fatal(err)
		}
	}()
	go func() {
		defer log.HandlePanic()
		tcpListener := cfgObjs.tcpStack
		log.Info("DELETEME %%%%%%%%% colibri TCP grpc server listening", "tcp_addr", tcpListener.Addr())
		if err := tcpColServer.Serve(tcpListener); err != nil {
			fatal.Fatal(err)
		}
	}()

	manager, err := colibriManager(topo, cfgObjs.router, colibriStore, cfg.Reservations)
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
	mgr, err := reservationstore.NewColibriManager(topo.IA(), router,
		store, initialRsvs)
	if err != nil {
		return nil, serrors.WrapStr("could not start colibri manager", err)
	}
	return periodic.Start(mgr, 100*time.Millisecond, 5*time.Second), nil
	//
	//
	//
	//
	//
	// dont
	// forget
	// to
	// remote
	// deleteme
	// return periodic.Start(mgr, 100*time.Millisecond, 5*time.Hour), nil // TODO(juagargi)
}
