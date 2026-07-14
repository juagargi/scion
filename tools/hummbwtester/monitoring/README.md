# hummbwtester monitoring

This directory contains a small Docker Compose setup for observing the Prometheus metrics
exported by `./tools/hummbwtester`.

It follows the same basic pattern as `./monitoring-prometheus-grafana/topology`: a local
Prometheus instance scrapes metrics from the running SCION tooling, and Grafana is provided for
interactive dashboards. For `hummbwtester`, the scrape targets are taken from
`./run-hummbwtester.sh`:

- server metrics: `:9090`
- client metrics: `:9091`

Prometheus also scrapes the border router metrics from the tiny topology, using the same BR
targets as `./monitoring-prometheus-grafana/topology/prometheus.yml`.

Prometheus runs in Docker with host networking so it can reach both the `hummbwtester` host ports
and the BR loopback addresses. The `hummbwtester` client and server should keep running on the
host exactly as they do today.

## Files

- `docker-compose.yml`: starts Prometheus and Grafana
- `prometheus.yml`: scrape configuration for the `hummbwtester` server, client, and border routers
- `grafana/provisioning`: auto-configures the Prometheus data source and dashboard loading
- `grafana/dashboards/hummbwtester-overview.json`: starter Grafana dashboard for traffic,
  latency, jitter, reservation lifetime, border router flyovers, and loss signals

## Start

1. Start `hummbwtester` from the repository root:

   ```bash
   ./run-hummbwtester.sh
   ```

2. In another terminal, start the monitoring stack:

   ```bash
   cd monitoring-prometheus-grafana/hummbwtester
   docker compose up -d
   ```

3. Check that the containers are running:

   ```bash
   docker compose ps
   ```

## Stop

Stop the monitoring stack from this directory:

```bash
docker compose down
```

If you also want to remove the persisted Prometheus and Grafana data volumes:

```bash
docker compose down -v
```

`./run-hummbwtester.sh` stops independently from the monitoring stack, so you can interrupt it
without shutting down Prometheus or Grafana.

## Use

Prometheus is exposed on [http://localhost:8090](http://localhost:8090). The Prometheus UI is
mapped to `8090` instead of `9090` so it does not collide with the `hummbwtester` server metrics
endpoint on the host.

Grafana is exposed on [http://localhost:3000](http://localhost:3000). The default login is
`admin` / `admin`.

If either host port is already in use, override it when starting Compose:

```bash
PROMETHEUS_PORT=18090 GRAFANA_PORT=13000 docker compose up -d
```

You can also put those variables in a local `.env` file in this directory if you want the same
port selection on every run.

## Viewing metrics

### In Prometheus

Open `http://localhost:${PROMETHEUS_PORT:-8090}/targets` and confirm that
`hummbwtester-server`, `hummbwtester-client`, and `scion-border-routers` are `UP`.

Then use the expression browser at `http://localhost:${PROMETHEUS_PORT:-8090}/graph`.
Useful example queries include:

- `hummbwtester_client_send_rate_bps`
- `hummbwtester_client_jitter_seconds`
- `hummbwtester_client_reservation_renewals_total`
- `histogram_quantile(0.95, sum by (le) (rate(hummbwtester_client_rtt_seconds_bucket[1m])))`
- `hummbwtester_server_receive_rate_bps`
- `hummbwtester_server_active_clients`
- `router_humm_flyover_pkts_total`
- `router_priority_forwarded_pkts_total`
- `router_processed_pkts_total`
- `router_bfd_sent_packets_total`
- `router_bfd_received_packets_total`
- `router_humm_demoted_freshness_total`
- `router_humm_demoted_expired_total`
- `router_humm_demoted_tokenbucket_total`
- `router_queue_depth`

### In Grafana

Open `http://localhost:${GRAFANA_PORT:-3000}` and log in with `admin` / `admin`.

The Prometheus data source is provisioned automatically, and the dashboard
`hummbwtester Overview` is loaded automatically in the `hummbwtester` folder.

That dashboard includes:

- traffic rate panels for client send rate and server receive rate
- RTT and jitter panels
- client reservation success rate and active-client panels
- packet rate insights with priority and best-effort packet rates
- border router egress queue depths for priority and best-effort queues
- total border router demotion rates by freshness, expiry, and token bucket cause
- detailed demotion rates by cause, border router, and interface
- pong activity and loss signals

If you want to build your own panels, create a new dashboard in Grafana and query the same
metrics that appear in `./tools/hummbwtester/metrics.go`.
