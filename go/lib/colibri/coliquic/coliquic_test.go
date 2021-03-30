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
	"sync"
	"testing"
	"time"

	"github.com/lucas-clemente/quic-go"
	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/go/lib/slayers/path/colibri"
	"github.com/scionproto/scion/go/lib/snet"
	"github.com/scionproto/scion/go/lib/spath"
	"github.com/scionproto/scion/go/lib/xtest"
)

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

func mockColibriAddress(ia string, host *net.UDPAddr) net.Addr {
	return &snet.UDPAddr{
		IA:   xtest.MustParseIA(ia),
		Host: host,
		Path: spath.Path{
			Raw:  []byte{0, 0},
			Type: colibri.PathType,
		},
	}
}

type connMock struct {
	localAddr net.Addr
	net       *network
}

var _ net.PacketConn = (*connMock)(nil)

func NewConnMock(localAddr net.Addr, network *network) *connMock {
	if network == nil {
		panic("network is nil")
	}
	return &connMock{
		localAddr: localAddr,
		net:       network,
	}
}

func (c *connMock) LocalAddr() net.Addr {
	return c.localAddr
}

func (c *connMock) Close() error {
	return nil
}

func (c *connMock) ReadFrom(p []byte) (int, net.Addr, error) {
	b, sender := c.net.ReadFrom(c.localAddr)
	n := copy(p, b)
	return n, sender, nil
}

func (c *connMock) WriteTo(p []byte, addr net.Addr) (int, error) {
	c.net.WriteTo(c.localAddr, addr, p)
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

func TestDeleteme(t *testing.T) {
	thisNet := NewNetwork()
	// server:
	serverLocalAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 43210, Zone: ""}
	serverAddr := mockColibriAddress("1-ff00:0:111", serverLocalAddr)
	serverTlsConfig := &tls.Config{
		Certificates: []tls.Certificate{*generateKeyAndCert(t)},
		NextProtos:   []string{"netcat"},
	}
	serverQuicConfig := &quic.Config{KeepAlive: true}
	listener, err := quic.Listen(NewConnMock(serverAddr, thisNet), serverTlsConfig, serverQuicConfig)
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
	clientLocalAddr := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 12345, Zone: ""}
	clientAddr := mockColibriAddress("1-ff00:0:112", clientLocalAddr)
	clientTlsConfig := &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"netcat"},
	}
	clientQuicConfig := &quic.Config{KeepAlive: true}

	ctx2, cancelF2 := context.WithTimeout(context.Background(), 9*time.Hour)
	defer cancelF2()
	session, err := quic.DialContext(ctx2, NewConnMock(clientAddr, thisNet), serverAddr, "serverName",
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
