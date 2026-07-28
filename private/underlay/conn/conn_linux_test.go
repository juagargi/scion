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
	"encoding/binary"
	"math"
	"testing"
	"unsafe"

	"github.com/stretchr/testify/require"
	"golang.org/x/net/ipv4"
	"golang.org/x/sys/unix"
)

func TestRecordReceiveOverflow(t *testing.T) {
	c := &connUDPBase{}
	// SO_RXQ_OVFL values are cumulative. Repeating 5 must not add another five drops, and the
	// following value of 9 must add only the four newly reported drops.
	c.recordReceiveOverflow(Messages{
		receiveOverflowMessage(5),
		receiveOverflowMessage(5),
		receiveOverflowMessage(9),
	})

	total, supported := c.ReceiveOverflow()
	require.True(t, supported)
	require.Equal(t, uint64(9), total)
}

func TestRecordReceiveOverflowWraps(t *testing.T) {
	// Moving from MaxUint32-2 to 1 crosses MaxUint32 and represents four new drops. Seed the
	// extended total independently to verify that only this wrapped delta is added.
	c := &connUDPBase{lastReceiveOverflow: math.MaxUint32 - 2}
	c.receiveOverflow.Store(100)
	c.recordReceiveOverflow(Messages{receiveOverflowMessage(1)})

	total, supported := c.ReceiveOverflow()
	require.True(t, supported)
	require.Equal(t, uint64(104), total)
}

func TestNewReadMessagesAllocatesOverflowControlBuffer(t *testing.T) {
	msgs := NewReadMessages(2)
	require.Len(t, msgs, 2)
	for _, msg := range msgs {
		// CmsgSpace includes the cmsghdr, its uint32 payload, and platform-required alignment.
		require.GreaterOrEqual(t, len(msg.OOB), unix.CmsgSpace(4))
	}
}

// receiveOverflowMessage constructs the ancillary data that Linux supplies when SO_RXQ_OVFL is
// enabled. It lets the parser be tested without relying on timing-sensitive real socket overflow.
func receiveOverflowMessage(value uint32) ipv4.Message {
	oob := make([]byte, unix.CmsgSpace(4))
	// A socket control message starts with a native cmsghdr. The unsafe conversion is limited to
	// this test helper and mirrors the kernel layout parsed by unix.ParseSocketControlMessage.
	header := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
	header.Level = unix.SOL_SOCKET
	header.Type = unix.SO_RXQ_OVFL
	header.SetLen(unix.CmsgLen(4))
	// Control-message integers use native byte order, and CmsgLen(0) points just past the header.
	binary.NativeEndian.PutUint32(oob[unix.CmsgLen(0):], value)
	// NN is the number of valid ancillary bytes returned by a receive call. Padding up to
	// CmsgSpace(4) remains allocated but is not part of the parsed message.
	return ipv4.Message{OOB: oob, NN: unix.CmsgLen(4)}
}
