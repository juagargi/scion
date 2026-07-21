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

- starts the existing Docker topology;
- discovers the Docker bridges joining BRs in different ASes;
- applies a TBF qdisc to every veth on those bridges using the configured `tc` values;
- copies the statically linked `hummbwtester` artifact built by `make build-dev` to every configured tester container; and
- generates Prometheus file-service-discovery targets in `gen/hummbwtester-prometheus/`.

The cap is applied in both directions of each selected inter-AS link.
In tiny topology this shapes the `110 <-> 111` and `110 <-> 112` links,
while leaving the intra-AS bridges unshaped.

`tools/hummbwtester/run-humm-bwtester.py` starts the server, waits two seconds, and starts all clients concurrently.
Hummingbird clients derive reservations from `/share/gen` master keys
rather than using the redemption service.
Each Hummingbird client chooses a random nonzero 22-bit reservation ID when
it starts and reuses it across reservation renewals.

The runner currently uses fixed experiment settings:
1 Mbps payload rate, 600-second duration, and Hummingbird parameters `1000,10s,1000`.
Change the runner constants if a different workload is required.

## Configuration

Edit [hummbwtester.json](hummbwtester.json). It has four required top-level sections:

- `server`: the server's `isd_as`, tester `host`, and UDP `port`.
- `hummingbird_clients`: zero or more Hummingbird client endpoint objects.
- `best_effort_clients`: zero or more best-effort client endpoint objects.
- `tc`: TBF `rate`, `burst`, and `latency` values passed to `tc`.

Every client object has exactly `client_id`, `isd_as`, `host`, and `port`.
Client IDs must be unique and match `[A-Za-z0-9._-]+`.
Use port `0` to request an ephemeral UDP port.

Daemon connectors are deliberately not configured:
they are derived from `gen/sciond_addresses.json` for the configured AS.
Metrics ports are also derived:
after sorting all clients lexicographically by `client_id`,
the first receives `9090`, the next `9091`, and so on.
Prometheus adds `client_id` as the sole custom label to that client's metrics.

The endpoint addresses must match the generated tester-container addresses.
For Docker tiny, the sample configuration places the server in AS112 and both sample clients in AS111.

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

For a source change, first rebuild the standard development artifacts, then run setup:

```bash
make build-dev
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

The Python test covers configuration validation and deterministic metrics-port assignment.
The Go test covers random Hummingbird reservation-ID generation.
A practical Docker smoke test is to run setup, stop SCION, rerun setup,
and inspect the qdiscs with `tc qdisc show` on the generated inter-AS bridge veths.
