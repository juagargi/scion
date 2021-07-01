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
	"net"
	"path/filepath"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/resolver"

	"github.com/scionproto/scion/go/cs/config"
	coli_conf "github.com/scionproto/scion/go/cs/reservation/conf"
	admission "github.com/scionproto/scion/go/cs/reservation/segment/admission/stateless"
	"github.com/scionproto/scion/go/cs/reservationstorage"
	"github.com/scionproto/scion/go/cs/reservationstore"
	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/coliquic"
	"github.com/scionproto/scion/go/lib/fatal"
	"github.com/scionproto/scion/go/lib/infra/infraenv"
	"github.com/scionproto/scion/go/lib/infra/messenger"
	"github.com/scionproto/scion/go/lib/infra/modules/itopo"
	"github.com/scionproto/scion/go/lib/infra/modules/segfetcher"
	segfetchergrpc "github.com/scionproto/scion/go/lib/infra/modules/segfetcher/grpc"
	"github.com/scionproto/scion/go/lib/infra/modules/seghandler"
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
	"github.com/scionproto/scion/go/pkg/sciond"
	"github.com/scionproto/scion/go/pkg/storage"
	"github.com/scionproto/scion/go/pkg/trust"
	"github.com/scionproto/scion/go/pkg/trust/compat"
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

// cfgObjs contains the objects needed for the confinguration of colibri.
type cfgObjs struct {
	masterKey keyconf.Master
	revCache  revcache.RevCache
	pathDB    pathdb.PathDB
	trustDB   storage.TrustDB
	nc        *infraenv.NetworkConfig
	quicStack *infraenv.QUICStack
	tcpStack  net.Listener
	dialer    *libgrpc.QUICDialer
	tcpDialer *libgrpc.TCPDialer
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
	// quicStack, err := nc.QUICStack()
	quicStack, err := nc.QUICStack_deleteme()
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

	tcpDialer := &libgrpc.TCPDialer{
		SvcResolver: func(dst addr.HostSVC) []resolver.Address {
			targets := []resolver.Address{}
			addrs, err := itopo.Provider().Get().Multicast(dst)
			if err != nil {
				return targets
			}
			for _, entry := range addrs {
				targets = append(targets, resolver.Address{Addr: entry.String()})
			}
			return targets
		},
	}

	cfgObjs := &cfgObjs{
		revCache:  revCache,
		pathDB:    pathDB,
		trustDB:   trustDB,
		nc:        nc,
		quicStack: quicStack,
		tcpStack:  tcpStack,
		dialer:    dialer,
		tcpDialer: tcpDialer,
		closeFcn: func() {
			// LIFO order:
			quicStack.RedirectCloser()
			trustDB.Close()
			pathDB.Close()
			revCache.Close()
		},
	}

	return setupRouter(cfg, cfgObjs)
}

func setupRouter(cfg *config.Config, cfgObjs *cfgObjs) (*cfgObjs, error) {
	engine, err := sciond.TrustEngine(cfg.General.ConfigDir, cfgObjs.trustDB, cfgObjs.tcpDialer)
	if err != nil {
		return nil, serrors.WrapStr("creating trust engine", err)
	}
	requester := &segfetchergrpc.Requester{
		Dialer: cfgObjs.tcpDialer,
	}
	verifier := compat.Verifier{Verifier: trust.Verifier{
		Engine:             engine,
		Cache:              cfg.TrustEngine.Cache.New(),
		MaxCacheExpiration: cfg.TrustEngine.Cache.Expiration,
	}}
	pather := &segfetcher.Pather{
		RevCache:     cfgObjs.revCache,
		TopoProvider: itopo.Provider(),
		Fetcher: &segfetcher.Fetcher{
			QueryInterval: 10 * time.Minute,
			PathDB:        cfgObjs.pathDB,
			Resolver: segfetcher.NewResolver(
				cfgObjs.pathDB,
				cfgObjs.revCache,
				neverLocal{},
			),
			ReplyHandler: &seghandler.Handler{
				Verifier: &seghandler.DefaultVerifier{Verifier: verifier},
				Storage: &seghandler.DefaultStorage{
					PathDB:   cfgObjs.pathDB,
					RevCache: cfgObjs.revCache,
				},
			},
			Requester: &segfetcher.DefaultRequester{
				RPC:         requester,
				DstProvider: &dstProvider{},
			},
			Metrics: segfetcher.NewFetcherMetrics("co"),
		},
		Splitter: &segfetcher.MultiSegmentSplitter{
			LocalIA:   itopo.Provider().Get().IA(),
			Core:      itopo.Get().Core(),
			Inspector: engine,
		},
	}
	cfgObjs.router = NewRouter(pather)
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
	colibriStore, err := reservationstore.NewStore(itopo.Get(), cfgObjs.router, cfgObjs.dialer,
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
	// TODO(juagargi) integrate TCP and QUIC with just one listener in coliquic.
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

type dstProvider struct {
}

func (r *dstProvider) Dst(_ context.Context, _ segfetcher.Request) (net.Addr, error) {
	return addr.SvcCS, nil
}

type neverLocal struct{}

func (neverLocal) IsSegLocal(_ segfetcher.Request) bool {
	return false
}

type router struct {
	pather *segfetcher.Pather
}

func NewRouter(pather *segfetcher.Pather) *router {
	return &router{
		pather: pather,
	}
}

func (r *router) Route(ctx context.Context, dst addr.IA) (snet.Path, error) {
	paths, err := r.AllRoutes(ctx, dst)
	if len(paths) > 0 {
		return paths[0], err
	}
	return nil, err
}

func (r *router) AllRoutes(ctx context.Context, dst addr.IA) ([]snet.Path, error) {
	return r.pather.GetPaths(ctx, dst, false)
}
