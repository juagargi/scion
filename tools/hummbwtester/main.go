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

// Command hummbwtester is a continuous-streaming bandwidth/latency/jitter tester built on
// Hummingbird flyover reservations. See bwtester-hummingbird-design.md at the repository root
// for the design this tool implements.
package main

import (
	"context"
	"flag"
	"fmt"
	"math"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/scionproto/scion/pkg/daemon"
	"github.com/scionproto/scion/pkg/hummingbird/bwencoding"
	marketclient "github.com/scionproto/scion/pkg/hummingbird/marketplace"
	"github.com/scionproto/scion/pkg/log"
	"github.com/scionproto/scion/pkg/private/serrors"
	"github.com/scionproto/scion/pkg/snet"
	"github.com/scionproto/scion/pkg/snet/metrics"
	"github.com/scionproto/scion/private/env"
)

const (
	modeClient = "client"
	modeServer = "server"

	defaultPayloadSize        = 800
	defaultPongRateHz         = 1.0
	defaultMetricsAddr        = ":9090"
	defaultReportInterval     = 1 * time.Second
	defaultRenewalAhead       = 20 * time.Second
	defaultReservationOverlap = 15 * time.Second
	defaultHummStartOffset    = -1 * time.Second
	defaultSciondConfDir      = "/etc/scion"

	// bidirectionalFirstPacketPayload is the fixed payload size used for the very first
	// Payload packet sent on a bidirectional reservation, since that packet also carries the
	// reverse-reservation E2E extension option and must stay small.
	bidirectionalFirstPacketPayload = 8
)

var (
	mode          string
	localFlag     snet.UDPAddr
	remoteFlag    snet.UDPAddr
	sciondAddr    string
	sciondConfDir string

	bandwidthFlag      string
	maxBurstFlag       string
	duration           time.Duration
	payloadSize        int
	pongRateHz         float64
	hummingbirdFlag    string
	hummKeysDir        string
	metricsAddr        string
	reportInterval     time.Duration
	renewalAhead       time.Duration
	reservationOverlap time.Duration
	hummStartOffset    time.Duration
	verifyIntegrity    bool
	receiveBufferSize  int
)

func main() {
	os.Exit(realMain())
}

func realMain() int {
	defer log.HandlePanic()
	defer log.Flush()
	addFlags()
	flag.Parse()
	if err := validateFlags(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	log.Setup(log.Config{Console: log.ConsoleConfig{Level: "info"}})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Info("Received shutdown signal")
		cancel()
	}()

	if mode == modeClient {
		metricsCfg := env.Metrics{Prometheus: metricsAddr}
		go func() {
			defer log.HandlePanic()
			if err := metricsCfg.ServePrometheus(ctx); err != nil {
				log.Error("Serving prometheus metrics", "err", err)
			}
		}()
	}

	sdConn, err := daemon.NewAutoConnector(ctx,
		daemon.WithDaemon(sciondAddr),
		daemon.WithConfigDir(sciondConfDir),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error connecting to sciond:", err)
		return 1
	}
	defer sdConn.Close()

	topo, err := daemon.LoadTopology(ctx, sdConn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error loading topology:", err)
		return 1
	}

	scionPacketConnMetrics := metrics.NewSCIONPacketConnMetrics()
	sn := &snet.SCIONNetwork{
		SCMPHandler: snet.DefaultSCMPHandler{
			RevocationHandler: daemon.RevHandler{Connector: sdConn},
			SCMPErrors:        scionPacketConnMetrics.SCMPErrors,
		},
		PacketConnMetrics: scionPacketConnMetrics,
		Topology:          topo,
	}

	switch mode {
	case modeServer:
		scfg := serverConfig{
			local:             localFlag,
			reportInterval:    reportInterval,
			verifyIntegrity:   verifyIntegrity,
			receiveBufferSize: receiveBufferSize,
		}
		return runServer(ctx, sn, scfg)
	case modeClient:
		var hummParams hummingbirdParameters
		if hummingbirdFlag != "" {
			hummParams, err = parseHummingbirdFlag(hummingbirdFlag, hummKeysDir == "")
			if err != nil {
				fmt.Fprintln(os.Stderr, "error parsing -hummingbird:", err)
				return 1
			}
			if hummKeysDir != "" && (hummParams.Bw > math.MaxUint16 ||
				hummParams.ReverseBw > math.MaxUint16) {
				fmt.Fprintln(os.Stderr, "error: key-derived Hummingbird bandwidth must fit in 16 bits")
				return 1
			}
		}
		var reservationID uint32
		if hummingbirdFlag != "" && hummKeysDir != "" {
			reservationID, err = randomHummReservationID()
			if err != nil {
				fmt.Fprintln(os.Stderr, "error generating Hummingbird reservation ID:", err)
				return 1
			}
		}
		var marketplaceJWT string
		if hummingbirdFlag != "" && hummKeysDir == "" {
			marketplaceJWT = os.Getenv(envMarketplaceJWT)
			if marketplaceJWT == "" {
				fmt.Fprintf(os.Stderr, "error: missing marketplace token (env %s)\n", envMarketplaceJWT)
				return 1
			}
		}
		bandwidthBps, maxBurstBps, err := parsePacingBandwidths(bandwidthFlag, maxBurstFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error configuring client pacing:", err)
			return 1
		}
		cfg := clientConfig{
			local:              localFlag,
			remote:             remoteFlag,
			sdConn:             sdConn,
			bandwidthBps:       bandwidthBps,
			maxBurstBps:        maxBurstBps,
			duration:           duration,
			payloadSize:        payloadSize,
			pongRateHz:         pongRateHz,
			humm:               hummParams,
			hummEnabled:        hummingbirdFlag != "",
			hummReservationID:  reservationID,
			hummKeysDir:        hummKeysDir,
			marketplaceJWT:     marketplaceJWT,
			reportInterval:     reportInterval,
			renewalAhead:       renewalAhead,
			reservationOverlap: reservationOverlap,
			hummStartOffset:    hummStartOffset,
		}
		return runClient(ctx, sn, cfg)
	default:
		fmt.Fprintln(os.Stderr, "error: -mode must be \"client\" or \"server\"")
		return 1
	}
}

func addFlags() {
	flag.StringVar(&mode, "mode", "", "Mode: \"client\" or \"server\"")
	flag.Var(&localFlag, "local", "Local address (IA,IP:port)")
	flag.Var(&remoteFlag, "remote", "(Client only) remote server address (IA,IP:port)")
	flag.StringVar(&sciondAddr, "sciond", "", "SCION daemon address (empty: use -sciond-config-dir)")
	flag.StringVar(&sciondConfDir, "sciond-config-dir", defaultSciondConfDir,
		"Directory containing topology.json/certs for standalone (daemon-less) operation")

	flag.StringVar(&bandwidthFlag, "bandwidth", "1Mbps",
		"(Client only) target payload send rate, e.g. \"1Mbps\", \"500Kbps\", \"2Gbps\"")
	flag.StringVar(&maxBurstFlag, "maxburst", "",
		"(Client only) maximum payload rate while catching up; must be >= -bandwidth; "+
			"empty uses -bandwidth")
	flag.DurationVar(&duration, "duration", 30*time.Second,
		"(Client only) how long to send payload traffic; 0 = run until interrupted")
	flag.IntVar(&payloadSize, "payload-size", defaultPayloadSize,
		"(Client only) UDP payload size in bytes for Payload packets")
	flag.Float64Var(&pongRateHz, "pong-rate", defaultPongRateHz,
		"(Client only) rate, in Hz, at which to send latency probe (pong-request) packets")
	flag.StringVar(&hummingbirdFlag, "hummingbird", "",
		"(Client only, optional) Hummingbird reservation spec: BW,dur[,reverseBW]. "+
			"With -hummKeysDir BW is a class without a unit; otherwise BW is a "+
			"marketplace bandwidth with kbps|mbps|gbps (e.g. \"100kbps,20s\")")
	flag.StringVar(&hummKeysDir, "hummKeysDir", "",
		"(Client only, testing) root dir containing AS*/keys/master0.key; without it, buy from the marketplace")
	flag.DurationVar(&renewalAhead, "renewal-ahead", defaultRenewalAhead,
		"(Client only) how long before reservation expiry to request its replacement")
	flag.DurationVar(&reservationOverlap, "reservation-overlap", defaultReservationOverlap,
		"(Client only) how long before reservation expiry to switch to its replacement")
	flag.DurationVar(&hummStartOffset, "humm-start-offset", defaultHummStartOffset,
		"(Client only) signed offset added to Hummingbird reservation start times")
	flag.BoolVar(&verifyIntegrity, "verify-integrity", false,
		"(Server only) verify the deterministic filler pattern of received payload packets")
	flag.IntVar(&receiveBufferSize, "receive-buffer-size", 0,
		"(Server only) operating-system receive buffer size in bytes; 0 uses the default")

	flag.StringVar(&metricsAddr, "metrics-addr", defaultMetricsAddr,
		"(Client only) address to serve Prometheus /metrics on")
	flag.DurationVar(&reportInterval, "report-interval", defaultReportInterval,
		"Interval between periodic stdout reports")
}

func validateFlags() error {
	switch mode {
	case modeClient, modeServer:
	default:
		return serrors.New("invalid -mode", "mode", mode)
	}
	if localFlag.Host == nil {
		return serrors.New("missing -local")
	}
	if receiveBufferSize < 0 {
		return serrors.New("receive-buffer-size must not be negative", "value", receiveBufferSize)
	}
	if err := validateRenewalTiming(renewalAhead, reservationOverlap); err != nil {
		return err
	}
	if mode == modeClient {
		if remoteFlag.Host == nil {
			return serrors.New("missing -remote")
		}
		if payloadSize < HeaderLen {
			return serrors.New("payload-size too small, must be at least the header size",
				"value", payloadSize, "min", HeaderLen)
		}
	}
	return nil
}

func validateRenewalTiming(ahead, overlap time.Duration) error {
	if ahead < 0 {
		return serrors.New("renewal-ahead must not be negative", "value", ahead)
	}
	if overlap < 0 {
		return serrors.New("reservation-overlap must not be negative", "value", overlap)
	}
	if overlap > ahead {
		return serrors.New("reservation-overlap must not exceed renewal-ahead",
			"reservation_overlap", overlap, "renewal_ahead", ahead)
	}
	return nil
}

// hummingbirdParameters holds the parsed -hummingbird flag: forward bandwidth class, duration
// in seconds, and (if non-zero) reverse-direction bandwidth class for a bidirectional
// reservation.
type hummingbirdParameters struct {
	Bw        uint32
	Duration  uint16
	ReverseBw uint32
}

// parseHummingbirdFlag parses the "BW,dur[,reverseBW]" convention shared with
// tools/end2end/main.go. Marketplace bandwidths carry units; key-derived classes do not.
func parseHummingbirdFlag(raw string, withUnits bool) (hummingbirdParameters, error) {
	parts := strings.Split(raw, ",")
	if len(parts) != 2 && len(parts) != 3 {
		return hummingbirdParameters{}, serrors.New("expected BW,dur[,reverseBW]")
	}
	bw, err := bwencoding.ParseBandwidth(parts[0], withUnits)
	if err != nil {
		return hummingbirdParameters{}, serrors.Wrap("parsing hummingbird bandwidth", err,
			"value", parts[0])
	}
	dur, err := time.ParseDuration(parts[1])
	if err != nil {
		return hummingbirdParameters{}, serrors.Wrap("parsing hummingbird duration", err,
			"value", parts[1])
	}
	if dur.Seconds() > float64(^uint16(0)) {
		return hummingbirdParameters{}, serrors.New(
			"hummingbird duration too long, must fit in 16 bits in seconds",
			"value", dur.Seconds())
	}
	params := hummingbirdParameters{
		Bw:       bw,
		Duration: uint16(dur.Seconds()),
	}
	if len(parts) == 3 {
		reverseBw, err := bwencoding.ParseBandwidth(parts[2], withUnits)
		if err != nil {
			return hummingbirdParameters{}, serrors.Wrap("parsing reverse hummingbird bandwidth", err,
				"value", parts[2])
		}
		params.ReverseBw = reverseBw
	}
	return params, nil
}

const envMarketplaceJWT = "SCION_MARKETPLACE_JWT"

const (
	marketplaceInsecure          = true
	marketplaceMaxPrice          = uint64(math.MaxUint64)
	marketplaceBuyMode           = marketclient.FailOnError
	marketplaceFetchReservations = true
	marketplaceCombineAssets     = false
	marketplaceRetries           = 3
)

// parseBandwidth parses a target bit-rate flag such as "1Mbps", "500Kbps", "2Gbps", or a plain
// number of bits per second.
func parseBandwidth(raw string) (float64, error) {
	raw = strings.TrimSpace(raw)
	units := []struct {
		suffix string
		factor float64
	}{
		{"Gbps", 1e9},
		{"Mbps", 1e6},
		{"Kbps", 1e3},
		{"bps", 1},
	}
	for _, u := range units {
		if strings.HasSuffix(raw, u.suffix) {
			numPart := strings.TrimSuffix(raw, u.suffix)
			val, err := strconv.ParseFloat(numPart, 64)
			if err != nil {
				return 0, serrors.Wrap("parsing bandwidth value", err, "value", raw)
			}
			return val * u.factor, nil
		}
	}
	val, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return 0, serrors.Wrap("parsing bandwidth value", err, "value", raw)
	}
	return val, nil
}

// parsePacingBandwidths parses the canonical and catch-up rates and enforces the relationship
// required by the payload pacer. An omitted max-burst rate disables acceleration while preserving
// command-line compatibility: the maximum then equals the canonical rate.
func parsePacingBandwidths(bandwidthRaw, maxBurstRaw string) (float64, float64, error) {
	bandwidth, err := parseBandwidth(bandwidthRaw)
	if err != nil {
		return 0, 0, serrors.Wrap("parsing -bandwidth", err)
	}
	if bandwidth <= 0 || math.IsNaN(bandwidth) || math.IsInf(bandwidth, 0) {
		return 0, 0, serrors.New("-bandwidth must be finite and positive", "value", bandwidthRaw)
	}
	if strings.TrimSpace(maxBurstRaw) == "" {
		return bandwidth, bandwidth, nil
	}
	maxBurst, err := parseBandwidth(maxBurstRaw)
	if err != nil {
		return 0, 0, serrors.Wrap("parsing -maxburst", err)
	}
	if math.IsNaN(maxBurst) || math.IsInf(maxBurst, 0) || maxBurst < bandwidth {
		return 0, 0, serrors.New("-maxburst must be finite and >= -bandwidth",
			"maxburst", maxBurstRaw, "bandwidth", bandwidthRaw)
	}
	return bandwidth, maxBurst, nil
}
