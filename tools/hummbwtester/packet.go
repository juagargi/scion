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
	"encoding/binary"

	"github.com/scionproto/scion/pkg/private/serrors"
)

// PacketType identifies the role of a packet on the wire.
type PacketType uint8

const (
	// PacketTypePayload is a bandwidth-test payload packet, client -> server.
	PacketTypePayload PacketType = 1
	// PacketTypePongRequest is a latency probe, client -> server.
	PacketTypePongRequest PacketType = 2
	// PacketTypePongReply is the server's echo of a PongRequest, server -> client.
	PacketTypePongReply PacketType = 3
)

// Version is the wire-format version. It changes whenever the header layout below changes,
// so that a client and server running mismatched builds fail loudly instead of misparsing.
const Version = 2

// HeaderLen is the size in bytes of the fixed header shared by all packet types.
const HeaderLen = 24

// PongReplyExtraLen is the size in bytes of the fields appended after the fixed header on a
// PongReply packet: two timestamps and six cumulative server-observation counters.
const PongReplyExtraLen = 64

// PongReplyLen is the total size in bytes of a PongReply packet.
const PongReplyLen = HeaderLen + PongReplyExtraLen

// Header is the fixed 24-byte header carried by every packet.
type Header struct {
	Type               PacketType
	SequenceNumber     uint64
	SendTimestampNanos int64
}

// EncodeHeader writes h into the first HeaderLen bytes of buf. buf must be at least HeaderLen
// bytes long.
func EncodeHeader(buf []byte, h Header) {
	buf[0] = Version
	buf[1] = byte(h.Type)
	buf[2], buf[3], buf[4], buf[5], buf[6], buf[7] = 0, 0, 0, 0, 0, 0
	binary.BigEndian.PutUint64(buf[8:16], h.SequenceNumber)
	binary.BigEndian.PutUint64(buf[16:24], uint64(h.SendTimestampNanos))
}

// DecodeHeader reads a Header from the first HeaderLen bytes of buf.
func DecodeHeader(buf []byte) (Header, error) {
	if len(buf) < HeaderLen {
		return Header{}, serrors.New("packet too short for header",
			"len", len(buf), "expected", HeaderLen)
	}
	if buf[0] != Version {
		return Header{}, serrors.New("version mismatch",
			"got", buf[0], "expected", Version)
	}
	return Header{
		Type:               PacketType(buf[1]),
		SequenceNumber:     binary.BigEndian.Uint64(buf[8:16]),
		SendTimestampNanos: int64(binary.BigEndian.Uint64(buf[16:24])),
	}, nil
}

// PongReply carries a PongReply packet's fields, header included.
type PongReply struct {
	Header
	ServerRecvTimestampNanos int64
	ServerSendTimestampNanos int64
	PayloadPacketsReceived   uint64
	PayloadBytesReceived     uint64
	PayloadLost              uint64
	PayloadOutOfOrder        uint64
	PongRequestsReceived     uint64
	PongRepliesSent          uint64
}

// EncodePongReply writes reply into buf, which must be at least PongReplyLen bytes long.
func EncodePongReply(buf []byte, reply PongReply) {
	EncodeHeader(buf, reply.Header)
	binary.BigEndian.PutUint64(buf[24:32], uint64(reply.ServerRecvTimestampNanos))
	binary.BigEndian.PutUint64(buf[32:40], uint64(reply.ServerSendTimestampNanos))
	binary.BigEndian.PutUint64(buf[40:48], reply.PayloadPacketsReceived)
	binary.BigEndian.PutUint64(buf[48:56], reply.PayloadBytesReceived)
	binary.BigEndian.PutUint64(buf[56:64], reply.PayloadLost)
	binary.BigEndian.PutUint64(buf[64:72], reply.PayloadOutOfOrder)
	binary.BigEndian.PutUint64(buf[72:80], reply.PongRequestsReceived)
	binary.BigEndian.PutUint64(buf[80:88], reply.PongRepliesSent)
}

// DecodePongReply reads a PongReply from buf.
func DecodePongReply(buf []byte) (PongReply, error) {
	if len(buf) < PongReplyLen {
		return PongReply{}, serrors.New("packet too short for pong reply",
			"len", len(buf), "expected", PongReplyLen)
	}
	h, err := DecodeHeader(buf)
	if err != nil {
		return PongReply{}, err
	}
	if h.Type != PacketTypePongReply {
		return PongReply{}, serrors.New("unexpected packet type for pong reply", "type", h.Type)
	}
	return PongReply{
		Header:                   h,
		ServerRecvTimestampNanos: int64(binary.BigEndian.Uint64(buf[24:32])),
		ServerSendTimestampNanos: int64(binary.BigEndian.Uint64(buf[32:40])),
		PayloadPacketsReceived:   binary.BigEndian.Uint64(buf[40:48]),
		PayloadBytesReceived:     binary.BigEndian.Uint64(buf[48:56]),
		PayloadLost:              binary.BigEndian.Uint64(buf[56:64]),
		PayloadOutOfOrder:        binary.BigEndian.Uint64(buf[64:72]),
		PongRequestsReceived:     binary.BigEndian.Uint64(buf[72:80]),
		PongRepliesSent:          binary.BigEndian.Uint64(buf[80:88]),
	}, nil
}

// filler deterministically fills buf with non-zero pseudo-random bytes derived from seed, so
// that repeated runs with the same seed are reproducible and a receiver can optionally verify
// payload integrity.
func filler(buf []byte, seed uint64) {
	state := seed | 1 // xorshift64 requires a non-zero state.
	for i := range buf {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		b := byte(state)
		if b == 0 {
			b = 1 // Keep filler bytes non-zero, per design.
		}
		buf[i] = b
	}
}

// EncodePayload writes a Payload packet of exactly len(buf) bytes (buf must be at least
// HeaderLen bytes long). Bytes after the header are deterministic filler derived from seed.
func EncodePayload(buf []byte, seq uint64, sendTimeNanos int64, seed uint64) {
	EncodeHeader(buf, Header{
		Type:               PacketTypePayload,
		SequenceNumber:     seq,
		SendTimestampNanos: sendTimeNanos,
	})
	if len(buf) > HeaderLen {
		filler(buf[HeaderLen:], seed)
	}
}

// EncodePongRequest writes a PongRequest packet into buf, which must be at least HeaderLen
// bytes long.
func EncodePongRequest(buf []byte, seq uint64, sendTimeNanos int64) {
	EncodeHeader(buf, Header{
		Type:               PacketTypePongRequest,
		SequenceNumber:     seq,
		SendTimestampNanos: sendTimeNanos,
	})
}
