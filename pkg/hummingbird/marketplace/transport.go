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

package marketplace

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"connectrpc.com/connect"
	"github.com/quic-go/quic-go/http3"
	"github.com/scionproto/scion/pkg/addr"
	libconnect "github.com/scionproto/scion/pkg/connect"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird/v1/hummingbirdconnect"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
	"github.com/scionproto/scion/pkg/snet/squic"
	"github.com/scionproto/scion/private/app/appnet"
	"github.com/scionproto/scion/private/storage"
	"github.com/scionproto/scion/private/trust"
)

// ClientOptions configures the transport used by NewClientSet.
// Querier and Topology are required only for SCION connections.
type ClientOptions struct {
	Querier  snet.PathQuerier
	Topology snet.Topology
	Insecure bool
}

// ClientSet contains all marketplace API clients backed by one shared transport.
type ClientSet struct {
	Marketplace hummingbirdconnect.MarketplaceServiceClient
	Redemption  hummingbirdconnect.RedemptionServiceClient
	Account     hummingbirdconnect.AccountServiceClient
	Authority   string
	SCION       bool
}

// IsSCIONURL reports whether rawURL contains a SCION address.
func IsSCIONURL(rawURL string) bool {
	api, err := endpointAddress(rawURL)
	if err != nil {
		return false
	}
	return isSCIONAddress(api)
}

// NewClientSet creates marketplace, redemption, and account clients that share
// the same TCP or SCION transport.
//
// Valid URL forms include:
//
//	https://127.0.0.1:8888
//	https://my-marketplace.local:8888
//	[1-ff00:0:111,127.0.0.1]:9888
//	[1-ff00:0:111,my-marketplace.local]:9888
func NewClientSet(
	ctx context.Context,
	rawURL string,
	token string,
	options ClientOptions,
) (*ClientSet, error) {
	api, err := endpointAddress(rawURL)
	if err != nil {
		return nil, err
	}
	interceptor := connect.WithInterceptors(authInterceptor(token))
	if !isSCIONAddress(api) {
		httpClient := http.DefaultClient
		if options.Insecure {
			transport := http.DefaultTransport.(*http.Transport).Clone()
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			httpClient = &http.Client{Transport: transport}
		}
		return newClientSet(httpClient, rawURL, api, false, interceptor), nil
	}
	scionAddr, port, serverName, err := parseSCIONAddress(api)
	if err != nil {
		return nil, err
	}
	if options.Querier == nil {
		return nil, serrors.New("SCION marketplace connection requires a path querier")
	}
	if options.Topology.LocalIA.IsZero() {
		return nil, serrors.New("SCION marketplace connection requires a local topology")
	}
	remote := &snet.UDPAddr{
		IA:   scionAddr.IA,
		Host: net.UDPAddrFromAddrPort(netip.AddrPortFrom(scionAddr.Host.IP(), port)),
	}
	httpClient, baseURL, err := newSCIONHTTPClient(ctx, remote, serverName, options)
	if err != nil {
		return nil, err
	}
	authority := strings.TrimPrefix(baseURL, "https://")
	return newClientSet(httpClient, baseURL, authority, true, interceptor), nil
}

func newClientSet(
	httpClient connect.HTTPClient,
	baseURL string,
	authority string,
	scion bool,
	options ...connect.ClientOption,
) *ClientSet {
	return &ClientSet{
		Marketplace: hummingbirdconnect.NewMarketplaceServiceClient(
			httpClient, baseURL, options...),
		Redemption: hummingbirdconnect.NewRedemptionServiceClient(
			httpClient, baseURL, options...),
		Account: hummingbirdconnect.NewAccountServiceClient(
			httpClient, baseURL, options...),
		Authority: authority,
		SCION:     scion,
	}
}

func newSCIONHTTPClient(
	ctx context.Context,
	remote *snet.UDPAddr,
	serverName string,
	options ClientOptions,
) (connect.HTTPClient, string, error) {
	if remote.IA == options.Topology.LocalIA {
		remote.Path = snetpath.Empty{}
		remote.NextHop = remote.Host
	} else {
		paths, err := options.Querier.Query(ctx, remote.IA)
		if err != nil {
			return nil, "", err
		}
		if len(paths) == 0 {
			return nil, "", serrors.New("no paths found to marketplace")
		}
		remote.Path = paths[0].Dataplane()
		remote.NextHop = paths[0].UnderlayNextHop()
	}

	trustDB, err := storage.NewInMemoryTrustStorage()
	if err != nil {
		return nil, "", err
	}
	conn, err := net.Dial("udp", remote.NextHop.String())
	if err != nil {
		return nil, "", err
	}
	localPublic, ok := conn.LocalAddr().(*net.UDPAddr)
	if closeErr := conn.Close(); closeErr != nil {
		return nil, "", closeErr
	}
	if !ok {
		return nil, "", serrors.New("localAddr not UDP addr")
	}

	nc := appnet.NetworkConfig{
		Topology: options.Topology,
		IA:       options.Topology.LocalIA,
		QUIC: appnet.QUIC{
			TLSVerifier: trust.NewTLSCryptoVerifier(trustDB),
		},
		MTU: 1400,
		Public: &net.UDPAddr{
			IP:   localPublic.IP,
			Port: 0,
			Zone: localPublic.Zone,
		},
	}
	quicStack, err := nc.QUICStack(ctx)
	if err != nil {
		return nil, "", err
	}

	tlsConfig := &tls.Config{
		NextProtos: []string{"h3", "SCION"},
		ServerName: serverName,
	}
	if options.Insecure {
		tlsConfig.InsecureSkipVerify = true
	}
	dialer := (&squic.EarlyDialerFactory{
		Transport: quicStack.Dialer.Transport,
		TLSConfig: tlsConfig,
		Rewriter: &appnet.AddressRewriter{
			Router: &snet.BaseRouter{Querier: options.Querier},
		},
	}).NewDialer(remote)
	roundTripper := &http3.Transport{Dial: dialer.DialEarly}
	return libconnect.HTTPClient{RoundTripper: roundTripper}, libconnect.BaseUrl(remote), nil
}

func authInterceptor(jwtToken string) connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+jwtToken)
			return next(ctx, req)
		}
	}
}

func endpointAddress(rawURL string) (string, error) {
	if strings.Count(rawURL, "://") > 1 {
		return "", serrors.New("invalid url", "url", rawURL)
	}
	if _, api, ok := strings.Cut(rawURL, "://"); ok {
		return api, nil
	}
	return rawURL, nil
}

func isSCIONAddress(s string) bool {
	host, _, err := net.SplitHostPort(s)
	if err != nil {
		return false
	}
	ia, host, ok := strings.Cut(host, ",")
	if !ok || host == "" {
		return false
	}
	_, err = addr.ParseIA(ia)
	return err == nil
}

func parseSCIONAddress(s string) (addr.Addr, uint16, string, error) {
	host, port, err := net.SplitHostPort(s)
	if err != nil {
		return addr.Addr{}, 0, "", serrors.Wrap("invalid address: split host:port", err,
			"addr", s)
	}
	splits := strings.Split(host, ",")
	if len(splits) != 2 {
		return addr.Addr{}, 0, "", serrors.New("invalid SCION address", "addr", s)
	}
	if _, err := netip.ParseAddr(splits[1]); err != nil {
		ipAddr, err := net.ResolveIPAddr("ip", splits[1])
		if err != nil {
			return addr.Addr{}, 0, "", serrors.Wrap("resolving SCION host", err, "addr", s)
		}
		s = fmt.Sprintf("[%s,%s]:%s", splits[0], ipAddr.String(), port)
	}
	a, p, err := addr.ParseAddrPort(s)
	return a, p, splits[1], err
}
