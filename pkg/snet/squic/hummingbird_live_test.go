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

package squic_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/scionproto/scion/pkg/snet/squic/hummingbirdtest"
)

const (
	tinyServerDaemonAddr = "127.0.0.19:30255"
	tinyClientDaemonAddr = "[fd00:f00d:cafe::7f00:b]:30255"
	tinyServerListenAddr = "1-ff00:0:111,127.0.0.20:12345"
	tinyClientListenAddr = "1-ff00:0:112,[fd00:f00d:cafe::7f00:c]:0"
	tinyServerRemoteAddr = "1-ff00:0:111,127.0.0.20:12345"
)

var tinyBRMetricsEndpoints = []string{
	"http://127.0.0.9:30442/metrics",
	"http://127.0.0.10:30442/metrics",
	"http://127.0.0.17:30442/metrics",
	"http://[fd00:f00d:cafe::7f00:9]:30442/metrics",
}

// TestQUICOverHummingbirdTinyTopology verifies that a QUIC handshake plus a
// stream exchange succeeds when the client sends over a Hummingbird reservation
// path in the running tiny topology.
func TestQUICOverHummingbirdTinyTopology(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live tiny-topology test in short mode")
	}
	if os.Getenv("SCION_RUN_LIVE_TESTS") == "" {
		t.Skip("set SCION_RUN_LIVE_TESTS=1 to run live tiny-topology tests")
	}

	keysRoot := requireTinyTopologyAssets(t)
	serverLocal, err := hummingbirdtest.MustParseUDPAddr(tinyServerListenAddr)
	require.NoError(t, err)
	clientLocal, err := hummingbirdtest.MustParseUDPAddr(tinyClientListenAddr)
	require.NoError(t, err)
	serverRemote, err := hummingbirdtest.MustParseUDPAddr(tinyServerRemoteAddr)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- hummingbirdtest.RunServer(
			ctx,
			tinyServerDaemonAddr,
			serverLocal,
			clientLocal.IA,
			t.Logf,
		)
	}()

	err = hummingbirdtest.RunClient(
		ctx,
		tinyClientDaemonAddr,
		clientLocal,
		serverRemote,
		keysRoot,
		t.Logf,
	)
	require.NoErrorf(t, err,
		"QUIC dial timed out or failed; this usually means the initial Hummingbird "+
			"packet was dropped before the server could reply")

	require.NoError(t, <-serverErr)
}

// TestQUICOverHummingbirdTinyTopologyTokenBucketDemotion verifies that QUIC
// still succeeds when the reservation bandwidth is intentionally tiny, causing
// routers to demote packets to best-effort via token bucket checks.
func TestQUICOverHummingbirdTinyTopologyTokenBucketDemotion(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping live tiny-topology test in short mode")
	}
	if os.Getenv("SCION_RUN_LIVE_TESTS") == "" {
		t.Skip("set SCION_RUN_LIVE_TESTS=1 to run live tiny-topology tests")
	}

	before, err := scrapeHummCounterTotals(tinyBRMetricsEndpoints)
	require.NoError(t, err)

	keysRoot := requireTinyTopologyAssets(t)
	serverLocal, err := hummingbirdtest.MustParseUDPAddr(tinyServerListenAddr)
	require.NoError(t, err)
	clientLocal, err := hummingbirdtest.MustParseUDPAddr(tinyClientListenAddr)
	require.NoError(t, err)
	serverRemote, err := hummingbirdtest.MustParseUDPAddr(tinyServerRemoteAddr)
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	serverErr := make(chan error, 1)
	go func() {
		serverErr <- hummingbirdtest.RunServer(
			ctx,
			tinyServerDaemonAddr,
			serverLocal,
			clientLocal.IA,
			t.Logf,
		)
	}()

	params := hummingbirdtest.DefaultReservationParams()
	// Intentionally tiny so payload quickly exceeds token bucket and gets demoted.
	params.Bandwidth = 1
	err = hummingbirdtest.RunClientWithParams(
		ctx,
		tinyClientDaemonAddr,
		clientLocal,
		serverRemote,
		keysRoot,
		params,
		t.Logf,
	)
	require.NoErrorf(t, err,
		"QUIC dial timed out or failed; demoted packets should still complete over best-effort")

	require.NoError(t, <-serverErr)

	after, err := scrapeHummCounterTotals(tinyBRMetricsEndpoints)
	require.NoError(t, err)

	hummCounter := deltaCounter(after, before, "router_humm_processed_pkts_total")
	flyoverCounter := deltaCounter(after, before, "router_humm_flyover_pkts_total")
	demotedCounter := deltaCounter(after, before, "router_humm_demoted_tokenbucket_total")
	t.Logf("total hummingbird packets: %f", hummCounter)
	t.Logf("total flyover packets: %f", flyoverCounter)
	t.Logf("total demotions: %f", demotedCounter)
	require.Greater(t, hummCounter, float64(0))
	require.Greater(t, flyoverCounter, float64(0))
	require.Greater(t, demotedCounter, float64(0))
}

func requireTinyTopologyAssets(t *testing.T) string {
	t.Helper()

	root := filepath.Join(requireRepoRoot(t), "gen")
	keysRoot, err := hummingbirdtest.FindTinyTopologyAssets(root)
	require.NoError(t, err)
	t.Logf("using tiny-topology assets from %s", keysRoot)
	return keysRoot
}

func requireRepoRoot(t *testing.T) string {
	t.Helper()

	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok, "resolve current file path")
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func scrapeHummCounterTotals(endpoints []string) (map[string]float64, error) {
	totals := map[string]float64{
		"router_humm_processed_pkts_total":      0,
		"router_humm_flyover_pkts_total":        0,
		"router_humm_demoted_freshness_total":   0,
		"router_humm_demoted_expired_total":     0,
		"router_humm_demoted_tokenbucket_total": 0,
	}
	client := &http.Client{Timeout: 3 * time.Second}

	for _, endpoint := range endpoints {
		resp, err := client.Get(endpoint)
		if err != nil {
			return nil, fmt.Errorf("fetching %s: %w", endpoint, err)
		}
		if resp.StatusCode != http.StatusOK {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("fetching %s: status %s", endpoint, resp.Status)
		}

		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "router_humm_") || strings.HasPrefix(line, "#") {
				continue
			}
			space := strings.LastIndexByte(line, ' ')
			if space <= 0 || space == len(line)-1 {
				continue
			}
			namePart := line[:space]
			metricName := namePart
			if brace := strings.IndexByte(namePart, '{'); brace >= 0 {
				metricName = namePart[:brace]
			}
			if _, ok := totals[metricName]; !ok {
				continue
			}
			v, err := strconv.ParseFloat(strings.TrimSpace(line[space+1:]), 64)
			if err != nil {
				continue
			}
			totals[metricName] += v
		}
		if err := scanner.Err(); err != nil {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("reading %s: %w", endpoint, err)
		}
		_ = resp.Body.Close()
	}
	return totals, nil
}

func deltaCounter(after, before map[string]float64, metric string) float64 {
	return after[metric] - before[metric]
}
