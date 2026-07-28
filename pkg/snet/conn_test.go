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

package snet

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestConnSetReadBuffer(t *testing.T) {
	packetConn := &readBufferPacketConn{}
	conn := &Conn{conn: packetConn}

	require.NoError(t, conn.SetReadBuffer(4194304))
	require.Equal(t, 4194304, packetConn.size)
}

func TestConnSetReadBufferUnsupported(t *testing.T) {
	conn := &Conn{conn: &unsupportedReadBufferPacketConn{}}
	require.Error(t, conn.SetReadBuffer(4194304))
}

type readBufferPacketConn struct {
	PacketConn
	size int
}

func (c *readBufferPacketConn) SetReadBuffer(size int) error {
	c.size = size
	return nil
}

type unsupportedReadBufferPacketConn struct {
	PacketConn
}
