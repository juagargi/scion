// Copyright 2026 ETH Zurich
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/netip"
	"os"
	"path"
	"time"

	"connectrpc.com/connect"
	"golang.org/x/sync/errgroup"
	"google.golang.org/grpc"

	"github.com/quic-go/quic-go/http3"
	"github.com/scionproto/scion/marketplace"
	marketplacestorage "github.com/scionproto/scion/marketplace/storage"
	"github.com/scionproto/scion/marketplace/webapp"
	libconnect "github.com/scionproto/scion/pkg/connect"
	"github.com/scionproto/scion/pkg/hummingbird/registration"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/control_plane"
	"github.com/scionproto/scion/pkg/proto/control_plane/v1/control_planeconnect"
	"github.com/scionproto/scion/pkg/proto/hummingbird"
	"github.com/scionproto/scion/pkg/proto/hummingbird/v1/hummingbirdconnect"
	"github.com/scionproto/scion/pkg/scrypto/cppki"
	"github.com/scionproto/scion/pkg/segment/iface"
	"github.com/scionproto/scion/pkg/snet"
	"github.com/scionproto/scion/private/app"
	"github.com/scionproto/scion/private/app/appnet"
	"github.com/scionproto/scion/private/app/launcher"
	"github.com/scionproto/scion/private/storage"
	"github.com/scionproto/scion/private/topology"
	"github.com/scionproto/scion/private/trust"
	trustgrpc "github.com/scionproto/scion/private/trust/grpc"
	"github.com/scionproto/scion/private/trust/happy"
)

const APIMajorVersion = uint32(0)
const APIMinorVersion = uint32(1)

var globalCfg marketplace.Config

func main() {
	application := launcher.Application{
		ApplicationBase: launcher.ApplicationBase{
			TOMLConfig: &globalCfg,
			ShortName:  "Hummingbird Marketplace",
			Main:       realMain,
		},
	}
	application.Run()
}

type controlServiceFetcherGrpc struct {
	client control_plane.TrustMaterialServiceClient
}

func (f *controlServiceFetcherGrpc) Chains(ctx context.Context, query trust.ChainQuery,
	_ net.Addr) ([][]*x509.Certificate, error) {

	rep, err := f.client.Chains(ctx, trustgrpc.ChainQueryToReq(query))
	if err != nil {
		return nil, serrors.Wrap("fetching chains over connect", err)
	}
	chains, _, err := trustgrpc.RepToChains(rep.Chains)
	if err != nil {
		return nil, serrors.Wrap("parsing chains", err)
	}
	if err := trustgrpc.CheckChainsMatchQuery(query, chains); err != nil {
		return nil, serrors.Wrap("chains do not match query", err)
	}
	return chains, nil
}

func (f *controlServiceFetcherGrpc) TRC(ctx context.Context, id cppki.TRCID,
	_ net.Addr) (cppki.SignedTRC, error) {

	rep, err := f.client.TRC(ctx, trustgrpc.IDToReq(id))
	if err != nil {
		return cppki.SignedTRC{}, serrors.Wrap("fetching chains over connect", err)
	}
	//nolint:forbidigo // generated field.
	trc, err := cppki.DecodeSignedTRC(rep.Trc)
	if err != nil {
		return cppki.SignedTRC{}, serrors.Wrap("parse TRC reply", err)
	}
	if trc.TRC.ID != id && !(id.Base == 0 && id.Serial == 0) {
		return cppki.SignedTRC{}, serrors.New("received wrong TRC", "expected", id,
			"actual", trc.TRC.ID)
	}
	return trc, nil
}

type controlServiceFetcherConnect struct {
	client control_planeconnect.TrustMaterialServiceClient
}

func (f *controlServiceFetcherConnect) Chains(ctx context.Context, query trust.ChainQuery,
	_ net.Addr) ([][]*x509.Certificate, error) {

	rep, err := f.client.Chains(ctx, connect.NewRequest(trustgrpc.ChainQueryToReq(query)))
	if err != nil {
		return nil, serrors.Wrap("fetching chains over connect", err)
	}
	chains, _, err := trustgrpc.RepToChains(rep.Msg.Chains)
	if err != nil {
		return nil, serrors.Wrap("parsing chains", err)
	}
	if err := trustgrpc.CheckChainsMatchQuery(query, chains); err != nil {
		return nil, serrors.Wrap("chains do not match query", err)
	}
	return chains, nil
}

func (f *controlServiceFetcherConnect) TRC(ctx context.Context, id cppki.TRCID,
	_ net.Addr) (cppki.SignedTRC, error) {

	rep, err := f.client.TRC(ctx, connect.NewRequest(trustgrpc.IDToReq(id)))
	if err != nil {
		return cppki.SignedTRC{}, serrors.Wrap("fetching chains over connect", err)
	}
	//nolint:forbidigo // generated field.
	trc, err := cppki.DecodeSignedTRC(rep.Msg.Trc)
	if err != nil {
		return cppki.SignedTRC{}, serrors.Wrap("parse TRC reply", err)
	}
	if trc.TRC.ID != id && !(id.Base == 0 && id.Serial == 0) {
		return cppki.SignedTRC{}, serrors.New("received wrong TRC", "expected", id,
			"actual", trc.TRC.ID)
	}
	return trc, nil
}

type recurser struct {
}

func (r *recurser) AllowRecursion(peer net.Addr) error {
	return nil
}

func realMain(ctx context.Context) error {
	var snetTopo snet.Topology
	var err error
	topo, err := topology.NewLoader(topology.LoaderCfg{
		File:      globalCfg.General.Topology(),
		Validator: &topology.DefaultValidator{},
	})
	if err != nil {
		return serrors.Wrap("creating topology loader", err)
	}
	trustDB, err := storage.NewInMemoryTrustStorage()
	if err != nil {
		return err
	}
	_, err = trust.LoadTRCs(context.Background(), path.Join(globalCfg.General.ConfigDir, "certs"), trustDB)
	if err != nil {
		return err
	}
	var regService *registration.Service

	startPort, endPort := topo.PortRange()
	snetTopo.PortRange = snet.TopologyPortRange{
		Start: startPort,
		End:   endPort,
	}
	snetTopo.LocalIA = topo.IA()
	snetTopo.Interface = func(u uint16) (netip.AddrPort, bool) {
		i, found := topo.InterfaceInfoMap()[iface.ID(u)]
		if !found {
			return netip.AddrPort{}, false
		}
		return i.InternalAddr, true
	}
	if !globalCfg.Marketplace.DisableASRegistration {
		connectConn := control_planeconnect.NewTrustMaterialServiceClient(http.DefaultClient, fmt.Sprintf("http://%s", topo.ControlServiceAddresses()[0].String()))
		cpConn, err := grpc.DialContext(ctx, topo.ControlServiceAddresses()[0].String(), grpc.WithInsecure())
		if err != nil {
			return err
		}
		cpTrustMaterialClient := control_plane.NewTrustMaterialServiceClient(cpConn)
		fetcher := happy.Fetcher{
			Connect: &controlServiceFetcherConnect{
				client: connectConn,
			},
			Grpc: &controlServiceFetcherGrpc{
				client: cpTrustMaterialClient,
			},
		}
		trustDB = marketplace.FromTrustDB(trustDB, fetcher, topo.ControlServiceAddresses()[0])
		trustProvider := trust.FetchingProvider{
			DB:      trustDB,
			Fetcher: fetcher,
			Router: trust.LocalRouter{
				IA: topo.IA(),
			},
			Recurser: &recurser{},
		}
		regService = registration.NewService(trustProvider)
	}

	store, err := marketplacestorage.NewStorage(globalCfg.MarketplaceDB, globalCfg.Marketplace.TransactionFeeRelative,
		globalCfg.Marketplace.TransactionFeeAbsolute, globalCfg.Marketplace.SplitCombineFeeAbsolute, globalCfg.Marketplace.DelegationHourlyFee)
	if err != nil {
		return err
	}
	cert, err := loadOrCreateCertificate(path.Join(globalCfg.General.ConfigDir, "server.crt"), path.Join(globalCfg.General.ConfigDir, "server.key"))
	if err != nil {
		return err
	}
	signingPubKey, signingPrivKey, err := getJwtKeys()
	if err != nil {
		return err
	}
	jwtSigner := registration.NewSigner(signingPrivKey)
	tokenVerifier := &marketplace.TokenVerifier{
		Store:       store,
		JWTVerifier: registration.NewVerifier(signingPubKey),
	}

	trustVerifer := trust.NewTLSCryptoVerifier(trustDB)
	service, err := marketplace.NewService(ctx, &marketplace.MarketplaceInfo{
		ApiMajorVersion:              APIMajorVersion,
		ApiMinorVersion:              APIMinorVersion,
		Currency:                     globalCfg.Marketplace.Currency,
		StatisticsTimeGranularity:    globalCfg.Marketplace.StatisticsTimeGranularity,
		SupportsRedemptionDelegation: globalCfg.Marketplace.SupportsRedemptionDelegation,
		CurrencyExponent:             globalCfg.Marketplace.CurrencyExponent,
		PricingStrategy:              hummingbird.PricingStrategy_static_pricing,
		TransactionFeeRelative:       globalCfg.Marketplace.TransactionFeeRelative,
		TransactionFeeAbsolute:       globalCfg.Marketplace.TransactionFeeAbsolute,
		SplitCombineFeeAbsolute:      globalCfg.Marketplace.SplitCombineFeeAbsolute,
		DelegationHourlyFee:          globalCfg.Marketplace.DelegationHourlyFee,
		MaxReturnedAssets:            globalCfg.Marketplace.MaxRowsPerPage,
		AssetValidityMax:             globalCfg.Marketplace.AssetValidityMax,
	}, store, regService, jwtSigner)
	if err != nil {
		return err
	}

	mux := http.NewServeMux()
	apiPath1, handler1 := hummingbirdconnect.NewMarketplaceServiceHandler(service, connect.WithInterceptors(marketplace.NewAuthInterceptor(tokenVerifier)))
	apiPath2, handler2 := hummingbirdconnect.NewRedemptionServiceHandler(service, connect.WithInterceptors(marketplace.NewAuthInterceptor(tokenVerifier)))

	mux.Handle(apiPath1, handler1)
	mux.Handle(apiPath2, handler2)

	accountPath, accountHandler := hummingbirdconnect.NewAccountServiceHandler(service, connect.WithInterceptors(marketplace.AccountManagerInterceptor(tokenVerifier)))

	webapp.Init(jwtSigner, store, mux, globalCfg.Marketplace.DisableUserRegistration)
	mux.Handle(accountPath, accountHandler)
	server := &http.Server{
		Addr:    globalCfg.Marketplace.APIAddr,
		Handler: loggingMiddleware(mux),
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{cert},
		},
	}
	var cleanup app.Cleanup
	g, errCtx := errgroup.WithContext(ctx)
	g.Go(func() error {
		return server.ListenAndServeTLS("", "")
	})
	log.Info(fmt.Sprintf("HTTPS server running on %s\n", globalCfg.Marketplace.APIAddr))
	if globalCfg.Marketplace.SCIONAPIAddr != "" {
		err = StartSCIONServer(errCtx, snetTopo, 1400, globalCfg.Marketplace.SCIONAPIAddr, g, trustVerifer, &cert, mux, &cleanup)
		if err != nil {
			return err
		}
	}
	cleanup.Add(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		return nil
	})
	g.Go(func() error {
		defer log.HandlePanic()
		<-errCtx.Done()
		return cleanup.Do()
	})

	// Without this, a server failing to listen, e.g. because its port is already
	// taken, ends the process with neither an error nor a message.
	if err := g.Wait(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func loggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Printf("%s: %s %s%s\n",
			r.Proto,
			r.Method,
			r.URL.Path,
			func() string {
				if r.URL.RawQuery != "" {
					return "?" + r.URL.RawQuery
				}
				return ""
			}(),
		)

		next.ServeHTTP(w, r)
	})
}

func StartSCIONServer(ctx context.Context, topo snet.Topology, mtu uint16, addrString string, g *errgroup.Group, trustVerifier *trust.TLSCryptoVerifier, cert *tls.Certificate, mux *http.ServeMux, cleanup *app.Cleanup) error {
	addr, err := net.ResolveUDPAddr("udp", addrString)
	if err != nil {
		return err
	}
	nc := appnet.NetworkConfig{
		Topology: topo,
		IA:       topo.LocalIA,
		QUIC: appnet.QUIC{
			TLSVerifier: trustVerifier,
			GetCertificate: func(chi *tls.ClientHelloInfo) (*tls.Certificate, error) {
				return cert, nil
			},
		},
		Public: addr,
		MTU:    mtu,
	}
	quicStack, err := nc.QUICStack(ctx)
	if err != nil {
		return err
	}
	cleanup.Add(func() error { return quicStack.Listener.Close() })
	connectServer := &http3.Server{
		Handler: loggingMiddleware(mux),
	}
	cleanup.Add(func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()
		if err := connectServer.Shutdown(ctx); err != nil && ctx.Err() == nil {
			return err
		}
		return nil
	})
	connectionDispatcher := libconnect.ConnectionDispatcher{
		Listener: quicStack.Listener,
		Connect:  connectServer,
		Error: func(err error) {
			log.Debug("Error handling ConnectRPC connection", "err", err)
		},
	}
	g.Go(func() error {
		defer log.HandlePanic()
		if err := connectionDispatcher.Run(ctx); err != nil {
			return serrors.Wrap("serving ConnectRPC/SCION API", err)
		}
		return nil
	})
	return nil
}

func saveSignatureKeys(pubKey ed25519.PublicKey, privKey ed25519.PrivateKey) error {
	privBytes, err := x509.MarshalPKCS8PrivateKey(privKey)
	if err != nil {
		return err
	}

	privBlock := &pem.Block{
		Type:  "PRIVATE KEY",
		Bytes: privBytes,
	}

	if err := os.WriteFile(path.Join(globalCfg.General.ConfigDir, "signature_priv.pem"), pem.EncodeToMemory(privBlock), 0644); err != nil {
		return err
	}

	pubBytes, err := x509.MarshalPKIXPublicKey(pubKey)
	if err != nil {
		return err
	}

	pubBlock := &pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	}
	if err := os.WriteFile(path.Join(globalCfg.General.ConfigDir, "signature_pub.pem"), pem.EncodeToMemory(pubBlock), 0644); err != nil {
		return err
	}

	return nil
}

func getJwtKeys() (ed25519.PublicKey, ed25519.PrivateKey, error) {
	pubFile := path.Join(globalCfg.General.ConfigDir, "signature_pub.pem")
	privFile := path.Join(globalCfg.General.ConfigDir, "signature_priv.pem")
	if _, err := os.Stat(privFile); os.IsNotExist(err) {
		signingPubKey, signingPrivKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		return signingPubKey, signingPrivKey, saveSignatureKeys(signingPubKey, signingPrivKey)
	}
	privData, err := os.ReadFile(privFile)
	if err != nil {
		return nil, nil, err
	}
	block, _ := pem.Decode(privData)
	if block == nil {
		return nil, nil, fmt.Errorf("failed to decode PEM")
	}

	privkey, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}

	priv, ok := privkey.(ed25519.PrivateKey)
	if !ok {
		return nil, nil, fmt.Errorf("not an Ed25519 private key")
	}

	pubData, err := os.ReadFile(pubFile)
	if err != nil {
		return nil, nil, err
	}

	block, _ = pem.Decode(pubData)
	if block == nil {
		return nil, nil, fmt.Errorf("failed to decode PEM")
	}

	pubKey, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, nil, err
	}

	pub, ok := pubKey.(ed25519.PublicKey)
	if !ok {
		return nil, nil, fmt.Errorf("not an Ed25519 public key")
	}

	return pub, priv, nil
}
func loadOrCreateCertificate(certFile, keyFile string) (tls.Certificate, error) {
	// Try existing files first.
	if _, err := os.Stat(certFile); err == nil {
		return tls.LoadX509KeyPair(certFile, keyFile)
	}

	// Generate new key.
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return tls.Certificate{}, err
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			CommonName: "marketplace.local",
		},
		NotBefore: time.Now(),
		NotAfter:  time.Now().Add(365 * 24 * time.Hour),

		KeyUsage: x509.KeyUsageKeyEncipherment |
			x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{
			x509.ExtKeyUsageServerAuth,
		},

		DNSNames: []string{"marketplace.local"},
	}

	derBytes, err := x509.CreateCertificate(
		rand.Reader,
		&template,
		&template,
		&priv.PublicKey,
		priv,
	)
	if err != nil {
		return tls.Certificate{}, err
	}

	// Write certificate.
	certOut, err := os.Create(certFile)
	if err != nil {
		return tls.Certificate{}, err
	}
	defer certOut.Close()

	err = pem.Encode(certOut, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})
	if err != nil {
		return tls.Certificate{}, err
	}

	// Write private key.
	keyOut, err := os.OpenFile(keyFile, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return tls.Certificate{}, err
	}
	defer keyOut.Close()

	err = pem.Encode(keyOut, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})
	if err != nil {
		return tls.Certificate{}, err
	}

	return tls.LoadX509KeyPair(certFile, keyFile)
}
