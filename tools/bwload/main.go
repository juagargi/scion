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
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/scionproto/scion/pkg/addr"
	"github.com/scionproto/scion/pkg/daemon"
	daemontypes "github.com/scionproto/scion/pkg/daemon/types"
	"github.com/scionproto/scion/pkg/private/serrors"
	hummlib "github.com/scionproto/scion/pkg/slayers/path/hummingbird"
	"github.com/scionproto/scion/pkg/snet"
	"github.com/scionproto/scion/pkg/snet/metrics"
	snetpath "github.com/scionproto/scion/pkg/snet/path"
	"github.com/scionproto/scion/private/keyconf"
)

const (
	defaultChunkSize  = 1200
	hummReservationID = uint32(1)
	hummStartOffset   = -3 * time.Second
)

type config struct {
	local       snet.UDPAddr
	remote      snet.UDPAddr
	sciond      string
	totalSize   int64
	bandwidth   int64
	hummingbird string
	hummKeysDir string
	hummParams  hummingbirdParameters
}

type hummingbirdParameters struct {
	Bw       uint16
	Duration uint16
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "bwload: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := parseFlags()
	if err != nil {
		return err
	}

	setupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	daemonConn, err := daemon.NewService(cfg.sciond).Connect(setupCtx)
	if err != nil {
		return serrors.Wrap("connecting to sciond", err, "addr", cfg.sciond)
	}
	defer daemonConn.Close()

	localIA, err := daemonConn.LocalIA(setupCtx)
	if err != nil {
		return serrors.Wrap("querying local IA from sciond", err)
	}
	if cfg.local.IA.IsZero() {
		return serrors.New("local address is missing IA")
	}
	if !cfg.local.IA.Equal(localIA) {
		return serrors.New("local IA does not match sciond",
			"local", cfg.local.IA, "sciond", localIA)
	}

	topo, err := daemon.LoadTopology(setupCtx, daemonConn)
	if err != nil {
		return serrors.Wrap("loading topology from sciond", err)
	}

	scionMetrics := metrics.NewSCIONPacketConnMetrics()
	network := &snet.SCIONNetwork{
		SCMPHandler: snet.DefaultSCMPHandler{
			RevocationHandler: daemon.RevHandler{Connector: daemonConn},
			SCMPErrors:        scionMetrics.SCMPErrors,
		},
		PacketConnMetrics: scionMetrics,
		Topology:          topo,
	}

	remote, err := buildRemote(setupCtx, daemonConn, cfg)
	if err != nil {
		return err
	}

	mode := "plain"
	if cfg.hummingbird != "" {
		mode = "hummingbird"
	}
	fmt.Printf("mode=%s local_ia=%s local=%s remote=%s size=%d bw=%dB/s\n",
		mode, localIA, cfg.local.String(), remote.String(), cfg.totalSize, cfg.bandwidth)

	conn, err := network.Dial(context.Background(), "udp", cfg.local.Host, remote)
	if err != nil {
		return serrors.Wrap("dialing SCION UDP connection", err, "remote", remote)
	}
	defer conn.Close()

	start := time.Now()
	bytesSent, err := sendPaced(conn, cfg.totalSize, cfg.bandwidth)
	if err != nil {
		return err
	}
	elapsed := time.Since(start)
	actualBW := float64(bytesSent) / elapsed.Seconds()
	fmt.Printf("sent=%dB elapsed=%s actual_bw=%.2fB/s\n", bytesSent, elapsed.Round(time.Millisecond), actualBW)
	return nil
}

func parseFlags() (*config, error) {
	cfg := &config{}
	flag.Var(&cfg.local, "local", "local SCION UDP address")
	flag.Var(&cfg.remote, "remote", "remote SCION UDP address")
	flag.StringVar(&cfg.sciond, "sciond", "", "SCION daemon address")
	flag.Int64Var(&cfg.totalSize, "size", 0, "total payload bytes to send")
	flag.Int64Var(&cfg.bandwidth, "bw", 0, "target payload bandwidth in bytes per second")
	flag.StringVar(&cfg.hummingbird, "hummingbird", "", "Enable Hummingbird with BW,dur (e.g. '3,5s')")
	flag.StringVar(&cfg.hummKeysDir, "hummKeysDir", "",
		"Root directory containing AS*/keys/master0.key files for Hummingbird")
	flag.Parse()

	switch {
	case cfg.sciond == "":
		return nil, serrors.New("missing -sciond")
	case cfg.local.Host == nil:
		return nil, serrors.New("missing or invalid -local")
	case cfg.remote.Host == nil:
		return nil, serrors.New("missing or invalid -remote")
	case cfg.totalSize <= 0:
		return nil, serrors.New("-size must be > 0", "size", cfg.totalSize)
	case cfg.bandwidth <= 0:
		return nil, serrors.New("-bw must be > 0", "bw", cfg.bandwidth)
	}

	if cfg.hummingbird != "" {
		if cfg.hummKeysDir == "" {
			return nil, serrors.New("-hummKeysDir is required when -hummingbird is set")
		}
		params, err := parseHummingbirdFlag(cfg.hummingbird)
		if err != nil {
			return nil, err
		}
		cfg.hummParams = params
	}
	return cfg, nil
}

func parseHummingbirdFlag(raw string) (hummingbirdParameters, error) {
	bwRaw, durRaw, ok := strings.Cut(raw, ",")
	if !ok {
		return hummingbirdParameters{}, serrors.New("bad hummingbird flag, expected BW,duration",
			"value", raw)
	}
	bw, err := strconv.ParseUint(bwRaw, 10, 16)
	if err != nil {
		return hummingbirdParameters{}, serrors.Wrap("parsing hummingbird bandwidth", err,
			"value", bwRaw)
	}
	dur, err := time.ParseDuration(durRaw)
	if err != nil {
		return hummingbirdParameters{}, serrors.Wrap("parsing hummingbird duration", err,
			"value", durRaw)
	}
	if dur.Seconds() > math.MaxUint16 {
		return hummingbirdParameters{}, serrors.New(
			"hummingbird duration too long, must fit in uint16 seconds",
			"seconds", dur.Seconds(),
		)
	}
	return hummingbirdParameters{
		Bw:       uint16(bw),
		Duration: uint16(dur.Seconds()),
	}, nil
}

func buildRemote(ctx context.Context, daemonConn daemon.Connector, cfg *config) (*snet.UDPAddr, error) {
	remote := cfg.remote.Copy()
	if remote == nil {
		return nil, serrors.New("remote address is nil")
	}
	if remote.IA.Equal(cfg.local.IA) {
		remote.Path = snetpath.Empty{}
		remote.NextHop = nil
		return remote, nil
	}

	paths, err := daemonConn.Paths(ctx, remote.IA, cfg.local.IA, daemontypes.PathReqFlags{})
	if err != nil {
		return nil, serrors.Wrap("requesting paths", err, "src", cfg.local.IA, "dst", remote.IA)
	}
	if len(paths) == 0 {
		return nil, serrors.New("no path found", "src", cfg.local.IA, "dst", remote.IA)
	}
	path := paths[0]
	if cfg.hummingbird != "" {
		reservation, err := buildReservationWithSecretValues(path, cfg.hummKeysDir, cfg.hummParams, time.Now())
		if err != nil {
			return nil, err
		}
		remote.Path = reservation
	} else {
		remote.Path = path.Dataplane()
	}
	remote.NextHop = path.UnderlayNextHop()
	return remote, nil
}

func buildReservationWithSecretValues(
	path snet.Path,
	hummKeysDir string,
	params hummingbirdParameters,
	now time.Time,
) (*snetpath.Reservation, error) {
	returnNow := func() time.Time {
		return now
	}
	baseHops := snetpath.InterfacesToBaseHops(path.Metadata().Interfaces)
	flyovers := make([]*snetpath.Hop, 0, len(baseHops))
	startTime := uint32(now.Add(hummStartOffset).Unix())
	aesByIA := make(map[addr.IA]cipher.Block)
	buffer := make([]byte, hummlib.AkBufferSize)

	for _, baseHop := range baseHops {
		block, ok := aesByIA[baseHop.IA]
		if !ok {
			sv, err := hummSecretValue(hummKeysDir, baseHop.IA)
			if err != nil {
				return nil, err
			}
			block, err = aes.NewCipher(sv)
			if err != nil {
				return nil, serrors.Wrap("creating AES cipher", err, "ia", baseHop.IA)
			}
			aesByIA[baseHop.IA] = block
		}
		akRaw := hummlib.DeriveAuthKey(
			block,
			hummReservationID,
			params.Bw,
			baseHop.Ingress,
			baseHop.Egress,
			startTime,
			params.Duration,
			buffer,
		)
		var ak [hummlib.AkBufferSize]byte
		copy(ak[:], akRaw)
		flyovers = append(flyovers, &snetpath.Hop{
			BaseHop: baseHop,
			Flyover: &snetpath.FlyoverData{
				ResID:     hummReservationID,
				Ak:        ak,
				Bw:        params.Bw,
				StartTime: startTime,
				Duration:  params.Duration,
			},
		})
	}
	return snetpath.NewReservation(
		snetpath.WithNow(returnNow),
		snetpath.WithDataplanePath(path.Dataplane(), path.Destination(), flyovers),
	)
}

func hummSecretValue(hummKeysDir string, ia addr.IA) ([]byte, error) {
	asDir := addr.FormatAS(ia.AS(), addr.WithDefaultPrefix(), addr.WithFileSeparator())
	keysDir := filepath.Join(hummKeysDir, asDir, "keys")
	master, err := keyconf.LoadMaster(keysDir)
	if err != nil {
		return nil, serrors.Wrap("loading Hummingbird master key", err, "ia", ia, "dir", keysDir)
	}
	return hummlib.DeriveSecretValue(master.Key0), nil
}

func sendPaced(conn *snet.Conn, totalSize, bandwidth int64) (int64, error) {
	payload := make([]byte, defaultChunkSize)
	start := time.Now()
	var bytesSent int64
	for bytesSent < totalSize {
		chunkSize := minInt64(defaultChunkSize, totalSize-bytesSent)
		n, err := conn.Write(payload[:int(chunkSize)])
		if err != nil {
			return bytesSent, serrors.Wrap("writing payload", err)
		}
		if n != int(chunkSize) {
			return bytesSent, serrors.New("short write", "expected", chunkSize, "actual", n)
		}
		bytesSent += int64(n)

		idealElapsed := time.Duration((bytesSent * int64(time.Second)) / bandwidth)
		if sleepFor := start.Add(idealElapsed).Sub(time.Now()); sleepFor > 0 {
			time.Sleep(sleepFor)
		}
	}
	return bytesSent, nil
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
