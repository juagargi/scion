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

//go:build linux

package conn

import (
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/ipv4"
)

// x/net returns the raw return value of recvmmsg/sendmmsg, i.e. -1, when the call fails. The
// callers slice with the returned count, so the Conn implementations must never expose it.
//
// For efficiency reasons, we don't want to expose an interface to replace the ipv4 and ipv6
// PacketConn objects, so we force the real socket to return an error.

type batchTestFamily struct {
	name     string
	loopback netip.Addr
}

var batchTestFamilies = []batchTestFamily{
	{name: "IPv4", loopback: netip.MustParseAddr("127.0.0.1")},
	{name: "IPv6", loopback: netip.MustParseAddr("::1")},
}

func TestWriteBatchNeverNegative(t *testing.T) {
	for _, f := range batchTestFamilies {
		t.Run(f.name, func(t *testing.T) {
			c := newTestConn(t, f, netip.AddrPort{})

			// A datagram larger than the maximum UDP payload fails in sendmmsg with EMSGSIZE.
			msgs := Messages{{
				Buffers: [][]byte{make([]byte, 70000)},
				Addr:    net.UDPAddrFromAddrPort(netip.AddrPortFrom(f.loopback, 9)),
			}}
			// Sanity check: the underlying library does report -1 for this failure.
			raw, rawErr := rawWriteBatch(t, c, msgs)
			require.Error(t, rawErr)
			require.Equal(t, -1, raw)

			n, err := c.WriteBatch(msgs, 0)
			require.Error(t, err)
			require.GreaterOrEqual(t, n, 0)
		})
	}
}

func TestReadBatchNeverNegative(t *testing.T) {
	for _, f := range batchTestFamilies {
		t.Run(f.name, func(t *testing.T) {
			// Find a local port nobody listens on.
			l, err := net.ListenUDP("udp",
				net.UDPAddrFromAddrPort(netip.AddrPortFrom(f.loopback, 0)))
			if err != nil && f.loopback.Is6() {
				t.Skipf("IPv6 loopback not available: %v", err)
			}
			require.NoError(t, err)
			remote := l.LocalAddr().(*net.UDPAddr).AddrPort()
			require.NoError(t, l.Close())

			// On a connected socket, the ICMP port unreachable answering a sent packet makes the
			// next recvmmsg fail with ECONNREFUSED. Closed sockets and expired deadlines don't
			// work for this: Go's poller reports those without ever calling recvmmsg, so x/net
			// returns 0.
			c := newTestConn(t, f, remote)
			ping := func() {
				sent, err := c.WriteBatch(Messages{{Buffers: [][]byte{[]byte("ping")}}}, 0)
				require.NoError(t, err)
				require.Equal(t, 1, sent)
			}
			ping()
			// The ICMP error is delivered asynchronously, but within the loopback it is
			// immediate.
			require.NoError(t, c.SetReadDeadline(time.Now().Add(time.Second)))

			msgs := NewReadMessages(2)
			for i := range msgs {
				msgs[i].Buffers = [][]byte{make([]byte, 100)}
			}
			// Sanity check: the underlying library does report -1 for this failure.
			raw, rawErr := rawReadBatch(t, c, msgs)
			require.ErrorIs(t, rawErr, syscall.ECONNREFUSED)
			require.Equal(t, -1, raw)

			// The error is consumed by the read above, so provoke it again.
			ping()
			n, err := c.ReadBatch(msgs)
			require.ErrorIs(t, err, syscall.ECONNREFUSED)
			require.GreaterOrEqual(t, n, 0)
		})
	}
}

// newTestConn opens a socket on the loopback of the family, or skips the test if the host has no
// such loopback.
func newTestConn(t *testing.T, f batchTestFamily, remote netip.AddrPort) Conn {
	t.Helper()
	c, err := New(netip.AddrPortFrom(f.loopback, 0), remote, &Config{})
	if err != nil && f.loopback.Is6() {
		t.Skipf("IPv6 loopback not available: %v", err)
	}
	require.NoError(t, err)
	t.Cleanup(func() { c.Close() })
	return c
}

// rawWriteBatch calls the x/net WriteBatch, bypassing our wrapper.
func rawWriteBatch(t *testing.T, c Conn, msgs Messages) (int, error) {
	t.Helper()
	switch cc := c.(type) {
	case *connUDPIPv4:
		return cc.pconn.WriteBatch([]ipv4.Message(msgs), 0)
	case *connUDPIPv6:
		return cc.pconn.WriteBatch([]ipv4.Message(msgs), 0)
	}
	require.FailNow(t, "unexpected connection type")
	return 0, nil
}

// rawReadBatch calls the x/net ReadBatch, bypassing our wrapper.
func rawReadBatch(t *testing.T, c Conn, msgs Messages) (int, error) {
	t.Helper()
	switch cc := c.(type) {
	case *connUDPIPv4:
		return cc.pconn.ReadBatch([]ipv4.Message(msgs), syscallMSG_WAITFORONE)
	case *connUDPIPv6:
		return cc.pconn.ReadBatch([]ipv4.Message(msgs), syscallMSG_WAITFORONE)
	}
	require.FailNow(t, "unexpected connection type")
	return 0, nil
}
