# Hummingbird bandwidth tester

`hummbwtester` is a continuous-traffic experiment for comparing Hummingbird-reserved SCION traffic
with ordinary best-effort SCION traffic.
It runs one UDP server and any number of clients either inside the Docker test topology or on
explicitly inventoried SSH hosts.
Clients send paced payload traffic and periodic probes;
replies report remote receive, loss, and ordering information back to the client.

The experiment is intentionally client-observed: the server does not expose Prometheus metrics.
Each client exposes its own metrics endpoint,
including the server observations returned in probe replies.

## How it works

The Docker deployment uses the generated topology in `gen/`; it never generates a topology itself.
`experiment.py setup` reads the generated Docker Compose file and topology files, then:

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

`experiment.py run` starts the Docker-topology server,
waits two seconds, and starts all clients concurrently.
Hummingbird clients either derive reservations from `/share/gen` master keys or buy them from the
marketplace advertised by the selected SCION path, according to the global `hummingbird` setting.
Marketplace runs log in through the registration website advertised in `gen/AS*/staticInfoConfig.json`
(the workload `url` is only used by SSH deployments) and pass the resulting JWT to client processes
through an owner-only file.
Key-derived Hummingbird clients choose a random nonzero 22-bit reservation ID when they start and
reuse it across reservation renewals. Marketplace reservations use the IDs returned by the
marketplace. Client workload and reservation settings are read from the JSON configuration.

## Configuration

Each run uses one self-contained JSON file.
[hummbwtester.json](hummbwtester.json) configures the generated Docker topology;
`hummbwtester-sciera.json` configures SSH hosts. Both files use the same six required top-level sections:

- `server`: the server's `isd_as`, tester `host`, UDP `port`, and `receive_buffer_size`.
- `hummingbird_clients`: zero or more Hummingbird client endpoint objects.
- `best_effort_clients`: zero or more best-effort client endpoint objects.
- `deployment`: `kind` is `docker` or `ssh`. Docker derives placement, daemon addresses,
  BR metrics and shaped links from `gen/`; `deployment.router` sets its BR socket and queue sizes.
  SSH declares `hosts`, `metrics`, and `shaping` here, while each endpoint names its host with `node`.
  The endpoint `host` remains its SCION bind IP.
- `tc`: TBF `rate`, `burst`, and explicit queue `limit` values passed to `tc`.
- `hummingbird`: required global reservation source (`keys` or `marketplace`).
  SSH deployments support only `marketplace`: key-derived reservations need the master keys of every
  on-path AS, which only a generated Docker topology provides.
  Marketplace mode also requires a `marketplace` object with `url`, `username`, and `password_env`; `sub_account` is optional. The password is read from the named environment variable, never from JSON.
  In SSH deployments, `host` is required: it names the `deployment.hosts` entry from which `url` is
  reachable, e.g. `"host": "ufms"` with `"url": "https://127.0.0.1:8888"` for a marketplace bound to
  the loopback of that host. Every SSH command (`setup`, `run`, `teardown`) rejects a configuration
  without it. Docker deployments reject `host`.
  In SSH deployments, the optional `scion_address` is the SCION API address of the marketplace,
  e.g. `"[71-2:0:5c,127.0.0.1]:31888"`; see the static info Note below. Docker deployments reject it,
  because their generated topology already advertises its marketplace.
  The Docker runner discovers the reachable registration URL from `gen/`; `url` is used by SSH runs.

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

- `bandwidth`: in `keys` mode, a forward reservation bandwidth class (integer); in `marketplace`
  mode, a unit-bearing bandwidth such as `"100kbps"`, `"1mbps"`, or `"1gbps"`.
- `duration`: reservation duration, such as `"1m"`.
- `reverse_bandwidth`: reverse reservation bandwidth in the same representation; use `0` in keys
  mode or `"0kbps"` in marketplace mode for no reverse reservation.
- `renewal_ahead` (optional): how long before expiry to request the next reservation; defaults to
  `"20s"`.
- `reservation_overlap` (optional): how long before expiry to switch to the next reservation;
  defaults to `"15s"`.
- `humm_start_offset` (optional): a signed offset added to the calculated reservation start time;
  defaults to `"-1s"`. For example, `"-3s"` starts the reservation three seconds earlier.

Both client types may optionally set `payload_size` and `pong_rate`. They are passed as
`-payload-size` and `-pong-rate`. The Hummingbird reservation timing fields are passed as
`-renewal-ahead`, `-reservation-overlap`, and `-humm-start-offset`; omitted fields use the binary's
built-in defaults. `reservation_overlap` must not exceed `renewal_ahead`, ensuring the replacement
has been requested before its handover. With a negative start offset, the replacement's validity
window begins before handover and overlaps the old window by the overlap plus the magnitude of that
offset. After handover, the old reservation remains valid for the configured overlap while its
packets drain.

### Bounded catch-up after a missed deadline

Payload packets and pong requests have independent schedules. The client wakes every millisecond
and compares the bytes sent with an absolute `bandwidth` schedule. It sends the packets due at that
instant back-to-back, preserving fractional byte credit between wakes so packetization does not
change the long-term rate. Every packet is encoded and passed separately to `WriteTo`, giving it a
fresh sequence number, timestamp, and (for Hummingbird) dataplane MAC.

A small token bucket bounds each catch-up batch and replenishes at `maxburst`. Scheduler or CPU
delays therefore create retained debt rather than an unbounded burst; while debt exists, the client
catches up at no more than `maxburst`, then resumes `bandwidth`.

For example, a 10 Mbps client with `maxburst` 20 Mbps that accumulates 10 megabits of debt has
10 Mbps of extra catch-up capacity and needs at least one second to repay it. Setting `maxburst`
equal to `bandwidth` retains the debt accounting but provides no acceleration, so it cannot catch
up while continuously sending.

Pong probes do not contribute to the configured payload bandwidth and retain their independent
no-catch-up schedule. A pacing tick that leaves its schedule behind contributes to the rate-limited
`Pacing schedule behind` log message. A request that exceeds its RTT deadline increments
`hummbwtester_client_pong_lost_total`, but a later reply is still accepted for latency and remote
receive/loss statistics. Such replies also increment
`hummbwtester_client_pong_late_replies_received_total`. Replies are discarded as stale if either
their sequence number or echoed client send timestamp does not advance beyond the last accepted
reply.

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
//     "bandwidth": "1mbps",
//     "duration": "1m",
//     "reverse_bandwidth": "1mbps",
//     "renewal_ahead": "20s",
//     "reservation_overlap": "15s",
//     "humm_start_offset": "-1s"
//   },
//   "payload_size": 1200,
//   "pong_rate": 2.0
// }
```

Client IDs must be unique and match `[A-Za-z0-9._-]+`.

Docker daemon connectors are derived from `gen/sciond_addresses.json` for the configured AS.
SSH daemon connectors are declared in `deployment.hosts`.
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
./scion.sh topology -d -c topology/tiny.topo -m 1-ff00:0:111
```

Review and edit `tools/hummbwtester/hummbwtester.json`, then prepare the experiment:

```bash
python3 tools/hummbwtester/experiment.py setup --config tools/hummbwtester/hummbwtester.json
```

Setup prints the derived metrics-port mapping, starts the topology,
applies the qdiscs, and copies the binary built by `make build-dev`.
Start the experiment with:

```bash
python3 tools/hummbwtester/experiment.py run --config tools/hummbwtester/hummbwtester.json
```

Logs are written beneath `logs/hummbwtester/`, one file per `client_id` plus `server.log`.

## SSH real-topology runs

For SSH-accessible SCION hosts, configure one file such as `hummbwtester-sciera.json`.
Place every endpoint with `node` on a declared host and give it the IA of that host's AS;
declare only the exact egress flows that may be shaped.
SSH aliases may use `ProxyJump`; the runner uses them unchanged.

Set the password named by `hummingbird.marketplace.password_env`, build the artifact,
set up the persistent SSH resources, and then run the experiment:

```bash
make build-dev
python3 tools/hummbwtester/experiment.py setup --config tools/hummbwtester/hummbwtester-sciera.json
python3 tools/hummbwtester/experiment.py run --config tools/hummbwtester/hummbwtester-sciera.json
```

### How SSH setup works

`scripts/ssh_setup.py` runs entirely on the controller, the machine where `experiment.py` is started.
It reaches the hosts only through their configured SSH aliases: commands run as
`ssh -- <alias> sh -c '<script>'` and files are copied with `scp`.
Every step first compares the current state with the desired one, so repeating setup only changes
what differs. Setup performs these steps in order:

1. **Binary.** On the server, client, and shaping hosts it checks for `sha256sum` and `nc`,
   creates `run_dir`, checks that the SCION daemon port is open, and runs the optional
   `readiness_command`. It then copies `bin/hummbwtester` to `<run_dir>/setup/hummbwtester` unless
   the remote SHA-256 already matches. Copies are atomic: `scp` to a `.tmp` file, verify its digest,
   `chmod`, and `mv`.
2. **Marketplace JWT.** It starts a temporary ssh-control-master that forwards a free controller
   loopback port to `hummingbird.marketplace.url` as seen from `hummingbird.marketplace.host`,
   logs in through it with `marketplace/tools/get_jwt.py`, and closes it. The JWT is copied with
   mode `600` to `<run_dir>/setup/marketplace.jwt` on the Hummingbird client hosts, and the
   controller's copy is deleted. The SSH launch shell reads the JWT file only immediately before
   `exec`; it is never placed in command arguments or the configuration.
3. **Selective qdiscs.** On each shaping host it copies `scripts/selective_qdisc.py` to
   `<run_dir>/setup/` and runs it with `sudo -n`, unless the recorded state in
   `/run/hummbwtester-qdisc/` and the live root qdisc already match (see below).
4. **Static info Note.** Clients find the marketplace of a path in the static info Note that every
   on-path AS puts into its beacons. When `hummingbird.marketplace.scion_address` is set, setup
   pipes `scripts/static_info_note.py` to `sudo -n python3 -` on the server, client, and
   marketplace hosts. The helper inserts or updates one entry named `hummbwtester` at the front of
   the Note's `hummingbird` list, with `api_protocol` `connectrpc/TLS/QUIC/SCION`, `api_address`
   the `scion_address`, and `client_registration_website` the `url`. Other static info settings,
   other Note keys, and other marketplaces' entries are kept; an entry for the same `api_address`
   under another name is replaced, because clients would treat it as a different marketplace.
   The file keeps its owner and mode and is created if missing. Each host's file is `static_info`
   (default `/etc/scion/staticInfoConfig.json`).
5. **Prometheus.** It writes the file-SD targets to `gen/hummbwtester-prometheus/` locally and, with
   a Prometheus configuration and a Docker Compose file, to `/tmp/hummbwtester/prometheus/` on
   `deployment.metrics.prometheus`, where it runs the `hummbwtester-prometheus` container with host
   networking on port `8090`. If the files or the container's configuration hash differ, setup
   removes the old container, replaces the files, and starts it again; otherwise it leaves both alone.
6. **Metric relays.** Each client metrics port and each `deployment.metrics.routers` address gets
   relay port `local_port_base + i`, carried by two ssh-control-masters: one to the source host with
   `-L 127.0.0.1:<port>:<source address>`, and one to the Prometheus host with
   `-R 127.0.0.1:<port>:127.0.0.1:<port>`. Prometheus scrapes its own `127.0.0.1:<port>`, and the
   traffic flows Prometheus host → controller → source host. The control sockets and a manifest
   (a hash of the relay list and every ssh-control-master) live in `/tmp/hummbwtester/ssh-tunnels/`
   on the controller. If the hash matches and every ssh-control-master answers `-O check`, the
   relays are kept; otherwise all of them are replaced.
7. **Manual steps.** Setup never restarts host services. It ends by printing the steps that must be
   done manually, or that none are required; if it fails after changing a file, it still prints the
   steps that change needs. The control service reads its static info file only at startup, so
   every host whose Note changed gets a step to restart its control service, for example:

   ```text
   setup: the following steps must be done manually:
     1. restart the control service on ufms so it reads /etc/scion/staticInfoConfig.json: ssh -t sciera-ufms sudo systemctl restart scion-control@cs-1.service
   ```

   The optional `deployment.hosts.<name>.control_service` names the systemd unit in that step;
   without it, the step shows a `<control service unit>` placeholder.
   Until the restart, and until new beacons have propagated, paths keep advertising the old Note.

An ssh-control-master is a background `ssh -M -S <socket> -f -N` connection that only carries one
port forward. Its control socket lets setup check (`ssh -S <socket> -O check -- <alias>`) and close
(`-O exit`) the connection without tracking process IDs. The marketplace forward of step 2 is a
temporary one whose socket lives in a private temporary directory; the relays of step 6 persist
until teardown.

### Running and tearing down

The SSH runner only launches the already-deployed server and clients.
It fails with an instruction to rerun setup if the deployed binary is missing or differs
from the local build.

When finished, remove the persistent setup with:

```bash
python3 tools/hummbwtester/experiment.py teardown --config tools/hummbwtester/hummbwtester-sciera.json
```

Teardown removes configured selective qdiscs, closes the ssh-control-masters recorded in the
manifest, removes the Prometheus container and its files under `/tmp`, and deletes
`<run_dir>/setup/` (binary, JWT, and helpers). It continues past failures and reports them at the end.
Run it from the same controller as setup, because the control sockets and the manifest are kept
under `/tmp/hummbwtester/ssh-tunnels/` on that controller.
It does not touch the static info files: the `hummbwtester` Note entry that setup advertised stays,
so the marketplace remains discoverable for later experiments. To withdraw it manually, pipe the
helper from the controller to each host, e.g. ufms, and then restart that host's control service:

```bash
ssh sciera-ufms sudo -n python3 - remove --file /etc/scion/staticInfoConfig.json --name hummbwtester \
    < tools/hummbwtester/scripts/static_info_note.py
ssh -t sciera-ufms sudo systemctl restart scion-control@cs-1.service
```

The helper removes only entries with that name and deletes a file that held nothing else.

### Shaping one production BR flow on a shared interface

`scripts/selective_qdisc.py` rate-limits one exact, locally generated IPv4 or IPv6 UDP flow without changing
the border router or its topology.
It replaces an explicitly acknowledged automatic root qdisc with a two-band PRIO qdisc.
A flower filter sends only the selected flow to a TBF in the first band;
everything else uses the unshaped second band. Incoming traffic is unchanged.

This replacement is deliberately explicit.
Pass `--expected-root noqueue` for the SCION VLAN devices on RNP and UFMS,
or `--expected-root mq` for UFES `ens192`.
The latter temporarily replaces the physical NIC's automatic multiqueue hierarchy,
although nonmatching traffic remains rate-unlimited.
`down` deletes the managed hierarchy and verifies that the acknowledged automatic root was restored.

Inspect compatibility without changing the host:

```bash
sudo ./tools/hummbwtester/scripts/selective_qdisc.py diagnose \
  --device ens192 \
  --local 10.6.7.1:50001 \
  --remote 10.6.7.2:50001 \
  --expected-root mq
```

For an IPv6 link-local endpoint, retain brackets and quote the shell arguments:

```bash
sudo ./tools/hummbwtester/scripts/selective_qdisc.py diagnose \
  --name rnp-ufms \
  --device eno4.140 \
  --local '[fe80::77c:140%eno4.140]:50031' \
  --remote '[fe80::2:0:5c:140]:50031' \
  --expected-root noqueue
```

Install the selective qdisc after reviewing the diagnostic report:

```bash
sudo ./tools/hummbwtester/scripts/selective_qdisc.py up \
  --device ens192 \
  --local 10.6.7.1:50001 \
  --remote 10.6.7.2:50001 \
  --expected-root mq \
  --rate 10mbit --burst 50kb --limit 1mb
```

The TBF queue must be larger than the UDP socket's effective send buffer
if the experiment relies on back pressure.
Otherwise, TBF can drop a packet before the socket reaches `EAGAIN`,
and UDP will not report that qdisc drop to the router.
`diagnose` reports the host's `wmem_default` and `wmem_max`;
also account for an explicit SCION `router.send_buffer_size`.
During a run, `status` should show TBF backlog and zero drops until the
BR's own bounded egress queues become the intended drop point.

State is stored under `/run/hummbwtester-qdisc/`.
One managed flow is allowed per interface,
but different `--name` values can manage separate interfaces on the same host.
Partial setup failures restore the acknowledged baseline.
Cleanup refuses to delete a root qdisc that no longer looks like
the hierarchy installed by this helper.

Inspect counters and remove all managed state with:

```bash
sudo ./tools/hummbwtester/scripts/selective_qdisc.py status
sudo ./tools/hummbwtester/scripts/selective_qdisc.py down
```

The implementation uses the Linux [PRIO qdisc](https://man7.org/linux/man-pages/man8/tc-prio.8.html),
[flower classifier](https://man7.org/linux/man-pages/man8/tc-flower.8.html), and
[Token Bucket Filter](https://man7.org/linux/man-pages/man8/tc-tbf.8.html).

An alternative for IPv4 is a veth hairpin: mark the exact locally generated flow,
policy-route it through one end of a veth pair carrying a TBF,
and forward the packet from the peer back to the real egress interface.
This preserves the real interface's root qdisc, but adds policy routing, firewall, forwarding,
and cleanup state.
It is not appropriate for the link-local IPv6 BR links here because
link-local packets cannot be forwarded between the veth and physical links.
Background material:
[veth(4)](https://man7.org/linux/man-pages/man4/veth.4.html),
[Linux network namespaces on Wikipedia](https://en.wikipedia.org/wiki/Linux_namespaces),
and a [web search for veth hairpin policy routing](https://www.google.com/search?q=Linux+veth+hairpin+policy+routing+tc).

SSH setup invokes this helper for every exact flow declared in `deployment.shaping`.
The TBF parameters come from the workload's `tc` object. For example:

```json
"shaping": [
  {
    "host": "ufes",
    "name": "ufes-peer",
    "device": "ens192",
    "local": "10.6.7.1:50001",
    "remote": "10.6.7.2:50001",
    "expected_root": "mq"
  }
]
```

Setup compares the desired configuration with the helper's state and verifies that its managed
root qdisc is present.
An identical active qdisc is retained; stale configured state is removed and recreated.
An empty `deployment.shaping` array disables SSH-mode shaping.

## Regular run cycle

For a configuration change, run setup again before running the experiment:

```bash
python3 tools/hummbwtester/experiment.py setup --config tools/hummbwtester/hummbwtester.json
python3 tools/hummbwtester/experiment.py run --config tools/hummbwtester/hummbwtester.json
```

For a tester source change, first rebuild the standard development artifacts, then run setup:

```bash
make build-dev
python3 tools/hummbwtester/experiment.py setup --config tools/hummbwtester/hummbwtester.json
python3 tools/hummbwtester/experiment.py run --config tools/hummbwtester/hummbwtester.json
```

For a router source change, rebuild and reload the Docker images as well before setup:

```bash
make build-dev
make docker-images
python3 tools/hummbwtester/experiment.py setup --config tools/hummbwtester/hummbwtester.json
python3 tools/hummbwtester/experiment.py run --config tools/hummbwtester/hummbwtester.json
```

After `./scion.sh stop`, Docker removes the bridges and their qdiscs.
Do not use `./scion.sh start` alone for another experiment;
rerun `experiment.py setup` so it starts the existing generated topology,
recreates the bandwidth caps, and recopies the binary.

Docker setup requires an existing `gen/scion-dc.yml`.
If `gen/` was generated for supervisord instead,
regenerate Docker topology with the command in the first-run section.

## Monitoring

Docker setup starts Prometheus automatically. Grafana remains optional and separately managed;
see [monitoring](monitoring/README.md). To start Grafana:

```bash
cd tools/hummbwtester/monitoring
docker compose up -d grafana
```

Prometheus is available at `http://localhost:8090`;
Grafana is at `http://localhost:3000` (`admin` / `admin`).
The dashboard groups client series by `client_id`.

## Tests

Run the focused Go and orchestration tests from the repository root:

```bash
go test ./tools/hummbwtester
bazel test //tools/hummbwtester:go_default_test //tools/hummbwtester:experiment_test \
  //tools/hummbwtester:orchestration_test //tools/hummbwtester:ssh_orchestration_test \
  //tools/hummbwtester:ssh_setup_test //tools/hummbwtester:selective_qdisc_test \
  //tools/hummbwtester:static_info_note_test
```

Run the no-sleep client send-path benchmark with:

```bash
go test ./tools/hummbwtester -run '^$' -bench '^BenchmarkSerializeWriteTo$' -benchmem
```

The Python tests cover configuration validation, deterministic metrics-port assignment,
per-minute report aggregation, selective IPv4/IPv6 qdisc command construction, the SSH setup steps
(with SSH, scp, and ssh-control-masters mocked), and the static info Note edits.
The Go test covers random Hummingbird reservation-ID generation.
A privileged integration test creates disposable network namespaces and verifies IPv4 and
link-local IPv6 selection, UDP back pressure, zero TBF drops, cleanup, and partial-install rollback:

```bash
sudo ./tools/hummbwtester/scripts/selective_qdisc_integration_test.py
```

It self-reexecutes inside an isolated network namespace and does not inspect or modify other interfaces.
The corresponding Bazel target is tagged `manual` because it requires `CAP_NET_ADMIN`.
A practical Docker smoke test is to run setup, stop SCION, and rerun setup.
The generated `hummbwtester_tc_*` helpers inspect qdiscs inside the BR network namespaces.
