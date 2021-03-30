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
// Test with go test ./go/lib/colibri/coliquic/ -count=1
package coliquic

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/golang/mock/gomock"
	"github.com/lucas-clemente/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/lib/addr"
	"github.com/scionproto/scion/go/lib/colibri/coliquic/mock_coliquic"
	"github.com/scionproto/scion/go/lib/sciond"
	"github.com/scionproto/scion/go/lib/slayers/path/colibri"
	"github.com/scionproto/scion/go/lib/slayers/path/scion"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/sock/reliable"
	"github.com/scionproto/scion/go/lib/spath"
	"github.com/scionproto/scion/go/lib/xtest"
	// "github.com/scionproto/scion/go/lib/underlay/conn/mock_conn"
)

// TODO(juagargi) cleanup

// go test ./go/lib/colibri/coliquic/ -count=1 -run=TestScion -v

var scionNetwork *snet.SCIONNetwork
var pathQuerier snet.PathQuerier

func TestMain(m *testing.M) {
	initScionNetworkAs111()
	os.Exit(m.Run())
}

func TestScionClient(t *testing.T) {
	ctx := context.Background()
	// remoteAddr := "17-ffaa:1:a,127.0.0.1:43210" // ethz netcat
	remoteAddr := "1-ff00:0:112,[fd00:f00d:cafe::7f00:b]:43210"
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"netcat"},
	}
	quicConfig := &quic.Config{KeepAlive: true}

	raddr := getScionPathRemoteAddress(t, remoteAddr)

	listen := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0, Zone: ""}
	sconn, err := scionNetwork.Listen(ctx, "udp", &listen, addr.SvcNone)
	require.NoError(t, err)

	sess, err := quic.Dial(sconn, raddr, "serverName", tlsConfig, quicConfig)
	require.NoError(t, err)
	stream, err := sess.OpenStreamSync(context.Background())
	require.NoError(t, err)
	n, err := stream.Write([]byte("hello world"))
	require.NoError(t, err)
	require.Equal(t, 11, n)
	err = stream.Close()
	require.NoError(t, err)
}

func TestScionServer(t *testing.T) {
	// run the server in 112
	scionNetwork112, _ := initScionNetworkWithSciondPoint("[fd00:f00d:cafe::7f00:b]:30255")
	cert := generateKeyAndCert(t)
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"netcat"},
	}
	quicConfig := &quic.Config{KeepAlive: true}

	// scion connection
	ctx := context.Background()
	listen := net.UDPAddr{IP: net.ParseIP("fd00:f00d:cafe::7f00:b"), Port: 43210, Zone: ""}
	require.NotNil(t, listen.IP)
	sconn, err := scionNetwork112.Listen(ctx, "udp", &listen, addr.SvcNone)
	require.NoError(t, err)

	listener, err := quic.Listen(WrapConn(sconn), tlsConfig, quicConfig)
	require.NoError(t, err)

	session, err := listener.Accept(ctx)
	require.NoError(t, err)
	remoteNetAddr := session.RemoteAddr()
	remoteAddr, ok := remoteNetAddr.(*snet.UDPAddr)
	require.True(t, ok)
	require.NotNil(t, remoteAddr.Path)
	stream, err := session.AcceptStream(ctx)
	require.NoError(t, err)
	stream.Close()
}

func TestColibriClient(t *testing.T) {
	ctx := context.Background()
	// remoteAddr := "17-ffaa:1:a,127.0.0.1:43210" // ethz netcat
	remoteAddr := "1-ff00:0:112,[fd00:f00d:cafe::7f00:b]:43210"
	tlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"netcat"},
	}
	quicConfig := &quic.Config{KeepAlive: true}

	raddr := getColibriRemoteAddress(t, remoteAddr)

	listen := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0, Zone: ""}
	sconn, err := scionNetwork.Listen(ctx, "udp", &listen, addr.SvcNone)
	require.NoError(t, err)

	sess, err := quic.Dial(sconn, raddr, "serverName", tlsConfig, quicConfig)
	require.NoError(t, err)
	stream, err := sess.OpenStreamSync(context.Background())
	require.NoError(t, err)
	n, err := stream.Write([]byte("hello world"))
	require.NoError(t, err)
	require.Equal(t, 11, n)
	err = stream.Close()
	require.NoError(t, err)
}

func TestColibriServer(t *testing.T) {
	// run the server in 112
	scionNetwork112, _ := initScionNetworkWithSciondPoint("[fd00:f00d:cafe::7f00:b]:30255")
	cert := generateKeyAndCert(t)
	tlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*cert},
		NextProtos:   []string{"netcat"},
	}
	quicConfig := &quic.Config{KeepAlive: true}

	// scion connection
	ctx := context.Background()
	listen := net.UDPAddr{IP: net.ParseIP("fd00:f00d:cafe::7f00:b"), Port: 43210, Zone: ""}
	sconn, err := scionNetwork112.Listen(ctx, "udp", &listen, addr.SvcNone)
	require.NoError(t, err)

	listener, err := quic.Listen(sconn, tlsConfig, quicConfig)
	// listener, err := ListenQuic(ctx, scionNetwork, listen, tlsConfig, quicConfig)
	require.NoError(t, err)

	session, err := listener.Accept(ctx)
	require.NoError(t, err)
	remoteNetAddr := session.RemoteAddr()
	remoteAddr, ok := remoteNetAddr.(*snet.UDPAddr)
	require.True(t, ok)
	require.NotNil(t, remoteAddr.Path)
	stream, err := session.AcceptStream(ctx)
	require.NoError(t, err)
	stream.Close()
}

type bundle struct {
	sender net.Addr
	data   []byte
}

type network struct {
	channels map[string]chan bundle
	m        sync.Mutex
}

func NewNetwork() *network {
	return &network{
		channels: make(map[string]chan bundle),
	}
}

func (n *network) ReadFrom(receiver net.Addr) ([]byte, net.Addr) {
	key := receiver.String()
	n.ensureChannel(key)
	bun := <-n.channels[key]
	buff := make([]byte, len(bun.data))
	copy(buff, bun.data)
	return buff, bun.sender
}

func (n *network) WriteTo(sender, receiver net.Addr, data []byte) {
	buff := make([]byte, len(data))
	copy(buff, data)
	bun := bundle{sender: sender, data: buff}
	key := receiver.String()
	n.ensureChannel(key)
	n.channels[key] <- bun
}

func (n *network) ensureChannel(key string) {
	n.m.Lock()
	defer n.m.Unlock()
	if _, found := n.channels[key]; !found {
		n.channels[key] = make(chan bundle, 32)
	}
}

func mockColibriAddress(ia string, host *net.UDPAddr) *snet.UDPAddr {
	return &snet.UDPAddr{
		IA:   xtest.MustParseIA(ia),
		Host: host,
		Path: spath.Path{
			Raw:  []byte{0, 0},
			Type: colibri.PathType,
		},
	}
}

func TestColibriLocal(t *testing.T) {
	ctrl := gomock.NewController(t)
	defer ctrl.Finish()

	// deletemeBundle := make(chan bundle)
	thisNet := NewNetwork()

	// create a server (reads and writes will go thru the scion address)
	serverLocalAddr := &net.UDPAddr{IP: net.ParseIP("fd00:f00d:cafe::7f00:b"), Port: 43210, Zone: ""}
	serverAddr := mockColibriAddress("1-ff00:0:112", serverLocalAddr)
	serverConn := mock_coliquic.NewMockPacketConn(ctrl)
	serverConn.EXPECT().LocalAddr().AnyTimes().Return(serverLocalAddr)
	serverConn.EXPECT().WriteTo(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(buff []byte, addr net.Addr) (int, error) {
			thisNet.WriteTo(serverAddr, addr, buff)
			t.Logf("Server has written %d bytes from %s to %s", len(buff), serverAddr, addr)
			return len(buff), nil
		})
	serverConn.EXPECT().ReadFrom(gomock.Any()).AnyTimes().
		DoAndReturn(func(buff []byte) (int, net.Addr, error) {
			b, sender := thisNet.ReadFrom(serverAddr)
			t.Logf("Server has read %d bytes from %s", len(buff), sender)
			if len(b) > len(buff) {
				panic("buffer too small")
			}
			copy(buff, b)
			return len(buff), sender, nil
		})
	serverTlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*generateKeyAndCert(t)},
		NextProtos:   []string{"netcat"},
	}
	serverQuicConfig := &quic.Config{KeepAlive: true}
	listener, err := quic.Listen(serverConn, serverTlsConfig, serverQuicConfig)
	require.NoError(t, err)

	ctx, cancelF := context.WithTimeout(context.Background(), 5*time.Hour)
	defer cancelF()

	go func(ctx context.Context, listener quic.Listener) {
		session, err := listener.Accept(ctx)
		require.NoError(t, err)
		stream, err := session.AcceptStream(ctx)
		require.NoError(t, err)
		err = stream.Close()
		require.NoError(t, err)
	}(ctx, listener)

	// create a client:
	clientLocalAddr := &net.UDPAddr{IP: net.ParseIP("10.0.0.1"), Port: 8888, Zone: ""}
	clientAddr := mockColibriAddress("1-ff00:0:111", clientLocalAddr)
	clientConn := mock_coliquic.NewMockPacketConn(ctrl)
	clientConn.EXPECT().LocalAddr().AnyTimes().Return(clientLocalAddr)
	clientConn.EXPECT().WriteTo(gomock.Any(), gomock.Any()).AnyTimes().
		DoAndReturn(func(buff []byte, addr net.Addr) (int, error) {
			thisNet.WriteTo(clientAddr, addr, buff)
			t.Logf("Client has written %d bytes from %s to %s", len(buff), clientAddr, addr)
			return len(buff), nil
		})
	clientConn.EXPECT().ReadFrom(gomock.Any()).AnyTimes().
		DoAndReturn(func(buff []byte) (int, net.Addr, error) {
			t.Logf("Client waiting to read with address %s", clientAddr)
			b, sender := thisNet.ReadFrom(clientAddr)
			t.Logf("Client has read %d bytes from %s", len(buff), sender)
			if len(b) > len(buff) {
				panic("buffer too small")
			}
			copy(buff, b)
			return len(buff), sender, nil
		})

	clientTlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"netcat"},
	}
	clientQuicConfig := &quic.Config{KeepAlive: true}
	// remoteAddr := "1-ff00:0:112,[fd00:f00d:cafe::7f00:b]:43210"
	// // path set to colibri:
	// raddr := getColibriRemoteAddress(t, remoteAddr)
	raddr := serverAddr
	ctx2, cancelF2 := context.WithTimeout(context.Background(), 9*time.Hour)
	defer cancelF2()
	session, err := quic.DialContext(ctx2, clientConn, raddr, "serverName", clientTlsConfig, clientQuicConfig)
	require.NoError(t, err)
	_ = session
	// stream, err := sess.OpenStreamSync(context.Background())
	// require.NoError(t, err)
	// n, err := stream.Write([]byte("hello world"))
	// require.NoError(t, err)
	// require.Equal(t, 11, n)
	// err = stream.Close()
	// require.NoError(t, err)

}

type connMock struct {
	LocalAddress net.Addr
	DebugString  string
}

var _ net.PacketConn = (*connMock)(nil)

var thisNet *network

func (c *connMock) LocalAddr() net.Addr {
	return c.LocalAddress
}

func (c *connMock) Close() error {
	return nil
}

func (c *connMock) ReadFrom(p []byte) (int, net.Addr, error) {
	b, sender := thisNet.ReadFrom(c.LocalAddress)
	n := copy(p, b)
	return n, sender, nil
}

func (c *connMock) WriteTo(p []byte, addr net.Addr) (int, error) {
	thisNet.WriteTo(c.LocalAddress, addr, p)
	return len(p), nil
}

func (c *connMock) SetDeadline(t time.Time) error {
	return nil
}

func (c *connMock) SetReadDeadline(t time.Time) error {
	return nil
}

func (c *connMock) SetWriteDeadline(t time.Time) error {
	return nil
}

func mockConn(isServer bool) net.PacketConn {
	var addr net.Addr
	var str string
	if isServer {
		addr = &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43210, Zone: ""}
		str = "server"
	} else {
		addr = &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345, Zone: ""}
		str = "client"
	}
	return &connMock{
		LocalAddress: addr,
		DebugString:  str,
	}
}

func TestDeleteme(t *testing.T) {
	thisNet = NewNetwork()
	// server:
	serverAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43210, Zone: ""}
	// serverConn, err := net.ListenUDP("udp", serverAddr)
	// require.NoError(t, err)
	serverTlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*generateKeyAndCert(t)},
		NextProtos:   []string{"netcat"},
	}
	serverQuicConfig := &quic.Config{KeepAlive: true}
	// listener, err := quic.Listen(serverConn, serverTlsConfig, serverQuicConfig)
	listener, err := quic.Listen(mockConn(true), serverTlsConfig, serverQuicConfig)
	require.NoError(t, err)

	done := make(chan struct{})
	ctx, cancelF := context.WithTimeout(context.Background(), 5*time.Hour)
	defer cancelF()
	go func(ctx context.Context, listener quic.Listener) {
		session, err := listener.Accept(ctx)
		require.NoError(t, err)
		stream, err := session.AcceptStream(ctx)
		require.NoError(t, err)
		buff := make([]byte, 16384)
		n, err := stream.Read(buff)
		require.NoError(t, err)
		require.Equal(t, "hello world", string(buff[:n]))
		err = stream.Close()
		require.NoError(t, err)
		done <- struct{}{}
	}(ctx, listener)

	// client:
	// clientAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345, Zone: ""}
	// clientConn, err := net.ListenUDP("udp", clientAddr)
	// require.NoError(t, err)
	clientTlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"netcat"},
	}
	clientQuicConfig := &quic.Config{KeepAlive: true}

	ctx2, cancelF2 := context.WithTimeout(context.Background(), 9*time.Hour)
	defer cancelF2()
	session, err := quic.DialContext(ctx2, mockConn(false), serverAddr, "serverName",
		clientTlsConfig, clientQuicConfig)
	require.NoError(t, err)
	stream, err := session.OpenStream()
	require.NoError(t, err)
	n, err := stream.Write([]byte("hello world"))
	require.NoError(t, err)
	require.Equal(t, len("hello wold")+1, n)

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.FailNow(t, "timed out")
	}
	err = stream.Close()
	require.NoError(t, err)
}

func initScionNetworkAs111() {
	// scionNetwork, pathQuerier = initScionNetworkWithSciondPoint(sciond.DefaultAPIAddress)
	// scionNetwork, pathQuerier = initScionNetworkWithSciondPoint("[fd00:f00d:cafe::7f00:b]:30255")
	scionNetwork, pathQuerier = initScionNetworkWithSciondPoint("127.0.0.19:30255")
}

func initScionNetworkWithSciondPoint(sciondPoint string) (*snet.SCIONNetwork, snet.PathQuerier) {
	ctx := context.Background()
	dispatcherService := reliable.NewDispatcher("")
	sciondConn, err := sciond.NewService(sciondPoint).Connect(ctx) // 1-ff00:0:111
	if err != nil {
		panic(err)
	}

	localIA, err := sciondConn.LocalIA(ctx)
	if err != nil {
		panic(err)
	}
	scionNetwork = snet.NewNetwork(localIA, dispatcherService, sciond.RevHandler{Connector: sciondConn})
	pathQuerier = sciond.Querier{Connector: sciondConn, IA: localIA}
	return scionNetwork, pathQuerier
}

// createCertificate from scion-apps:
// createCertificate creates a self-signed dummy certificate for the given key
// Inspired/copy pasted from crypto/tls/generate_cert.go
func createCertificate(t *testing.T, priv *rsa.PrivateKey) *tls.Certificate {
	t.Helper()

	notBefore := time.Now()
	notAfter := notBefore.Add(365 * 24 * time.Hour)

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := rand.Int(rand.Reader, serialNumberLimit)
	require.NoError(t, err)

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"scionlab"},
		},
		NotBefore:             notBefore,
		NotAfter:              notAfter,
		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"dummy"},
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	require.NoError(t, err)

	certPEMBuf := &bytes.Buffer{}
	err = pem.Encode(certPEMBuf, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	require.NoError(t, err)

	privBytes, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)

	keyPEMBuf := &bytes.Buffer{}
	err = pem.Encode(keyPEMBuf, &pem.Block{Type: "PRIVATE KEY", Bytes: privBytes})
	require.NoError(t, err)

	cert, err := tls.X509KeyPair(certPEMBuf.Bytes(), keyPEMBuf.Bytes())
	require.NoError(t, err)

	return &cert
}

// generateKeyAndCert from scion-apps.
func generateKeyAndCert(t *testing.T) *tls.Certificate {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return createCertificate(t, priv)
}

func getScionPathRemoteAddress(t *testing.T, remote string) net.Addr {
	t.Helper()

	raddr, err := snet.ParseUDPAddr(remote)
	require.NoError(t, err)
	if raddr.Path.IsEmpty() {
		paths, err := pathQuerier.Query(context.Background(), raddr.IA)
		require.NoError(t, err)
		// raddr.Path.Type = 1 // scion type
		raddr.Path = paths[0].Path()
		t.Logf("Path type is %v", raddr.Path.Type)
		// BR complains about an empty path and a UDP header, TAL at the BR logs
		raddr.NextHop = paths[0].UnderlayNextHop()
		t.Logf("NextHop = %s", raddr.NextHop)
		require.Equal(t, scion.PathType, raddr.Path.Type)
		// print the SCION path
		// dec := scion.Decoded{}
		// err = dec.DecodeFromBytes(raddr.Path.Raw)
		// require.NoError(t, err)
		// t.Logf("SCION Path: %#v", dec)
	}
	require.NotNil(t, raddr.Path)
	return raddr
}

func getColibriRemoteAddress(t *testing.T, remote string) net.Addr {
	t.Helper()

	// to test, get a normal scion address first:
	scionRaddr, err := snet.ParseUDPAddr(remote)
	require.NoError(t, err)
	if scionRaddr.Path.IsEmpty() {
		paths, err := pathQuerier.Query(context.Background(), scionRaddr.IA)
		require.NoError(t, err)
		scionRaddr.Path = paths[0].Path()
		scionRaddr.NextHop = paths[0].UnderlayNextHop()
	}
	require.NotNil(t, scionRaddr.Path)
	// use the next hop from the normal address into the colibri address
	raddr := snet.UDPAddr{
		IA:   scionRaddr.IA,
		Host: scionRaddr.Host,
		Path: spath.Path{
			Raw:  createTestColibriPath(t),
			Type: colibri.PathType,
		},
		NextHop: scionRaddr.NextHop,
	}
	require.NotNil(t, raddr.Path)
	require.NotNil(t, raddr.NextHop)
	return &raddr
}

func createTestColibriPath(t *testing.T) []byte {
	t.Helper()

	path := colibri.ColibriPath{
		PacketTimestamp: 1,
		InfoField: &colibri.InfoField{
			C:           true,
			R:           false,
			S:           true,
			Ver:         1,
			CurrHF:      0,
			HFCount:     3,
			ResIdSuffix: xtest.MustParseHexString("beefcafe0000000000000000"),
			ExpTick:     1893452400, // valid until 1.1.2030
			BwCls:       7,
			Rlc:         7,
			OrigPayLen:  1208,
		},
		HopFields: []*colibri.HopField{
			{
				IngressId: 0,
				EgressId:  41,
				Mac:       []byte{140, 95, 102, 190}, // MAC is 4 bytes
			},
			{
				IngressId: 1,
				EgressId:  2,
				Mac:       []byte{0, 61, 66, 164},
			},
			{
				IngressId: 1,
				EgressId:  0,
				Mac:       xtest.MustParseHexString("00000000"),
			},
		},
	}
	buffLen := 8 + 24 + (len(path.HopFields) * 8) // timestamp + infofield + 3*hops
	buff := make([]byte, buffLen)
	err := path.SerializeTo(buff)
	require.NoError(t, err)
	return buff
}

// TODO(juagargi) border router receives packets from inside the AS in a different routine?
