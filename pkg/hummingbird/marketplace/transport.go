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
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"connectrpc.com/connect"
	"github.com/quic-go/quic-go"
	"github.com/quic-go/quic-go/http3"
	"github.com/scionproto/scion/pkg/addr"
	libconnect "github.com/scionproto/scion/pkg/connect"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/proto/hummingbird/v1/hummingbirdconnect"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
	"github.com/scionproto/scion/pkg/snet/squic"
	"github.com/scionproto/scion/private/app/appnet"
)

// ClientOptions configures the transport used by NewClientSet.
// Querier and Topology are required only for SCION connections.
type ClientOptions struct {
	Querier  snet.PathQuerier
	Topology snet.Topology
	Insecure bool
}

// Client contains all marketplace API clients backed by one shared transport.
// Close releases that transport, and must be called once the set is no longer used.
type Client struct {
	Marketplace hummingbirdconnect.MarketplaceServiceClient
	Redemption  hummingbirdconnect.RedemptionServiceClient
	Account     hummingbirdconnect.AccountServiceClient
	Authority   string
	SCION       bool

	// scion is the transport to a marketplace reached over SCION, nil for one reached over TCP.
	scion *scionTransport
	// tcp is the transport to a marketplace reached over TCP, but only when this set owns it.
	// It is nil for a SCION marketplace, and also for a TCP one left on http.DefaultTransport,
	// which is shared process-wide and must not be torn down here.
	tcp *http.Transport
}

// scionTransport are the layers a SCION marketplace connection is built from, outermost first.
// They are kept together because closing them is only correct in this order, see close.
type scionTransport struct {
	h3   *http3.Transport
	quic *quic.Transport
	conn *snet.Conn
}

// closingPeriod is how long the SCION socket stays open after the QUIC connection was closed.
// RFC 9000 (section 10.2.1) asks an endpoint that sent a CONNECTION_CLOSE to keep answering
// late packets of the peer for 3 Probe Timeouts (PTO);
// indeed the marketplace does send some after our close: they were already in flight.
// A var so tests can shorten it.
var closingPeriod = time.Second

// close tears the layers down from the outside in.
// http3 closes the HTTP/3 connections gracefully, so the marketplace learns of the shutdown;
// otherwise it holds its side of every connection until the idle timeout expires.
// Then QUIC is stopped, and finally the SCION socket is closed, but not right away:
// QUIC discards the late packets of the marketplace only while it still owns the socket, and
// the port would otherwise be handed to the next socket opened, which would then read them.
// So the last two steps run after closingPeriod, in a timer of their own, and their errors
// can only be logged. Nothing else closes the socket: QUIC does not own it.
func (t *scionTransport) close() error {
	var errs []error
	if err := t.h3.Close(); err != nil {
		errs = append(errs, serrors.Wrap("closing the HTTP/3 transport", err))
	}
	time.AfterFunc(closingPeriod, func() {
		if err := t.quic.Close(); err != nil {
			log.Error("Closing the QUIC transport", "err", err)
		}
		if err := t.conn.Close(); err != nil {
			log.Error("Closing the SCION connection", "err", err)
		}
	})
	return errors.Join(errs...)
}

// IsSCIONURL reports whether rawURL contains a SCION address.
func IsSCIONURL(rawURL string) bool {
	api, err := endpointAddress(rawURL)
	if err != nil {
		return false
	}
	return isSCIONAddress(api)
}

// Close releases the transport shared by the clients of the set,
// which must not be used afterwards. Calling it more than once is a no-op.
func (s *Client) Close() error {
	if s.tcp != nil {
		// Only the idle connections are closed; no request of ours is still holding one.
		s.tcp.CloseIdleConnections()
		s.tcp = nil
	}
	if s.scion == nil {
		return nil
	}
	transport := s.scion
	s.scion = nil
	return transport.close()
}

// NewClient creates marketplace, redemption, and account clients that share
// the same TCP or SCION transport.
//
// Valid URL forms include:
//
//	https://127.0.0.1:31888
//	https://my-marketplace.local:31888
//	[1-ff00:0:111,127.0.0.1]:31888
//	[1-ff00:0:111,my-marketplace.local]:31888
func NewClient(
	ctx context.Context,
	rawURL string,
	token string,
	options ClientOptions,
) (*Client, error) {
	api, err := endpointAddress(rawURL)
	if err != nil {
		return nil, err
	}
	interceptor := connect.WithInterceptors(NewAuthInterceptor(token))
	if !isSCIONAddress(api) {
		httpClient := http.DefaultClient
		var transport *http.Transport
		if options.Insecure {
			transport = http.DefaultTransport.(*http.Transport).Clone()
			transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			httpClient = &http.Client{Transport: transport}
		}
		set := newClient(httpClient, rawURL, api, false, interceptor)
		set.tcp = transport
		return set, nil
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
	httpClient, transport, baseURL, err := newSCIONHTTPClient(ctx, remote, serverName, options)
	if err != nil {
		return nil, err
	}
	authority := strings.TrimPrefix(baseURL, "https://")
	set := newClient(httpClient, baseURL, authority, true, interceptor)
	set.scion = transport
	return set, nil
}

func newClient(
	httpClient connect.HTTPClient,
	baseURL string,
	authority string,
	scion bool,
	options ...connect.ClientOption,
) *Client {
	return &Client{
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
) (connect.HTTPClient, *scionTransport, string, error) {
	if remote.IA == options.Topology.LocalIA {
		remote.Path = snetpath.Empty{}
		remote.NextHop = remote.Host
	} else {
		paths, err := options.Querier.Query(ctx, remote.IA)
		if err != nil {
			return nil, nil, "", err
		}
		if len(paths) == 0 {
			return nil, nil, "", serrors.New("no paths found to marketplace")
		}
		remote.Path = paths[0].Dataplane()
		remote.NextHop = paths[0].UnderlayNextHop()
	}
	conn, err := net.Dial("udp", remote.NextHop.String())
	if err != nil {
		return nil, nil, "", err
	}
	localPublic, ok := conn.LocalAddr().(*net.UDPAddr)
	if closeErr := conn.Close(); closeErr != nil {
		return nil, nil, "", closeErr
	}
	if !ok {
		return nil, nil, "", serrors.New("localAddr not UDP addr")
	}
	sn := snet.SCIONNetwork{
		Topology: options.Topology,
		// The QUIC client turns any error coming out of a read into a dead connection.
		// The SCMP handler returns an error for every SCMP error it understands,
		// even if temporary.
		// SCMP errors are handled and logged, but not propagated:
		// a temporary error on the path to the marketplace must not tear down the connection.
		SCMPHandler: snet.SCMPPropagationStopper{
			Handler: snet.DefaultSCMPHandler{},
			Log:     log.Debug,
		},
	}
	localAddr := &net.UDPAddr{
		IP:   localPublic.IP,
		Port: 0,
		Zone: localPublic.Zone,
	}
	client, err := sn.Listen(ctx, "udp", localAddr)
	if err != nil {
		return nil, nil, "", err
	}
	clientTransport := &quic.Transport{
		Conn: client,
	}
	dialer := (&squic.EarlyDialerFactory{
		Transport: clientTransport,
		TLSConfig: &tls.Config{
			NextProtos:         []string{"h3", "SCION"},
			ServerName:         serverName,
			InsecureSkipVerify: options.Insecure,
		},
		Rewriter: &appnet.AddressRewriter{
			Router: &snet.BaseRouter{
				Querier: options.Querier,
			},
		},
		QUICConfig: &quic.Config{
			InitialPacketSize: 1200, // Default is 1280, but with SCION we have less payload.
		},
	}).NewDialer(remote)
	roundTripper := &http3.Transport{Dial: dialer.DialEarly}
	transport := &scionTransport{
		h3:   roundTripper,
		quic: clientTransport,
		conn: client,
	}
	return libconnect.HTTPClient{RoundTripper: roundTripper}, transport,
		libconnect.BaseUrl(remote), nil
}

type AuthInterceptor struct {
	token string
}

func NewAuthInterceptor(token string) *AuthInterceptor {
	return &AuthInterceptor{token: token}
}

func (a *AuthInterceptor) WrapUnary(next connect.UnaryFunc) connect.UnaryFunc {
	return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
		req.Header().Set("Authorization", "Bearer "+a.token)
		return next(ctx, req)
	}
}

func (a *AuthInterceptor) WrapStreamingClient(
	next connect.StreamingClientFunc,
) connect.StreamingClientFunc {
	return func(ctx context.Context, spec connect.Spec) connect.StreamingClientConn {
		conn := next(ctx, spec)
		conn.RequestHeader().Set("Authorization", "Bearer "+a.token)
		return conn
	}
}

func (a *AuthInterceptor) WrapStreamingHandler(
	next connect.StreamingHandlerFunc,
) connect.StreamingHandlerFunc {
	return next
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
