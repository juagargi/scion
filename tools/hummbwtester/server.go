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
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
)

// clientIdleTimeout bounds memory: a client with no traffic for this long has its state evicted.
const clientIdleTimeout = 30 * time.Second

// serverConfig collects every value runServer needs, populated from CLI flags in main.go.
type serverConfig struct {
	local           snet.UDPAddr
	reportInterval  time.Duration
	verifyIntegrity bool
	// receiveBufferSize is applied to the listening socket before the receive loop starts.
	receiveBufferSize int
}

// clientState is the per-source-address accounting kept by the server. All access is
// serialized through the single receive-loop goroutine plus the periodic report/eviction
// ticker goroutine, both guarded by server.mu.
type clientState struct {
	addr         net.Addr
	payloadLoss  SeqLossTracker
	rate         *RateTracker
	payloadBytes uint64
	pongReqs     uint64
	pongReplies  uint64
	corrupted    uint64
	lastSeen     time.Time
}

type server struct {
	cfg serverConfig

	// pongReplyBuf is reused across calls to handlePongRequest, which only ever runs on the
	// single receiveLoop goroutine.
	pongReplyBuf []byte

	mu      sync.Mutex
	clients map[string]*clientState
}

func runServer(ctx context.Context, sn *snet.SCIONNetwork, cfg serverConfig) int {
	// HummReplyPather handles regular replies like a DefaultReplyPather, but also transparently
	// supports bidirectional Hummingbird reservations; it's harmless to always set it.
	sn.ReplyPather = snetpath.NewHummReplyPather()

	conn, err := sn.Listen(ctx, "udp", cfg.local.Host)
	if err != nil {
		log.Error("Listening", "err", err)
		return 1
	}
	defer conn.Close()
	if cfg.receiveBufferSize > 0 {
		if err := conn.SetReadBuffer(cfg.receiveBufferSize); err != nil {
			log.Error("Setting server receive buffer", "size", cfg.receiveBufferSize, "err", err)
			return 1
		}
	}
	log.Info("Server listening", "local", conn.LocalAddr())

	s := &server{
		cfg:          cfg,
		pongReplyBuf: make([]byte, PongReplyLen),
		clients:      make(map[string]*clientState),
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s.reportAndEvictLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		s.receiveLoop(ctx, conn)
	}()
	wg.Wait()

	log.Info("Server finished")
	return 0
}

// receiveLoop is the server's single receive goroutine: it reads every incoming packet,
// dispatches by PacketType, and replies to PongRequests inline (from the same iteration).
func (s *server) receiveLoop(ctx context.Context, conn *snet.Conn) {
	buf := make([]byte, PongReplyLen+4096)
	for {
		if ctx.Err() != nil {
			return
		}
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			log.Error("Setting read deadline", "err", err)
			return
		}
		n, rawAddr, err := conn.ReadFrom(buf)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var netErr net.Error
			if errors.As(err, &netErr) && netErr.Timeout() {
				continue
			}
			log.Error("Reading from conn", "err", err)
			continue
		}
		s.handlePacket(conn, rawAddr, buf[:n])
	}
}

func (s *server) handlePacket(conn *snet.Conn, from net.Addr, raw []byte) {
	h, err := DecodeHeader(raw)
	if err != nil {
		log.Error("Decoding packet header", "err", err, "from", from)
		return
	}
	key := from.String()
	now := time.Now()

	s.mu.Lock()
	cs, ok := s.clients[key]
	if !ok {
		cs = &clientState{addr: from, rate: NewRateTracker(now)}
		s.clients[key] = cs
		log.Info("New client", "client", key)
	}
	cs.lastSeen = now
	s.mu.Unlock()

	switch h.Type {
	case PacketTypePayload:
		s.handlePayload(cs, key, h, raw, now)
	case PacketTypePongRequest:
		s.handlePongRequest(conn, cs, key, h, from, now)
	default:
		log.Error("Unexpected packet type", "type", h.Type, "from", key)
	}
}

func (s *server) handlePayload(cs *clientState, key string, h Header, raw []byte, now time.Time) {
	s.mu.Lock()
	cs.payloadLoss.Received(h.SequenceNumber)
	cs.rate.Add(len(raw))
	cs.payloadBytes += uint64(len(raw))
	corrupted := s.cfg.verifyIntegrity && len(raw) > HeaderLen && !verifyFiller(raw[HeaderLen:])
	if corrupted {
		cs.corrupted++
	}
	s.mu.Unlock()

	if corrupted {
		log.Error("Payload integrity check failed", "client", key, "seq", h.SequenceNumber)
	}
}

// verifyFiller checks that filler bytes match the deterministic pattern the client generates,
// as an optional integrity check (design doc section 2).
func verifyFiller(got []byte) bool {
	want := make([]byte, len(got))
	filler(want, fillerSeed)
	return bytes.Equal(got, want)
}

func (s *server) handlePongRequest(
	conn *snet.Conn, cs *clientState, key string, h Header, from net.Addr, recvTime time.Time,
) {
	s.mu.Lock()
	cs.pongReqs++
	_, payloadPackets, payloadLost, payloadOutOfOrder := cs.payloadLoss.Stats()
	snapshot := PongReply{
		Header: Header{
			Type:               PacketTypePongReply,
			SequenceNumber:     h.SequenceNumber,
			SendTimestampNanos: h.SendTimestampNanos,
		},
		ServerRecvTimestampNanos: recvTime.UnixNano(),
		PayloadPacketsReceived:   payloadPackets,
		PayloadBytesReceived:     cs.payloadBytes,
		PayloadLost:              payloadLost,
		PayloadOutOfOrder:        payloadOutOfOrder,
		PongRequestsReceived:     cs.pongReqs,
		// Count this reply in the advertised snapshot. The count is committed below only
		// after WriteTo succeeds, so every snapshot a client receives is self-inclusive.
		PongRepliesSent: cs.pongReplies + 1,
	}
	s.mu.Unlock()

	sendTime := time.Now()
	snapshot.ServerSendTimestampNanos = sendTime.UnixNano()
	EncodePongReply(s.pongReplyBuf, snapshot)
	if _, err := conn.WriteTo(s.pongReplyBuf, from); err != nil {
		log.Error("Sending pong reply", "client", key, "err", err)
		return
	}
	s.mu.Lock()
	cs.pongReplies++
	s.mu.Unlock()
}

// reportAndEvictLoop prints periodic per-client and aggregate reports and evicts clients that
// have been idle too long.
func (s *server) reportAndEvictLoop(ctx context.Context) {
	ticker := time.NewTicker(s.cfg.reportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			s.reportAndEvict(now)
		}
	}
}

func (s *server) reportAndEvict(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var aggBytes uint64
	for key, cs := range s.clients {
		if now.Sub(cs.lastSeen) > clientIdleTimeout {
			delete(s.clients, key)
			log.Info("Evicted idle client", "client", key)
			continue
		}
		res := cs.rate.Snapshot(now)
		expected, received, lost, outOfOrder := cs.payloadLoss.Stats()
		aggBytes += res.Bytes
		fmt.Printf(
			"[server] client=%s interval_bytes=%d rate=%.2f Mbps expected=%d received=%d "+
				"lost=%d out_of_order=%d pong_reqs=%d pong_replies=%d corrupted=%d\n",
			key, res.Bytes, res.BitsPerSec/1e6, expected, received, lost, outOfOrder,
			cs.pongReqs, cs.pongReplies, cs.corrupted)
	}
	if len(s.clients) > 0 {
		fmt.Printf("[server] aggregate: clients=%d interval_bytes=%d rate=%.2f Mbps\n",
			len(s.clients), aggBytes, float64(aggBytes)*8/s.cfg.reportInterval.Seconds()/1e6)
	}
}
