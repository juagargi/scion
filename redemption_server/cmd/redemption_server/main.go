// Copyright 2026 ETH Zurich
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
	"net/netip"
	"path"
	"path/filepath"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/scionproto/scion/pkg/daemon"
	"github.com/scionproto/scion/pkg/hummingbird/marketplace"
	"github.com/scionproto/scion/pkg/hummingbird/registration"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/segment/iface"
	"github.com/scionproto/scion/pkg/snet"
	"github.com/scionproto/scion/private/app"
	"github.com/scionproto/scion/private/app/launcher"
	"github.com/scionproto/scion/private/keyconf"
	"github.com/scionproto/scion/private/topology"
	"github.com/scionproto/scion/redemption_server/config"
	"github.com/scionproto/scion/redemption_server/connector"
	"github.com/scionproto/scion/redemption_server/storage"
)

var globalCfg config.Config

func loadHBMasterSecret(path string) (masterKey [16]byte) {
	masterKeys, err := keyconf.LoadMaster(path)
	// We load both master keys, but only use key0
	if err != nil || len(masterKeys.Key0) != 16 {
		panic(err)
	}
	copy(masterKey[:], masterKeys.Key0[0:16])
	return
}

func main() {
	application := launcher.Application{
		ApplicationBase: launcher.ApplicationBase{
			TOMLConfig: &globalCfg,
			ShortName:  "SCION Hummingbird Redemption Service",
			Main:       realMain,
		},
	}
	application.Run()
}

func loadTopo(ctx context.Context) (snet.Topology, error) {
	topo := snet.Topology{}
	loader, err := topology.NewLoader(topology.LoaderCfg{
		File:   globalCfg.General.Topology(),
		Reload: app.SIGHUPChannel(ctx),
	})
	if err != nil {
		return topo, err
	}
	startPort, endPort := loader.PortRange()
	topo.PortRange = snet.TopologyPortRange{
		Start: startPort,
		End:   endPort,
	}
	topo.LocalIA = loader.IA()
	topo.Interface = func(u uint16) (netip.AddrPort, bool) {
		i, found := loader.InterfaceInfoMap()[iface.ID(u)]
		if !found {
			return netip.AddrPort{}, false
		}
		return i.InternalAddr, true
	}
	return topo, nil
}

func realMain(ctx context.Context) error {
	topo, err := loadTopo(ctx)
	if err != nil {
		return serrors.Wrap("creating topology loader", err)
	}

	g, errCtx := errgroup.WithContext(ctx)
	sd, err := daemon.NewAutoConnector(errCtx, daemon.WithConfigDir(globalCfg.General.ConfigDir))
	if err != nil {
		return err
	}

	masterKey := loadHBMasterSecret(filepath.Join(globalCfg.General.ConfigDir, "keys"))
	store, err := storage.NewStorage(globalCfg.HB.RedemptionDB)
	if err != nil {
		return err
	}
	for _, cfg := range globalCfg.HB.Marketplaces {
		g.Go(func() error {
			//var client *marketplace.Client
			//var err error
			initClients := func() (*marketplace.Client, error) {
				// First we initialize a client without a JWT token. This client is used to perform the
				// registration steps to obtain the JWT tokens.
				client, err := marketplace.NewClient(ctx, cfg.Address, "", marketplace.ClientOptions{
					Querier:  daemon.Querier{Connector: sd},
					Topology: topo,
					Insecure: cfg.Insecure,
				})
				if err != nil {
					return nil, err
				}
				// Now we perform the registration steps
				regClient := registration.NewClient(client.Account, client.Authority)
				_, token, err := regClient.RegisterWithNewSigner(errCtx, topo.LocalIA, path.Join(globalCfg.General.ConfigDir, "certs"),
					path.Join(globalCfg.General.ConfigDir, "crypto/as"), path.Join(globalCfg.General.ConfigDir, "crypto/as"))
				if err != nil {
					return nil, err
				}
				// Here we close the previous client and re-initialize the client but now with the
				// redemption server JWT token.
				err = client.Close()
				if err != nil {
					return nil, err
				}
				client, err = marketplace.NewClient(ctx, cfg.Address, token, marketplace.ClientOptions{
					Querier:  daemon.Querier{Connector: sd},
					Topology: topo,
					Insecure: cfg.Insecure,
				})
				if err != nil {
					return nil, err
				}
				return client, nil
			}
			runConnector := func(client *marketplace.Client) error {
				c, err := connector.NewConnector(errCtx, masterKey[:], cfg, client.Redemption, store)
				if err != nil {
					return err
				}
				err = c.StartRedemption(errCtx)
				if err != nil {
					return err
				}
				return nil
			}
			for {
				select {
				case <-errCtx.Done():
					return nil
				case <-time.After(time.Second * 10):
				}
				client, err := initClients()
				if err != nil {
					log.Debug("error initializing client", "err", err)
					continue
				}
				err = runConnector(client)
				if err != nil {
					log.Debug("error running connector", "err", err)
				}
				err = client.Close()
				if err != nil {
					log.Debug("error closing client", "err", err)
				}
			}
		})
	}

	var cleanup app.Cleanup

	g.Go(func() error {
		defer log.HandlePanic()
		<-errCtx.Done()
		return cleanup.Do()
	})

	return g.Wait()
}
