# Hummingbird bandwidth tester

`hummbwtester` is a continuous-traffic experiment for comparing Hummingbird-reserved SCION traffic with ordinary best-effort SCION traffic.
It runs one UDP server and any number of clients inside the Docker test topology.
Clients send paced payload traffic and periodic probes;
replies report remote receive, loss, and ordering information back to the client.

The experiment is intentionally client-observed: the server does not expose Prometheus metrics.
Each client exposes its own metrics endpoint,
including the server observations returned in probe replies.

## How it works

The experiment uses the generated Docker topology in `gen/`; it never generates a topology itself. `tools/hummbwtester/setup-topology.py` reads the generated Docker Compose file and topology files, then:

- configures every generated border router with the experiment's ingress and egress sizing;
- starts the existing Docker topology;
- discovers the border-router interfaces joining different ASes;
- applies a TBF qdisc inside every BR network namespace using the configured `tc` values;
- copies the statically linked `hummbwtester` artifact built by `make build-dev` to every configured tester container; and
- generates Prometheus file-service-discovery targets in `gen/hummbwtester-prometheus/`.

The cap is applied in both directions of each selected inter-AS link, before packets leave each
border router's network namespace.
In tiny topology this shapes the `110 <-> 111` and `110 <-> 112` links,
while leaving the intra-AS bridges unshaped.

`tools/hummbwtester/run-humm-bwtester.py` starts the server, waits two seconds, and starts all clients concurrently.
Hummingbird clients derive reservations from `/share/gen` master keys
rather than using the redemption service.
Each Hummingbird client chooses a random nonzero 22-bit reservation ID when
it starts and reuses it across reservation renewals. Client workload and reservation settings
are read from the JSON configuration.

## Configuration

Edit [hummbwtester.json](hummbwtester.json). It has five required top-level sections:

- `server`: the server's `isd_as`, tester `host`, UDP `port`, and
  `receive_buffer_size`.
- `hummingbird_clients`: zero or more Hummingbird client endpoint objects.
- `best_effort_clients`: zero or more best-effort client endpoint objects.
- `router`: experiment-only socket and queue settings written to every generated BR TOML before
  startup: `send_buffer_size`, `receive_buffer_size`, `ingress_batch_size`, `processor_queue_size`,
  `egress_batch_size`, and `egress_queue_size`.
- `tc`: TBF `rate`, `burst`, and explicit queue `limit` values passed to `tc`.

Linux doubles the requested `SO_SNDBUF` and `SO_RCVBUF` internally. The sample requests a 16 KiB
send buffer and uses a deliberately larger 256 KiB TBF limit, so socket-memory backpressure should
stop the BR writer before TBF tail-drop. It requests the host's current 4 MiB maximum receive
buffer for every BR socket and for the tester server. If these receive queues still overflow,
increase `net.core.rmem_max` and both configured receive-buffer values together. Its ingress batch
of 64 avoids the receive-side cost of one-packet batches; it is not queue storage. Each fast- and
slow-path processor ingress queue and each priority/best-effort egress queue has 640 slots to absorb
short scheduling stalls.

Every client requires these fields:

- `client_id`: a unique identifier used as the sole custom Prometheus label.
- `isd_as`, `host`, and `port`: the tester-container endpoint. Use port `0` for an ephemeral UDP port.
- `bandwidth`: payload send rate passed as `-bandwidth`, such as `"1Mbps"`.
- `maxburst`: maximum payload rate while repaying pacing debt, passed as `-maxburst`. It must be
  greater than or equal to `bandwidth`.
- `duration`: test length passed as `-duration`, such as `"600s"`.

Hummingbird clients additionally require `hummingbird_reservation`, an object with:

- `bandwidth`: forward reservation bandwidth class, passed as the first `-hummingbird` value.
- `duration`: reservation duration, such as `"10s"`.
- `reverse_bandwidth`: reverse reservation bandwidth class; use `0` for no reverse reservation.

Both client types may optionally set `payload_size`, `pong_rate`, and `renewal_fraction`.
They are passed respectively as `-payload-size`, `-pong-rate`, and `-renewal-fraction`.
When omitted, the binary's built-in defaults apply.

### Bounded catch-up after a missed deadline

Payload packets and pong requests have independent schedules. Payload pacing retains an absolute
canonical schedule at `bandwidth`, so a late send creates debt rather than discarding scheduled
payload slots. A second deadline spaces actual sends at `maxburst`. While debt exists, the client
therefore catches up at no more than `maxburst`; after repayment, it resumes `bandwidth`.

For example, a 10 Mbps client with `maxburst` 20 Mbps that accumulates 10 megabits of debt has
10 Mbps of extra catch-up capacity and needs at least one second to repay it. Setting `maxburst`
equal to `bandwidth` retains the debt accounting but provides no acceleration, so it cannot catch
up while continuously sending.

Pong probes do not contribute to the configured payload bandwidth and retain their independent
no-catch-up schedule. A send that leaves its schedule behind increments
`hummbwtester_client_pacing_overrun_total`, records its lateness in
`hummbwtester_client_pacing_delay_seconds`, and contributes to the rate-limited
`Pacing schedule behind` log message.

For example, this commented dummy Hummingbird client shows every supported client field:

```jsonc
// {
//   "client_id": "hummingbird-tuned-example",
//   "isd_as": "1-ff00:0:111",
//   "host": "172.20.0.29",
//   "port": 0,
//   "bandwidth": "2Mbps",
//   "maxburst": "4Mbps",
//   "duration": "5m",
//   "hummingbird_reservation": {
//     "bandwidth": 1000,
//     "duration": "10s",
//     "reverse_bandwidth": 1000
//   },
//   "payload_size": 1200,
//   "pong_rate": 2.0,
//   "renewal_fraction": 0.7
// }
```

Client IDs must be unique and match `[A-Za-z0-9._-]+`.

Daemon connectors are deliberately not configured:
they are derived from `gen/sciond_addresses.json` for the configured AS.
Metrics ports are also derived:
after sorting all clients lexicographically by `client_id`,
the first receives `9090`, the next `9091`, and so on.
Prometheus adds `client_id` as the sole custom label to that client's metrics.

The endpoint addresses must match the generated tester-container addresses.
For Docker tiny, the sample configuration places the server in AS112 and both sample clients in AS111.

While clients run, the runner prints one timestamped table per minute. Counter rows are changes
since the preceding report and TBF backlog is the current number of queued bytes. Columns identify
external border-router interfaces. The table reports BFD sent, received, and inferred lost packets
(peer sent minus local received); total Hummingbird demotions; `busy_forwarder` drops; and TBF
drops, overlimits, and backlog.

These values are observation-only: BFD state changes, packet drops, TBF counters, and log entries
never stop or fail an experiment. Only an experiment client or server process exiting unsuccessfully
causes the runner to return a failure.

## First run

From the repository root, build the Docker images and generate Docker tiny topology once:

```bash
make build-dev
make docker-images
./scion.sh topology -d -c topology/tiny.topo
```

Review and edit `tools/hummbwtester/hummbwtester.json`, then prepare the experiment:

```bash
./tools/hummbwtester/setup-topology.py
```

Setup prints the derived metrics-port mapping, starts the topology,
applies the qdiscs, and copies the binary built by `make build-dev`.
Start the experiment with:

```bash
./tools/hummbwtester/run-humm-bwtester.py
```

Logs are written beneath `logs/hummbwtester/`, one file per `client_id` plus `server.log`.

## Regular run cycle

For a configuration change, run setup again before running the experiment:

```bash
./tools/hummbwtester/setup-topology.py
./tools/hummbwtester/run-humm-bwtester.py
```

For a tester source change, first rebuild the standard development artifacts, then run setup:

```bash
make build-dev
./tools/hummbwtester/setup-topology.py
./tools/hummbwtester/run-humm-bwtester.py
```

For a router source change, rebuild and reload the Docker images as well before setup:

```bash
make build-dev
make docker-images
./tools/hummbwtester/setup-topology.py
./tools/hummbwtester/run-humm-bwtester.py
```

After `./scion.sh stop`, Docker removes the bridges and their qdiscs.
Do not use `./scion.sh start` alone for another experiment;
rerun `./tools/hummbwtester/setup-topology.py` so it starts the existing generated topology,
recreates the bandwidth caps, and recopies the binary.

`tools/hummbwtester/setup-topology.py` requires an existing `gen/scion-dc.yml`.
If `gen/` was generated for supervisord instead,
regenerate Docker topology with the command in the first-run section.

## Monitoring

The optional Prometheus/Grafana stack is in [monitoring](monitoring/README.md).
After setup has generated targets, start it with:

```bash
cd tools/hummbwtester/monitoring
docker compose up -d
```

Prometheus is available at `http://localhost:8090`;
Grafana is at `http://localhost:3000` (`admin` / `admin`).
The dashboard groups client series by `client_id`.

## Tests

Run the focused Go and orchestration tests from the repository root:

```bash
go test ./tools/hummbwtester
bazel test //tools/hummbwtester:go_default_test //tools/hummbwtester:orchestration_test
```

The Python test covers configuration validation, deterministic metrics-port assignment, and
per-minute report aggregation.
The Go test covers random Hummingbird reservation-ID generation.
A practical Docker smoke test is to run setup, stop SCION, and rerun setup. The generated
`hummbwtester_tc_*` helpers inspect qdiscs inside the BR network namespaces.
