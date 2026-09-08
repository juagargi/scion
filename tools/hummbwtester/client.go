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
	"context"
	"crypto/aes"
	"crypto/cipher"
	cryptorand "crypto/rand"
	"errors"
	"fmt"
	"math"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/daemon"
	daemontypes "github.com/scionproto/scion/pkg/daemon/types"
	humm "github.com/scionproto/scion/pkg/hummingbird"
	marketclient "github.com/scionproto/scion/pkg/hummingbird/marketplace"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	hummlib "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/pkg/snet"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
	"github.com/scionproto/scion/private/keyconf"
)

// cryptoRandRead is replaceable by tests. Reservation IDs are local client state, rather than a
// command-line setting, so independently launched clients do not share a token bucket.
var cryptoRandRead = cryptorand.Read

func randomHummReservationID() (uint32, error) {
	var buf [4]byte
	for {
		if _, err := cryptoRandRead(buf[:]); err != nil {
			return 0, err
		}
		id := uint32(buf[0])<<16 | uint32(buf[1])<<8 | uint32(buf[2])
		id &= maxHummReservationID
		if id != 0 {
			return id, nil
		}
	}
}

const (
	maxHummReservationID = (1 << 22) - 1

	// fillerSeed is the fixed seed used to generate the deterministic payload filler pattern,
	// known to both client and server so the server can optionally verify integrity.
	fillerSeed = 0xC0FFEE1234ABCDEF

	// payloadPacingCadence is deliberately coarser than typical.
	// Each wake sends a bounded batch based on the absolute payload schedule.
	payloadPacingCadence = time.Millisecond

	// bidirectionalFirstPacketLen is the total packet size (header + filler) used for the one
	// Payload packet sent immediately after attaching a bidirectional reservation
	// (initial dial or renewal), since that packet also carries the reverse-reservation
	// E2E extension option and must stay small.
	// The variable is used only to precompute the size of the first packet.
	bidirectionalFirstPacketLen = HeaderLen + bidirectionalFirstPacketPayload
)

// clientConfig collects every value runClient needs, populated from CLI flags in main.go.
type clientConfig struct {
	local  snet.UDPAddr
	remote snet.UDPAddr
	sdConn daemon.Connector

	bandwidthBps       float64
	maxBurstBps        float64
	duration           time.Duration
	payloadSize        int
	pongRateHz         float64
	humm               hummingbirdParameters
	hummEnabled        bool
	hummReservationID  uint32
	hummKeysDir        string
	marketplaceJWT     string
	reportInterval     time.Duration
	renewalAhead       time.Duration
	reservationOverlap time.Duration
	hummStartOffset    time.Duration
}

// bidirectional reports whether the client requested a reverse-direction reservation.
func (c clientConfig) bidirectional() bool {
	return c.humm.ReverseBw > 0
}

// client holds the mutable state of one client run.
type client struct {
	cfg     clientConfig
	sn      *snet.SCIONNetwork
	metrics *clientMetrics

	svMu       sync.Mutex
	hummSVByIA map[addr.IA][]byte

	// currentAddr is the *snet.UDPAddr (including the current DataplanePath) that the sender
	// loop must use for the next send. It is swapped atomically by the renewal goroutine;
	// the sender never mutates the reservation it points to in place.
	currentAddr atomic.Pointer[snet.UDPAddr]
	// forceSmallNextPayload is set whenever a new reservation carrying a pending
	// reverse-reservation E2E extension has just been published, and consumed (cleared) by the
	// very next Payload send, which must then be capped to bidirectionalFirstPacketLen.
	forceSmallNextPayload atomic.Bool

	rateMu sync.Mutex
	rate   *RateTracker
}

func runClient(ctx context.Context, sn *snet.SCIONNetwork, cfg clientConfig) int {
	c := &client{
		cfg:        cfg,
		sn:         sn,
		metrics:    newClientMetrics(),
		hummSVByIA: make(map[addr.IA][]byte),
		rate:       NewRateTracker(time.Now()),
	}
	c.metrics.remoteStatsAge.Set(-1)

	path, err := selectPath(ctx, cfg.sdConn, cfg.local.IA, cfg.remote.IA)
	if err != nil {
		log.Error("Selecting path", "err", err)
		return 1
	}

	var remoteAddr *snet.UDPAddr
	var reservation *snetpath.Reservation
	if cfg.hummEnabled {
		var nextHop *net.UDPAddr
		startTime := time.Now().Add(c.cfg.hummStartOffset)
		marketRoundtripStart := time.Now()
		reservation, nextHop, err = c.buildReservation(ctx, path, startTime)
		if err != nil {
			log.Error("Building initial Hummingbird reservation", "err", err)
			return 1
		}
		c.observeMarketRoundtrip(marketRoundtripStart)
		remoteAddr = &snet.UDPAddr{
			IA:      cfg.remote.IA,
			Host:    cfg.remote.Host,
			Path:    reservation,
			NextHop: nextHop,
		}
	} else {
		remoteAddr = &snet.UDPAddr{
			IA:      cfg.remote.IA,
			Host:    cfg.remote.Host,
			Path:    path.Dataplane(),
			NextHop: path.UnderlayNextHop(),
		}
	}
	c.currentAddr.Store(remoteAddr)
	c.forceSmallNextPayload.Store(c.cfg.bidirectional())

	conn, err := sn.Dial(ctx, "udp", cfg.local.Host, remoteAddr)
	if err != nil {
		log.Error("Dialing", "err", err)
		return 1
	}
	defer conn.Close()

	log.Info("Client started",
		"local", cfg.local, "remote", cfg.remote,
		"bandwidth_bps", cfg.bandwidthBps, "maxburst_bps", cfg.maxBurstBps,
		"payload_size", cfg.payloadSize,
		"pong_rate_hz", cfg.pongRateHz, "hummingbird_enabled", cfg.hummEnabled,
		"bidirectional", cfg.bidirectional(), "hummingbird_reservation_id", cfg.hummReservationID)

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()

	var wg sync.WaitGroup
	tracker := newPongTracker()

	if cfg.hummEnabled {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.renewalLoop(runCtx, path, reservation.Expiry())
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.pongReceiveLoop(runCtx, conn, tracker)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		c.reportLoop(runCtx, tracker)
	}()

	c.sendLoop(ctx, conn, tracker)

	// Sending is done (duration elapsed or ctx cancelled). Give in-flight pong replies a grace
	// period to arrive before tearing down the receiver/renewal goroutines.
	grace, cancelGrace := context.WithTimeout(context.Background(), 2*time.Second)
	select {
	case <-ctx.Done():
	case <-grace.Done():
	}
	cancelGrace()
	cancelRun()
	wg.Wait()

	log.Info("Client finished")
	return 0
}

// selectPath queries the daemon for paths to dst and returns the first candidate.
func selectPath(
	ctx context.Context, sdConn daemon.Connector, src, dst addr.IA,
) (snet.Path, error) {
	paths, err := sdConn.Paths(ctx, dst, src, daemontypes.PathReqFlags{})
	if err != nil {
		return nil, serrors.Wrap("requesting paths", err)
	}
	if len(paths) == 0 {
		return nil, serrors.New("no path found", "src", src, "dst", dst)
	}
	return paths[0], nil
}

// buildReservation obtains a fresh forward (and, if configured, reverse) Hummingbird
// reservation for path, either from local AS master keys or from the marketplace.
func (c *client) buildReservation(
	ctx context.Context,
	path snet.Path,
	startTime time.Time,
) (*snetpath.Reservation, *net.UDPAddr, error) {
	if c.cfg.hummKeysDir != "" {
		rsv, err := c.buildReservationWithSecretValues(path, startTime)
		return rsv, path.UnderlayNextHop(), err
	}
	rsv, err := c.buildReservationWithMarketplace(ctx, path, startTime)
	if err != nil {
		return nil, nil, err
	}
	return rsv, path.UnderlayNextHop(), nil
}

func (c *client) buildReservationWithMarketplace(
	ctx context.Context, path snet.Path, startTime time.Time,
) (*snetpath.Reservation, error) {
	startsAt := startTime.Truncate(time.Second)
	stopsAt := startsAt.Add(time.Duration(c.cfg.humm.Duration) * time.Second)
	querier := daemon.Querier{Connector: c.cfg.sdConn, IA: c.sn.Topology.LocalIA}
	return marketclient.OneShotReservation(
		ctx,
		path,
		c.cfg.marketplaceJWT,
		querier,
		c.sn.Topology,
		marketplaceInsecure,
		c.cfg.humm.Bw,
		c.cfg.humm.ReverseBw,
		startsAt,
		stopsAt,
		marketplaceMaxPrice,
		marketplaceBuyMode,
		marketplaceFetchReservations,
		marketplaceCombineAssets,
		marketplaceRetries,
	)
}

func (c *client) buildReservationWithSecretValues(
	path snet.Path,
	startTime time.Time,
) (*snetpath.Reservation, error) {
	baseHops := snetpath.InterfacesToBaseHops(path.Metadata().Interfaces)
	scionPath, ok := path.Dataplane().(snetpath.SCION)
	if !ok {
		return nil, serrors.New("provided path must be of type scion")
	}
	flyovers, err := c.deriveFlyoversFromSecretValues(baseHops, c.cfg.humm.Bw, startTime)
	if err != nil {
		return nil, err
	}
	reservation, err := snetpath.NewReservation(
		snetpath.WithDataplanePath(scionPath, path.Destination(), flyovers),
	)
	if err != nil || c.cfg.humm.ReverseBw == 0 {
		return reservation, err
	}
	reverseFlyovers, err := c.deriveFlyoversFromSecretValues(
		reverseBaseHops(baseHops), c.cfg.humm.ReverseBw, startTime)
	if err != nil {
		return nil, err
	}
	extn, err := humm.BuildReverseReservationExtn(scionPath, path.Source(), reverseFlyovers)
	if err != nil {
		return nil, err
	}
	reservation.SetReverseReservationExtn(extn)
	return reservation, nil
}

func (c *client) hummSecretValue(ia addr.IA) ([]byte, error) {
	c.svMu.Lock()
	defer c.svMu.Unlock()
	if sv, ok := c.hummSVByIA[ia]; ok {
		return sv, nil
	}
	asDir := addr.FormatAS(ia.AS(), addr.WithDefaultPrefix(), addr.WithFileSeparator())
	keysDir := filepath.Join(c.cfg.hummKeysDir, asDir, "keys")
	master, err := keyconf.LoadMaster(keysDir)
	if err != nil {
		return nil, serrors.Wrap("loading humm master key", err, "ia", ia, "dir", keysDir)
	}
	sv := hummlib.DeriveSecretValue(master.Key0)
	c.hummSVByIA[ia] = sv
	return sv, nil
}

func (c *client) deriveFlyoversFromSecretValues(
	baseHops []snetpath.BaseHop,
	bandwidth uint32,
	start time.Time,
) ([]*snetpath.Hop, error) {
	flyovers := make([]*snetpath.Hop, 0, len(baseHops))
	startTime := uint32(start.Unix())
	aesByIA := make(map[addr.IA]cipher.Block)
	buffer := make([]byte, hummlib.AkBufferSize)

	for _, baseHop := range baseHops {
		block, ok := aesByIA[baseHop.IA]
		if !ok {
			sv, err := c.hummSecretValue(baseHop.IA)
			if err != nil {
				return nil, err
			}
			block, err = aes.NewCipher(sv)
			if err != nil {
				return nil, serrors.Wrap("creating aes cipher", err, "ia", baseHop.IA)
			}
			aesByIA[baseHop.IA] = block
		}
		akRaw := hummlib.DeriveAuthKey(
			block, c.cfg.hummReservationID, uint16(bandwidth), baseHop.Ingress, baseHop.Egress,
			startTime, c.cfg.humm.Duration, buffer)
		var ak [hummlib.AkBufferSize]byte
		copy(ak[:], akRaw)
		flyovers = append(flyovers, &snetpath.Hop{
			BaseHop: baseHop,
			Flyover: &snetpath.FlyoverData{
				ResID:     c.cfg.hummReservationID,
				Ak:        ak,
				Bw:        uint16(bandwidth),
				StartTime: startTime,
				Duration:  c.cfg.humm.Duration,
			},
		})
	}
	return flyovers, nil
}

func reverseBaseHops(hops []snetpath.BaseHop) []snetpath.BaseHop {
	reversed := make([]snetpath.BaseHop, len(hops))
	for i, hop := range hops {
		reversed[len(hops)-1-i] = snetpath.BaseHop{
			IA:      hop.IA,
			Ingress: hop.Egress,
			Egress:  hop.Ingress,
		}
	}
	return reversed
}

// renewalLoop obtains each replacement ahead of its handover. The configured start offset is
// applied to the handover time, but the replacement is not published until the handover.
func (c *client) renewalLoop(runCtx context.Context, path snet.Path, expiry time.Time) {
	renewAt, handoverAt, nextStart := renewalSchedule(
		expiry, c.cfg.renewalAhead, c.cfg.reservationOverlap, c.cfg.hummStartOffset)

	for {
		c.metrics.reservationExpiry.Set(time.Until(expiry).Seconds())
		select {
		case <-runCtx.Done():
			return
		case <-time.After(time.Until(renewAt)):
		}
		if runCtx.Err() != nil {
			return
		}

		marketRoundtripStart := time.Now()
		newRsv, newNextHop, ok := c.renewWithRetry(runCtx, path, nextStart)
		if !ok {
			// Exhausted retries for this window; keep sending on the old reservation and try
			// again shortly, until it actually expires.
			if time.Now().After(expiry) {
				log.Error("Reservation expired without a successful renewal")
				return
			}
			renewAt = time.Now().Add(1 * time.Second)
			continue
		}
		c.observeMarketRoundtrip(marketRoundtripStart)

		select {
		case <-runCtx.Done():
			return
		case <-time.After(time.Until(handoverAt)):
		}

		old := c.currentAddr.Load()
		newAddr := &snet.UDPAddr{
			IA:      old.IA,
			Host:    old.Host,
			Path:    newRsv,
			NextHop: newNextHop,
		}
		c.currentAddr.Store(newAddr)
		if c.cfg.bidirectional() {
			c.forceSmallNextPayload.Store(true)
		}
		expiry = newRsv.Expiry()
		renewAt, handoverAt, nextStart = renewalSchedule(
			expiry, c.cfg.renewalAhead, c.cfg.reservationOverlap, c.cfg.hummStartOffset)
		log.Info("Renewed Hummingbird reservation", "new_expiry", expiry)
	}
}

// observeMarketRoundtrip records a successful marketplace acquisition. Key-derived reservations
// do not perform a marketplace roundtrip, so they are intentionally omitted from this metric.
func (c *client) observeMarketRoundtrip(start time.Time) {
	if c.cfg.hummKeysDir == "" {
		seconds := time.Since(start).Seconds()
		c.metrics.marketRoundtripLast.Set(seconds)
	}
}

func renewalSchedule(
	expiry time.Time,
	ahead, overlap, startOffset time.Duration,
) (requestAt, handoverAt, startAt time.Time) {
	requestAt = expiry.Add(-ahead)
	handoverAt = expiry.Add(-overlap)
	startAt = handoverAt.Add(startOffset)
	return requestAt, handoverAt, startAt
}

// renewWithRetry attempts to build a fresh reservation with a small bounded number of retries
// and exponential backoff, reporting the outcome via metrics.
func (c *client) renewWithRetry(
	runCtx context.Context, path snet.Path, startTime time.Time,
) (*snetpath.Reservation, *net.UDPAddr, bool) {
	const maxAttempts = 5
	backoff := 500 * time.Millisecond
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if runCtx.Err() != nil {
			return nil, nil, false
		}
		ctx, cancel := context.WithTimeout(runCtx, 5*time.Second)
		rsv, nextHop, err := c.buildReservation(ctx, path, startTime)
		cancel()
		if err == nil {
			return rsv, nextHop, true
		}
		log.Error("Renewing Hummingbird reservation failed",
			"attempt", attempt+1, "err", err, "time_until_start", time.Until(startTime))
		select {
		case <-runCtx.Done():
			return nil, nil, false
		case <-time.After(backoff):
		}
		backoff *= 2
	}
	return nil, nil, false
}

// pongTracker tracks outstanding pong requests awaiting a reply,
// evicting (and counting as lost) any that go unanswered for too long.
type pongTracker struct {
	mu          sync.Mutex
	outstanding map[uint64]time.Time
	lastRTT     time.Duration
	startedAt   time.Time
	jitter      JitterEstimator
	remote      remoteStatsTracker
}

func newPongTracker() *pongTracker {
	now := time.Now()
	return &pongTracker{
		outstanding: make(map[uint64]time.Time),
		lastRTT:     500 * time.Millisecond,
		startedAt:   now,
	}
}

func (t *pongTracker) recordSent(seq uint64, now time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.outstanding[seq] = now
}

// recordReplied calculates RTT for an accepted reply. On-time replies use the retained monotonic
// send time. If the timeout sweep already removed that entry, the echoed client timestamp still
// lets a late reply update latency and jitter; late reports whether that fallback was necessary.
func (t *pongTracker) recordReplied(
	reply PongReply, receivedAt time.Time,
) (rtt time.Duration, late bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	sentAt, ok := t.outstanding[reply.SequenceNumber]
	if !ok {
		sentAt = time.Unix(0, reply.SendTimestampNanos)
		late = true
	} else {
		delete(t.outstanding, reply.SequenceNumber)
	}
	rtt = receivedAt.Sub(sentAt)
	t.lastRTT = rtt
	t.jitter.Sample(sentAt.Sub(t.startedAt), receivedAt.Sub(t.startedAt))
	return rtt, late
}

// evictTimedOut removes and returns the count of outstanding requests older than the current
// timeout (a small multiple of the last observed RTT, with a floor).
func (t *pongTracker) evictTimedOut(now time.Time) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	timeout := 4 * t.lastRTT
	if timeout < time.Second {
		timeout = time.Second
	}
	lost := 0
	for seq, sentAt := range t.outstanding {
		if now.Sub(sentAt) > timeout {
			delete(t.outstanding, seq)
			lost++
		}
	}
	return lost
}

// advanceProbeDeadline advances the independent Pong probe schedule. Probes do not contribute to
// payload bandwidth and have no max-burst setting, so overdue probe slots are still discarded.
func advanceProbeDeadline(target time.Time, interval time.Duration, now time.Time) (time.Time, bool) {
	next := target.Add(interval)
	if !next.After(now) {
		return now.Add(interval), true
	}
	return next, false
}

// payloadPacer retains the absolute byte schedule while independently enforcing a maximum
// catch-up rate. The float-valued schedule preserves sub-packet byte credit across ticks.
// burstCreditBytes is capped so idle time cannot accumulate an unbounded instantaneous burst.
type payloadPacer struct {
	startedAt            time.Time
	lastTick             time.Time
	bandwidthBytesPerSec float64
	maxBurstBytesPerSec  float64
	accountedBytes       uint64
	burstCreditBytes     float64
	burstCapacityBytes   float64
}

func newPayloadPacer(
	now time.Time, payloadSize int, bandwidthBps, maxBurstBps float64,
) payloadPacer {
	maxBurstBytesPerSec := maxBurstBps / 8
	burstBytesPerTick := maxBurstBytesPerSec * payloadPacingCadence.Seconds()
	burstCapacityBytes := math.Ceil(burstBytesPerTick/float64(payloadSize)) *
		float64(payloadSize)
	return payloadPacer{
		startedAt:            now,
		lastTick:             now,
		bandwidthBytesPerSec: bandwidthBps / 8,
		maxBurstBytesPerSec:  maxBurstBytesPerSec,
		burstCreditBytes:     burstCapacityBytes,
		burstCapacityBytes:   burstCapacityBytes,
	}
}

// beginTick replenishes max-burst credit from elapsed wall time. The canonical schedule itself
// remains absolute and therefore retains all pacing debt after a delayed wake.
func (p *payloadPacer) beginTick(now time.Time) {
	if now.Before(p.lastTick) {
		return
	}
	p.burstCreditBytes += now.Sub(p.lastTick).Seconds() * p.maxBurstBytesPerSec
	if p.burstCreditBytes > p.burstCapacityBytes {
		p.burstCreditBytes = p.burstCapacityBytes
	}
	p.lastTick = now
}

// canSend reports whether the absolute bandwidth schedule and max-burst bucket both allow the
// next packet. scheduledBytes remains fractional rather than rounding at every tick.
func (p *payloadPacer) canSend(now time.Time, packetSize int) bool {
	scheduledBytes := p.scheduledBytes(now)
	nextAccountedBytes := float64(p.accountedBytes) + float64(packetSize)
	return nextAccountedBytes <= scheduledBytes &&
		float64(packetSize) <= p.burstCreditBytes
}

func (p *payloadPacer) scheduledBytes(now time.Time) float64 {
	return now.Sub(p.startedAt).Seconds() * p.bandwidthBytesPerSec
}

// sent accounts one independently serialized send attempt against both schedules.
func (p *payloadPacer) sent(packetSize int) {
	p.accountedBytes += uint64(packetSize)
	p.burstCreditBytes -= float64(packetSize)
}

// behind reports how late the next packet already due on the canonical schedule is. This is only
// true when a tick ended with debt, normally because maxburst or local send work bounded the batch.
func (p *payloadPacer) behind(now time.Time, packetSize int) (bool, time.Duration) {
	bytesUntilNext := float64(p.accountedBytes) + float64(packetSize)
	dueAfter := time.Duration(bytesUntilNext / p.bandwidthBytesPerSec * float64(time.Second))
	dueAt := p.startedAt.Add(dueAfter)
	if dueAt.After(now) {
		return false, 0
	}
	return true, now.Sub(dueAt)
}

// sendLoop is the single writer goroutine. It wakes at a coarse cadence, calculates the payload
// batch from an absolute byte schedule, and writes each packet independently through conn.WriteTo.
// This gives every packet a fresh application timestamp, sequence number, Hummingbird timestamp,
// duplicate-detection counter, and MAC. Payload debt is retained and repaid at no more than
// maxBurstBps; Pong probes remain on an independent no-catch-up schedule.
func (c *client) sendLoop(ctx context.Context, conn *snet.Conn, tracker *pongTracker) {
	pongInterval := time.Duration(float64(time.Second) / c.cfg.pongRateHz)
	if pongInterval <= 0 {
		pongInterval = time.Second
	}

	var deadline time.Time
	if c.cfg.duration > 0 {
		deadline = time.Now().Add(c.cfg.duration)
	}

	buf := make([]byte, c.cfg.payloadSize)
	pongBuf := make([]byte, HeaderLen)

	var payloadSeq, pongSeq uint64
	now := time.Now()
	payloadPacing := newPayloadPacer(
		now, c.cfg.payloadSize, c.cfg.bandwidthBps, c.cfg.maxBurstBps,
	)
	nextPong := now
	ticker := time.NewTicker(payloadPacingCadence)
	defer ticker.Stop()

	var lastSendTimestamp int64
	nextSendTimestamp := func(at time.Time) int64 {
		timestamp := at.UnixNano()
		if timestamp <= lastSendTimestamp {
			timestamp = lastSendTimestamp + 1
		}
		lastSendTimestamp = timestamp
		return timestamp
	}
	nextPayload := func() (size int, small bool) {
		if c.forceSmallNextPayload.Load() {
			return bidirectionalFirstPacketLen, true
		}
		return c.cfg.payloadSize, false
	}

	var overrunsSinceLog int
	lastOverrunLog := now
	recordPacingDelay := func(delay time.Duration) {
		overrunsSinceLog++
		if time.Since(lastOverrunLog) >= time.Second {
			log.Error("Pacing schedule behind", "count_since_last_log", overrunsSinceLog)
			overrunsSinceLog = 0
			lastOverrunLog = time.Now()
		}
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		tickNow := time.Now()
		if !deadline.IsZero() && !tickNow.Before(deadline) {
			return
		}
		payloadPacing.beginTick(tickNow)

		for {
			if ctx.Err() != nil {
				return
			}
			if !deadline.IsZero() && !time.Now().Before(deadline) {
				return
			}

			size, small := nextPayload()
			if !payloadPacing.canSend(tickNow, size) {
				break
			}
			if small && !c.forceSmallNextPayload.CompareAndSwap(true, false) {
				continue
			}

			sentAt := time.Now()
			EncodePayload(buf[:size], payloadSeq, nextSendTimestamp(sentAt), fillerSeed)
			if _, err := conn.WriteTo(buf[:size], c.currentAddr.Load()); err != nil {
				log.Error("Sending payload packet", "err", err)
			} else {
				c.metrics.payloadPacketsSent.Inc()
				c.metrics.payloadBytesSent.Add(float64(size))
				c.rateMu.Lock()
				c.rate.Add(size)
				c.rateMu.Unlock()
			}
			payloadSeq++
			payloadPacing.sent(size)
		}

		nextSize, _ := nextPayload()
		if behind, lateness := payloadPacing.behind(tickNow, nextSize); behind {
			recordPacingDelay(lateness)
		}

		if !nextPong.After(tickNow) {
			sentAt := time.Now()
			EncodePongRequest(pongBuf, pongSeq, nextSendTimestamp(sentAt))
			if _, err := conn.WriteTo(pongBuf, c.currentAddr.Load()); err != nil {
				log.Error("Sending pong request", "err", err)
			} else {
				c.metrics.pongRequestsSent.Inc()
				tracker.recordSent(pongSeq, sentAt)
			}
			pongSeq++
			afterSend := time.Now()
			pongTarget := nextPong
			var rebased bool
			// Payload and pong schedules rebase independently; delaying one does not move the other.
			nextPong, rebased = advanceProbeDeadline(nextPong, pongInterval, afterSend)
			if rebased {
				recordPacingDelay(afterSend.Sub(pongTarget))
			}
		}
	}
}

// pongReceiveLoop reads PongReply packets and feeds RTT/jitter measurements into tracker and
// the Prometheus metrics.
func (c *client) pongReceiveLoop(ctx context.Context, conn *snet.Conn, tracker *pongTracker) {
	buf := make([]byte, PongReplyLen)
	go c.pongSweepLoop(ctx, tracker)

	for {
		if ctx.Err() != nil {
			return
		}
		if err := conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond)); err != nil {
			log.Error("Setting read deadline", "err", err)
			return
		}
		n, _, err := conn.ReadFrom(buf)
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
		reply, err := DecodePongReply(buf[:n])
		if err != nil {
			log.Error("Decoding pong reply", "err", err)
			continue
		}
		now := time.Now()
		delta, accepted := tracker.remote.record(reply, now)
		if !accepted {
			continue // Duplicate, reordered, older, or a regressed cumulative snapshot.
		}
		rtt, late := tracker.recordReplied(reply, now)
		c.metrics.pongRepliesReceived.Inc()
		if late {
			c.metrics.pongLateRepliesReceived.Inc()
		}
		c.metrics.rtt.Observe(rtt.Seconds())
		tracker.mu.Lock()
		c.metrics.jitter.Set(tracker.jitter.Jitter.Seconds())
		tracker.mu.Unlock()
		c.applyRemoteStats(delta)
	}
}

func (c *client) applyRemoteStats(delta remoteStatsDelta) {
	c.metrics.remotePayloadPacketsReceived.Add(float64(delta.payloadPacketsReceived))
	c.metrics.remotePayloadBytesReceived.Add(float64(delta.payloadBytesReceived))
	c.metrics.remotePayloadLost.Add(float64(delta.payloadLost))
	c.metrics.remotePayloadOutOfOrder.Add(float64(delta.payloadOutOfOrder))
	c.metrics.remotePongRequestsReceived.Add(float64(delta.pongRequestsReceived))
	c.metrics.remotePongRepliesSent.Add(float64(delta.pongRepliesSent))
	if delta.hasReceiveRate {
		c.metrics.remoteReceiveRateBps.Set(delta.receiveRateBps)
	}
	c.metrics.remoteStatsAge.Set(0)
}

func (c *client) pongSweepLoop(ctx context.Context, tracker *pongTracker) {
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			if lost := tracker.evictTimedOut(now); lost > 0 {
				c.metrics.pongLost.Add(float64(lost))
			}
		}
	}
}

// reportLoop prints periodic interval reports to stdout, mirroring iperf -i, and updates the
// achieved send-rate gauge.
func (c *client) reportLoop(ctx context.Context, tracker *pongTracker) {
	ticker := time.NewTicker(c.cfg.reportInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-ticker.C:
			c.rateMu.Lock()
			res := c.rate.Snapshot(now)
			c.rateMu.Unlock()
			tracker.mu.Lock()
			jitter := tracker.jitter.Jitter
			tracker.mu.Unlock()
			c.metrics.sendRateBps.Set(res.BitsPerSec)
			age := tracker.remote.age(now)
			if age < 0 {
				c.metrics.remoteStatsAge.Set(-1)
			} else {
				c.metrics.remoteStatsAge.Set(age.Seconds())
			}
			fmt.Printf("[client] interval=%s payload_bytes=%d rate=%.2f Mbps jitter=%s\n",
				res.Duration.Round(time.Millisecond), res.Bytes,
				res.BitsPerSec/1e6, jitter.Round(time.Microsecond))
		}
	}
}
