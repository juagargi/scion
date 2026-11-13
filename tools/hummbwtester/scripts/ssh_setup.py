#!/usr/bin/env python3
"""Install and remove persistent infrastructure for SSH hummbwtester experiments.

Everything runs on the controller, the machine where experiment.py is started. Remote work is
done through the configured SSH aliases: commands go through ssh_orchestration.ssh_command
(`ssh -- <alias> sh -c '<script>'`) and files through scp. Every step compares the current state
with the desired one first, so a repeated setup only changes what differs.

setup performs, in this order:

1. Clocks: the controller and every host must be NTP-synchronized (timedatectl NTPSynchronized),
   and each host's clock within ssh_orchestration.CLOCK_MAX_OFFSET_SECONDS of the controller's,
   measured over one SSH session. The Prometheus agents stamp samples with their host's clock.
   `run` repeats this check.
2. Binary: on every host, report and kill running hummbwtester processes (SIGTERM, then
   SIGKILL after 5 s). Then, on the server, client and shaping hosts, preflight checks (tools,
   run_dir, SCION daemon, readiness_command), and copy bin/hummbwtester to <run_dir>/setup/
   unless its SHA-256 already matches. Copies are atomic: scp to a .tmp file, verify its digest,
   chmod, mv.
3. Marketplace service: on hummingbird.marketplace.host, install bin/marketplace as
   /usr/local/bin/hummingbird-marketplace and the hummingbird-marketplace.service unit with sudo,
   and its config (/etc/scion/marketplace/) and database directory (/var/lib/scion/marketplace/)
   for the SSH user, which the service runs as. Each is replaced only when it differs. The
   service is never enabled and does not restart by itself. It is (re)started, with a database
   rebuilt from the Docker topology's default entries, only when something was replaced, it is
   not running, or its APIs are unreachable; setup then waits until both APIs answer.
4. Marketplace JWT: open a temporary ssh-control-master that forwards a free controller loopback
   port to the host and port of hummingbird.marketplace.scion_address, as seen from
   hummingbird.marketplace.host (its TLS web app), log in through
   it with marketplace/tools/get_jwt.py, close it, and copy the JWT (mode 600) to the hosts of the
   Hummingbird clients. The controller's copy is deleted.
5. Selective qdiscs: on each shaping host, copy selective_qdisc.py to <run_dir>/setup/ and run it
   with sudo -n, unless its recorded state and the live root qdisc already match.
6. Static info Note: on the server, client and marketplace hosts, pipe static_info_note.py to
   `sudo -n python3 -` to insert or update the hummbwtester entry of the Note's hummingbird list.
   With hummingbird.marketplace.location_override, remove it instead, and any entry for the same
   api_address, so that no path announces the marketplace's location.
   Control services are never restarted; a changed file becomes a manual step.
7. Prometheus: write the file-SD targets of the sources on the Prometheus host locally and to
   /var/tmp/hummbwtester-prometheus/ on that host, and (re)create the hummbwtester-prometheus
   container (host networking, listening on 127.0.0.1:8090 only, remote-write receiver on, data
   in the prometheus-data volume) when files or its configuration hash differ.
8. Prometheus agents: every other host with metric sources (clients, routers) runs a Prometheus
   agent, hummbwtester-prometheus-agent: same compose project, files in
   /var/tmp/hummbwtester-prometheus/ there, host networking, listening on 127.0.0.1:8090, WAL in the
   agent-data volume. It scrapes the host's sources with the central configuration's jobs and
   remote-writes them to the central Prometheus. It keeps what it could not deliver until it is
   sent and accepted (up to AGENT_RETENTION; the central Prometheus accepts samples that old
   through out_of_order_time_window). Docker is used directly or with sudo -n. An agent is
   recreated only when its files changed, and only after it has sent its backlog: remote write
   never sends what a stopped agent left unsent.
9. Metric tunnels: each agent's host gets an ssh-control-master with an -R from its loopback
   write port (the relay port reserved for its first source) to the controller's Prometheus
   forward. A metric source on the controller itself would still get an -R relay to the
   Prometheus host. One more ssh-control-master forwards controller port
   deployment.metrics.local_prometheus_port (default 8090), on 127.0.0.1 and on the Docker
   bridge gateway, to the remote Prometheus. Their sockets and a manifest live in
   /tmp/hummbwtester/ssh-tunnels/ on the controller. Then check that every agent delivers.
10. Grafana: run the monitoring stack's hummbwtester-grafana container on the controller, with the
   Docker mode's provisioning, dashboards, and volume, and PROMETHEUS_PORT set to that forward;
   its data source host.docker.internal resolves to the bridge gateway. A running Grafana with
   that setting is kept. Then check that Grafana and its data source answer.
11. Print the manual steps, e.g. restarting a control service whose static info file changed.

teardown stops the marketplace service and waits until it is unreachable, removes the qdiscs,
removes the Prometheus agents (each after it has sent its backlog, up to AGENT_FLUSH_WAIT_SECONDS)
with their volumes and files, closes the ssh-control-masters of the manifest (tunnels and
Prometheus forward; Grafana keeps running, as in the Docker mode), removes the central
Prometheus container and files, and
deletes <run_dir>/setup/ (binary, JWT, helpers). It keeps the static info Note and the
marketplace's binary, unit, config, and database.

With dry_run (`experiment.py setup --dry-run`), every step runs its checks and reads and prints
what it would change ("dry-run: would ..."), but nothing is written on the hosts, no service or
container is started, stopped, or restarted, and no tunnel is started or stopped. The qdisc and
Note helpers are piped in instead of copied, and run read-only (diagnose/status, --dry-run). The
marketplace step still reads the hosts' topologies and derived secret values, and the login of
step 3 still happens, because issuing a JWT only reads the marketplace.

An ssh-control-master is an `ssh -M -S <socket> -f -N` connection that only carries one port
forward; its control socket lets setup check (`ssh -S <socket> -O check`) and close (`-O exit`)
it without tracking PIDs.
"""

from __future__ import annotations

import argparse
from collections.abc import Generator
import contextlib
from dataclasses import asdict, dataclass
import hashlib
import json
import os
from pathlib import Path
import shlex
import socket
import subprocess
import sys
import tempfile
import time
from types import SimpleNamespace
import urllib.error
import urllib.parse
import urllib.request

import yaml

try:  # Support both ``python -m``/tests and the small direct entrypoint scripts.
    from . import orchestration as workload
    from . import selective_qdisc
    from . import ssh_orchestration as remote
except ImportError:
    import orchestration as workload
    import selective_qdisc
    import ssh_orchestration as remote


SELECTIVE_HELPER = workload.ROOT / "tools" / "hummbwtester" / "scripts" / "selective_qdisc.py"
MARKETPLACE_BIN = workload.ROOT / "bin" / "marketplace"
MARKETPLACE_SCHEMA = workload.ROOT / "marketplace" / "db" / "schema.sql"
# The marketplace runs on its host as a manually started, never enabled systemd service.
MARKETPLACE_SERVICE = "hummingbird-marketplace.service"
REMOTE_MARKETPLACE_BIN = "/usr/local/bin/hummingbird-marketplace"
REMOTE_MARKETPLACE_UNIT = f"/etc/systemd/system/{MARKETPLACE_SERVICE}"
REMOTE_MARKETPLACE_CONFIG_DIR = "/etc/scion/marketplace"
REMOTE_MARKETPLACE_CONFIG = f"{REMOTE_MARKETPLACE_CONFIG_DIR}/marketplace.toml"
REMOTE_MARKETPLACE_DB_DIR = "/var/lib/scion/marketplace"
REMOTE_MARKETPLACE_DB = f"{REMOTE_MARKETPLACE_DB_DIR}/marketplace.db"
# The settings the database was built from (asset interfaces, initial user
# balance); a change rebuilds the database.
REMOTE_MARKETPLACE_ASSETS = f"{REMOTE_MARKETPLACE_DB_DIR}/assets.json"
# The SCION configuration of every host: topology.json, certs/, and keys/master0.key.
SCION_CONFIG_DIR = "/etc/scion"
# How long setup waits for a started marketplace, and teardown for a stopped one.
MARKETPLACE_WAIT_SECONDS = 30
STATIC_INFO_HELPER = workload.ROOT / "tools" / "hummbwtester" / "scripts" / "static_info_note.py"
# The Note entry name that marks the marketplace advertisement owned by these experiments.
NOTE_NAME = "hummbwtester"
NOTE_PROTOCOL = "connectrpc/TLS/QUIC/SCION"
PROMETHEUS_CONFIG = workload.ROOT / "tools" / "hummbwtester" / "monitoring" / "prometheus.yml"
# Outside /tmp, which a reboot empties: Docker restarts the containers at boot, and their files
# must still be there (a missing bind source fails the start, see _config_mounts).
REMOTE_PROMETHEUS_DIR = "/var/tmp/hummbwtester-prometheus"
PROMETHEUS_CONTAINER = "hummbwtester-prometheus"
PROMETHEUS_PROJECT = "hummbwtester-ssh"
PROMETHEUS_IMAGE = "prom/prometheus:latest"
PROMETHEUS_PORT = 8090
CONFIG_HASH_LABEL = "org.scion.hummbwtester.config-sha256"
# Grafana runs on the controller: the monitoring stack's service, shared with the Docker mode.
GRAFANA_CONTAINER = "hummbwtester-grafana"
GRAFANA_WAIT_SECONDS = 30
TUNNEL_STATE_DIR = Path("/tmp/hummbwtester/ssh-tunnels")
TUNNEL_MANIFEST = TUNNEL_STATE_DIR / "manifest.json"
# How long setup waits for the controller port of a Prometheus forward it has just closed.
PORT_RELEASE_WAIT_SECONDS = 10
# Prometheus agents (one per host with metric sources, other than the Prometheus host) scrape
# their host's sources and remote-write them to the central Prometheus. An agent keeps samples it
# has not delivered in its WAL until they are sent and accepted, up to AGENT_RETENTION; the
# central Prometheus accepts samples that old (out_of_order_time_window in prometheus.yml).
AGENT_SERVICE = "agent"
AGENT_RETENTION = "180d"
# Fixed number of remote-write shards. With a variable number, the queue reshards on its own,
# and stopping the old shards cancels their in-flight batches after the flush deadline (1 min):
# while the central Prometheus is unreachable those batches are dropped ("context canceled"),
# never retried. One shard sends about 2000 samples per round trip through the tunnel; four
# work off a backlog of an hour in minutes (an agent scrapes about 1200 samples/s on SCIERA).
AGENT_SHARDS = 4
# How long setup waits for a new agent to deliver, and setup or teardown for an agent to send its
# backlog before it is recreated or removed: remote write only resends the WAL of a running agent.
AGENT_DELIVERY_WAIT_SECONDS = 60
AGENT_FLUSH_WAIT_SECONDS = 120
# An agent counts as delivering when the newest sample it sent is at most this much older than
# the newest it scraped.
AGENT_MAX_BACKLOG_SECONDS = 10.0


@dataclass(frozen=True)
class MonitoringStack:
    """The identity of one tool's monitoring stack: what it names and where it keeps state.

    Two tools with different stacks can run side by side: neither replaces the other's
    Prometheus or Grafana containers, nor closes its relays.
    """
    prometheus_config: Path
    remote_dir: str
    prometheus_container: str
    prometheus_project: str
    remote_prometheus_port: int
    config_hash_label: str
    grafana_container: str
    grafana_compose: Path
    grafana_project: str
    tunnel_state_dir: Path
    # The relay manifest; None means manifest.json in tunnel_state_dir.
    tunnel_manifest: Path | None = None

    @property
    def manifest(self) -> Path:
        return self.tunnel_manifest or self.tunnel_state_dir / "manifest.json"


def hummbwtester_stack() -> MonitoringStack:
    """hummbwtester's own stack, read from the module constants above when called."""
    return MonitoringStack(
        PROMETHEUS_CONFIG, REMOTE_PROMETHEUS_DIR, PROMETHEUS_CONTAINER, PROMETHEUS_PROJECT,
        PROMETHEUS_PORT, CONFIG_HASH_LABEL, GRAFANA_CONTAINER, workload.MONITORING_COMPOSE,
        workload.MONITORING_PROJECT, TUNNEL_STATE_DIR, TUNNEL_MANIFEST,
    )


HUMMBWTESTER_STACK = hummbwtester_stack()


def _stack(stack: MonitoringStack | None) -> MonitoringStack:
    # None resolves at call time, so that the module constants stay the single source of truth
    # for hummbwtester itself (and patchable); other tools pass their own stack.
    return stack if stack is not None else hummbwtester_stack()


@dataclass(frozen=True)
class MetricSource:
    """A metrics endpoint: address on its host, and the relay port reserved for it."""
    # The inventory host the endpoint is on; None is the controller itself.
    host: str | None
    address: str
    port: int
    labels: dict[str, str]
    # The Prometheus file-SD file the target goes to, e.g. "targets/clients.json".
    target_file: str


@dataclass(frozen=True)
class PrometheusForward:
    """Controller port, bound on each address of binds, forwarded to the remote Prometheus.

    Part of the reuse API (README.md, "Reuse from other tools").
    """
    alias: str
    port: int
    binds: tuple[str, ...]


@dataclass(frozen=True)
class TunnelSpec:
    # None: the controller reaches the source directly, so only the -R relay is needed.
    source_alias: str | None
    prometheus_alias: str
    port: int
    source_address: str


def setup_hosts(inventory: remote.Inventory) -> list[str]:
    """The hosts that receive the binary and helpers: participants and shaping hosts.

    Part of the reuse API (README.md, "Reuse from other tools").
    """
    return sorted({
        *inventory.participant_hosts,
        *(shape.host for shape in inventory.shaping),
    })


def _remote_test(host: remote.SSHHost, command: str) -> bool:
    result = remote.ssh_command(
        host, command, check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def copy_if_changed(
    host: remote.SSHHost, local: Path, target: str, mode: str, dry_run: bool = False,
) -> bool:
    """Copy a file atomically when its content differs; return whether it changed (or would)."""
    digest = remote.sha256(local)
    if _remote_test(host, remote.digest_matches(target, digest)):
        return False
    if dry_run:
        return True
    temporary = target + ".tmp"
    remote.ssh_command(
        host, f"install -d -m 700 {shlex.quote(str(Path(target).parent))}",
    )
    remote.scp_to(host, local, temporary)
    remote.ssh_command(host, " && ".join([
        remote.digest_matches(temporary, digest),
        f"chmod {mode} {shlex.quote(temporary)}",
        f"mv -f {shlex.quote(temporary)} {shlex.quote(target)}",
    ]))
    return True


def deploy_binary(host: remote.SSHHost, dry_run: bool = False) -> None:
    target = remote.setup_dir(host) + "/hummbwtester"
    if copy_if_changed(host, remote.BIN, target, "700", dry_run):
        print(f"{'dry-run: would deploy' if dry_run else 'deployed'} hummbwtester to {host.name}")
    else:
        print(f"hummbwtester on {host.name} is current")


def _free_local_port() -> int:
    # The port may be taken again before ssh binds it; ExitOnForwardFailure then makes ssh fail.
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
        probe.bind(("127.0.0.1", 0))
        return probe.getsockname()[1]


def _tunneled_url(url: str, local_port: int) -> tuple[str, str]:
    """Return the remote host:port that url names and url rewritten to a local forward."""
    parts = urllib.parse.urlsplit(url)
    try:
        port = parts.port or (443 if parts.scheme == "https" else 80)
    except ValueError as err:
        raise workload.ConfigError(
            f"marketplace url has an invalid port: {url}",
        ) from err
    if not parts.hostname:
        raise workload.ConfigError(f"marketplace url has no host: {url}")
    host = f"[{parts.hostname}]" if ":" in parts.hostname else parts.hostname
    local = parts._replace(netloc=f"127.0.0.1:{local_port}")
    return f"{host}:{port}", urllib.parse.urlunsplit(local)


@contextlib.contextmanager
def marketplace_tunnel(host: remote.SSHHost, url: str) -> Generator[str, None, None]:
    """Forward a controller loopback port to url as seen from host, yielding the local url.

    The forward is a temporary ssh-control-master whose socket lives in a private temporary
    directory; it exists only inside the with block and is not recorded in the tunnel manifest.
    """
    local_port = _free_local_port()
    target, local_url = _tunneled_url(url, local_port)
    with tempfile.TemporaryDirectory(prefix="hummbwtester-market-") as directory:
        control_master = _start_ssh_control_master(
            ["-L", f"127.0.0.1:{local_port}:{target}"],
            Path(directory) / "marketplace.sock", host.alias,
        )
        print(f"tunneling 127.0.0.1:{local_port} to marketplace {target} on {host.name}")
        try:
            yield local_url
        finally:
            _stop_ssh_control_master(control_master)


def obtain_marketplace_jwt(inventory: remote.Inventory) -> str:
    """Log in at the marketplace web app as seen from its host, through a temporary tunnel."""
    marketplace = inventory.marketplace
    url = marketplace.url
    assert url is not None  # load_config requires scion_address for SSH deployments.
    host = inventory.hosts[inventory.marketplace_host]
    # Checked before the tunnel is opened, which would be pointless without a password.
    workload.require_marketplace_password(marketplace)
    with marketplace_tunnel(host, url) as local_url:
        try:
            return workload.obtain_marketplace_jwt(marketplace, local_url)
        except workload.ConfigError as err:
            raise workload.ConfigError(
                f"{err} (tunneled to {url} on {host.name})",
            ) from err


def deploy_jwt(
    inventory: remote.Inventory, clients: list[workload.Client], local_jwt: Path,
    dry_run: bool = False,
) -> None:
    for name in sorted(remote.hummingbird_hosts(inventory, clients)):
        host = inventory.hosts[name]
        if copy_if_changed(host, local_jwt, remote.jwt_path(host), "600", dry_run):
            print(f"{'dry-run: would upload' if dry_run else 'uploaded'} marketplace JWT to {name}")
        else:
            print(f"marketplace JWT on {name} is current")


def _topology_marketplace():
    """The marketplace module of the topology generator, which also configures Docker mode."""
    tools = str(workload.ROOT / "tools")
    if tools not in sys.path:
        sys.path.insert(0, tools)
    from topology import marketplace as topology_marketplace
    return topology_marketplace


def _scion_endpoint(scion_address: str) -> tuple[str, str, int]:
    match = workload.SCION_ADDRESS_RE.fullmatch(scion_address)
    assert match is not None  # load_config validated it.
    return match.group("ia"), match.group("host"), int(match.group("port"))


def _host_port(host: str, port: int) -> str:
    return f"[{host}]:{port}" if ":" in host else f"{host}:{port}"


def marketplace_toml(marketplace: workload.MarketplaceConfig) -> str:
    """The marketplace config, rendered from the same template as the Docker topology's."""
    assert marketplace.scion_address is not None  # load_config requires it for SSH deployments.
    _, scion_host, scion_port = _scion_endpoint(marketplace.scion_address)
    return _topology_marketplace().MARKETPLACE_TOML.format(
        config_dir=REMOTE_MARKETPLACE_CONFIG_DIR,
        # The TCP and the SCION API listen on the same host and port.
        api_addr=_host_port(scion_host, scion_port),
        scion_api_addr=_host_port(scion_host, scion_port),
        db_path=REMOTE_MARKETPLACE_DB,
    )


def marketplace_unit(user: str, group: str) -> str:
    """The systemd unit; without an [Install] section it cannot be enabled, only started."""
    return f"""[Unit]
Description=Hummingbird marketplace for hummbwtester experiments
After=network-online.target

[Service]
Type=simple
User={user}
Group={group}
Environment=TZ=UTC
ExecStart={REMOTE_MARKETPLACE_BIN} --config {REMOTE_MARKETPLACE_CONFIG}
Restart=no
"""


# Run on the marketplace host. The TLS web app counts as reachable once it answers any HTTP
# status; its certificate is self-signed, so it is not verified.
_HTTPS_PROBE = """import ssl, sys, urllib.error, urllib.request
context = ssl.create_default_context()
context.check_hostname = False
context.verify_mode = ssl.CERT_NONE
try:
    urllib.request.urlopen(sys.argv[1], context=context, timeout=3)
except urllib.error.HTTPError:
    pass
except Exception:
    sys.exit(1)
"""


def _marketplace_probe(marketplace: workload.MarketplaceConfig) -> str:
    """A shell test that succeeds while both marketplace APIs listen on the marketplace host."""
    assert marketplace.scion_address is not None
    _, _, scion_port = _scion_endpoint(marketplace.scion_address)
    assert marketplace.url is not None
    login = marketplace.url.rstrip("/") + "/login"
    return (f"python3 -c {shlex.quote(_HTTPS_PROBE)} {shlex.quote(login)} && "
            f"ss -Hlun {shlex.quote(f'sport = :{scion_port}')} | grep -q .")


def _wait_for(host: remote.SSHHost, probe: str, reachable: bool, seconds: int) -> bool:
    """Poll probe on host once per second until it succeeds (or fails, if not reachable)."""
    condition = probe if reachable else f"! {{ {probe}; }}"
    script = (f"i=0; while [ $i -lt {seconds} ]; do if {condition}; then exit 0; fi; "
              "i=$((i + 1)); sleep 1; done; exit 1")
    return _remote_test(host, script)


def _service_active(host: remote.SSHHost) -> bool:
    return _remote_test(host, f"systemctl is-active --quiet {MARKETPLACE_SERVICE}")


def _systemctl(host: remote.SSHHost, verb: str) -> None:
    remote.ssh_command(host, shlex.join(["sudo", "-n", "systemctl", verb, MARKETPLACE_SERVICE]))


def install_root_file(
    host: remote.SSHHost, local: Path, target: str, mode: str, dry_run: bool = False,
) -> bool:
    """Install a root-owned file with sudo when its content differs; return whether it changed.

    Like copy_if_changed, it compares SHA-256 digests and verifies the copy before installing it.
    """
    digest = remote.sha256(local)
    if _remote_test(host, remote.digest_matches(target, digest)):
        return False
    if dry_run:
        return True
    staging = remote.ssh_command(host, "mktemp -d", capture_output=True).stdout.strip()
    temporary = f"{staging}/{Path(target).name}"
    try:
        remote.scp_to(host, local, temporary)
        remote.ssh_command(host, " && ".join([
            remote.digest_matches(temporary, digest),
            shlex.join(["sudo", "-n", "install", "-o", "root", "-g", "root", "-m", mode,
                        temporary, target]),
        ]))
    finally:
        remote.ssh_command(host, f"rm -rf {shlex.quote(staging)}", check=False)
    return True


def _ensure_directory(
    host: remote.SSHHost, path: str, owner: str, mode: str, dry_run: bool,
) -> bool:
    """Create path owned by owner (user:group) with mode, using sudo, unless it already is."""
    if _remote_test(host, f"test \"$(stat -c %U:%G:%a {shlex.quote(path)})\" = "
                          f"{shlex.quote(f'{owner}:{mode}')}"):
        return False
    if not dry_run:
        user, group = owner.split(":")
        remote.ssh_command(host, shlex.join(["sudo", "-n", "install", "-d", "-o", user, "-g", group,
                                             "-m", mode, path]))
    return True


def _ensure_symlink(host: remote.SSHHost, link: str, target: str, dry_run: bool) -> bool:
    if _remote_test(host, f"test \"$(readlink {shlex.quote(link)})\" = {shlex.quote(target)}"):
        return False
    if not dry_run:
        remote.ssh_command(host, f"ln -sfn {shlex.quote(target)} {shlex.quote(link)}")
    return True


def _read_topology(host: remote.SSHHost) -> dict[str, object]:
    result = remote.ssh_command(host, f"cat {SCION_CONFIG_DIR}/topology.json", capture_output=True)
    try:
        topology = json.loads(result.stdout)
    except json.JSONDecodeError as err:
        raise remote.SSHError(f"invalid {SCION_CONFIG_DIR}/topology.json on {host.name}") from err
    if not isinstance(topology, dict) or not isinstance(topology.get("isd_as"), str):
        raise remote.SSHError(f"{SCION_CONFIG_DIR}/topology.json on {host.name} has no isd_as")
    return topology


# Run as root on a host: print the Hummingbird secret value derived from the AS master key, the
# same derivation as the routers and the Docker topology, so that the key itself stays there.
_SECRET_VALUE = """import base64, hashlib, sys
master = base64.b64decode(open(sys.argv[1]).read().strip(), validate=True)
print(hashlib.pbkdf2_hmac("sha256", master, b"Derive hbird sv", 1000, 16).hex())
"""


def _secret_value(host: remote.SSHHost) -> bytes:
    result = remote.ssh_command(
        host, shlex.join(["sudo", "-n", "python3", "-c", _SECRET_VALUE,
                          f"{SCION_CONFIG_DIR}/keys/master0.key"]),
        check=False, capture_output=True,
    )
    try:
        return bytes.fromhex(result.stdout.strip()) if result.returncode == 0 else b""
    except ValueError:
        return b""


def marketplace_assets(marketplace: workload.MarketplaceConfig) -> str:
    """The settings the marketplace database is built from: the configured interfaces, which
    determine the assets it offers, and the initial balance of its users."""
    interfaces = (None if marketplace.interfaces is None
                  else {ia: list(ids) for ia, ids in marketplace.interfaces.items()})
    settings = {
        "interfaces": interfaces,
        "initial_user_balance": _topology_marketplace().DEFAULT_USER_BALANCE,
    }
    return json.dumps(settings, indent=2, sort_keys=True) + "\n"


def marketplace_entries(inventory: remote.Inventory) -> dict[str, object]:
    """The Docker topology's default marketplace entries for the ASes advertising the marketplace.

    Users, accounts, assets, and redemption delegations are the ones a generated Docker topology
    starts with, except that the configured Hummingbird interfaces, if any, restrict the assets
    to their pairs. Only topology.json and the derived secret value of every AS are read from the
    hosts.
    """
    topology_marketplace = _topology_marketplace()
    secret_values: dict[str, bytes] = {}
    with tempfile.TemporaryDirectory(prefix="hummbwtester-market-gen-") as directory:
        for name in note_hosts(inventory):
            host = inventory.hosts[name]
            topology = _read_topology(host)
            ia = str(topology["isd_as"])
            secret = _secret_value(host)
            if len(secret) != topology_marketplace.SECRET_VALUE_LENGTH:
                raise remote.SSHError(
                    f"cannot derive the Hummingbird secret value from "
                    f"{SCION_CONFIG_DIR}/keys/master0.key on {name}",
                )
            secret_values[ia] = secret
            as_dir = Path(directory) / f"AS{name}"
            as_dir.mkdir()
            (as_dir / "topology.json").write_text(json.dumps(topology))
        try:
            return topology_marketplace.defaultEntries(
                directory, secret_values=secret_values,
                interfaces=inventory.marketplace.interfaces,
            )
        except topology_marketplace.MarketplaceError as err:
            raise workload.ConfigError(f"cannot build the marketplace database: {err}") from err


def _reconfigure_marketplace_db(
    host: remote.SSHHost, entries: dict[str, object], assets: str,
) -> None:
    """Replace the database of the stopped marketplace with one built from entries and record
    the settings it was built from."""
    with tempfile.TemporaryDirectory(prefix="hummbwtester-market-db-") as directory:
        database = Path(directory) / "marketplace.db"
        _topology_marketplace().populateDB(str(database), str(MARKETPLACE_SCHEMA), entries)
        # Journal files of the old database would be applied to the new one.
        remote.ssh_command(host, "rm -f " + " ".join(
            shlex.quote(REMOTE_MARKETPLACE_DB + suffix)
            for suffix in ("", "-wal", "-shm", "-journal")
        ))
        copy_if_changed(host, database, REMOTE_MARKETPLACE_DB, "600")
        settings = Path(directory) / "assets.json"
        settings.write_text(assets)
        copy_if_changed(host, settings, REMOTE_MARKETPLACE_ASSETS, "600")


def _check_marketplace_host(host: remote.SSHHost, marketplace: workload.MarketplaceConfig) -> None:
    """Check the tools the marketplace step uses, and that scion_address fits the host's AS."""
    remote.ssh_command(host, " && ".join(
        f"command -v {tool} >/dev/null" for tool in ("python3", "systemctl", "ss", "sha256sum")
    ))
    assert marketplace.scion_address is not None
    ia, _, port = _scion_endpoint(marketplace.scion_address)
    topology = _read_topology(host)
    if topology["isd_as"] != ia:
        raise workload.ConfigError(
            f"hummingbird.marketplace.scion_address names {ia}, but {host.name} is in "
            f"{topology['isd_as']}",
        )
    topology_marketplace = _topology_marketplace()
    with tempfile.TemporaryDirectory() as directory:
        (Path(directory) / "topology.json").write_text(json.dumps(topology))
        try:
            topology_marketplace.checkDispatchedPorts(directory, port)
        except topology_marketplace.MarketplaceError as err:
            raise workload.ConfigError(f"{host.name}: {err}") from err


def ensure_marketplace(inventory: remote.Inventory, dry_run: bool = False) -> None:
    """Install, configure, and run the marketplace on its host until it is reachable.

    The binary, unit, and config are replaced when they differ. The service is (re)started, with
    a rebuilt database, only when one of them changed, it is not running, or it is unreachable.
    """
    if not MARKETPLACE_BIN.is_file():
        raise RuntimeError(f"{MARKETPLACE_BIN} does not exist; run `make build-dev` first")
    marketplace = inventory.marketplace
    host = inventory.hosts[inventory.marketplace_host]
    _check_marketplace_host(host, marketplace)
    owner = remote.ssh_command(host, "printf '%s:%s' \"$(id -un)\" \"$(id -gn)\"",
                               capture_output=True).stdout.strip()

    changed = []
    if install_root_file(host, MARKETPLACE_BIN, REMOTE_MARKETPLACE_BIN, "755", dry_run):
        changed.append(f"binary {REMOTE_MARKETPLACE_BIN}")
    with tempfile.TemporaryDirectory(prefix="hummbwtester-market-") as directory:
        unit = Path(directory) / MARKETPLACE_SERVICE
        unit.write_text(marketplace_unit(*owner.split(":")))
        if install_root_file(host, unit, REMOTE_MARKETPLACE_UNIT, "644", dry_run):
            changed.append(f"unit {REMOTE_MARKETPLACE_UNIT}")
            if not dry_run:
                remote.ssh_command(host, "sudo -n systemctl daemon-reload")
        # Only the service user needs them; copy_if_changed also keeps its target directory at 700.
        for path in (REMOTE_MARKETPLACE_CONFIG_DIR, REMOTE_MARKETPLACE_DB_DIR):
            if _ensure_directory(host, path, owner, "700", dry_run):
                changed.append(f"directory {path}")
        config = Path(directory) / "marketplace.toml"
        config.write_text(marketplace_toml(marketplace))
        if copy_if_changed(host, config, REMOTE_MARKETPLACE_CONFIG, "640", dry_run):
            changed.append(f"config {REMOTE_MARKETPLACE_CONFIG}")
        # Recorded only once a database is built from them (see _reconfigure_marketplace_db).
        assets = Path(directory) / "assets.json"
        assets.write_text(marketplace_assets(marketplace))
        if copy_if_changed(host, assets, REMOTE_MARKETPLACE_ASSETS, "600", dry_run=True):
            changed.append(f"database settings {REMOTE_MARKETPLACE_ASSETS}")
    for name in ("topology.json", "certs"):
        if _ensure_symlink(host, f"{REMOTE_MARKETPLACE_CONFIG_DIR}/{name}",
                           f"{SCION_CONFIG_DIR}/{name}", dry_run):
            changed.append(f"link {REMOTE_MARKETPLACE_CONFIG_DIR}/{name}")
    for item in changed:
        print(f"{'dry-run: would install' if dry_run else 'installed'} marketplace {item} "
              f"on {host.name}")

    probe = _marketplace_probe(marketplace)
    active = _service_active(host)
    if active and not changed:
        if _remote_test(host, probe):
            print(f"marketplace {MARKETPLACE_SERVICE} on {host.name} is running and reachable")
            return
        print(f"marketplace {MARKETPLACE_SERVICE} on {host.name} is running but unreachable")
    # The database is rebuilt only while the service is stopped for a (re)start.
    entries = marketplace_entries(inventory)
    if dry_run:
        print(f"dry-run: would {'restart' if active else 'start'} {MARKETPLACE_SERVICE} on "
              f"{host.name} with a rebuilt database ({len(entries['assets'])} assets of "
              f"{len(entries['ases'])} ASes; it resets balances, purchases, and reservations) "
              "and wait until it is reachable")
        return
    if active:
        _systemctl(host, "stop")
    _reconfigure_marketplace_db(host, entries, marketplace_assets(marketplace))
    _systemctl(host, "start")
    if not _wait_for(host, probe, True, MARKETPLACE_WAIT_SECONDS):
        raise remote.SSHError(
            f"{MARKETPLACE_SERVICE} on {host.name} is not reachable after "
            f"{MARKETPLACE_WAIT_SECONDS} s; see: ssh -t {host.alias} "
            f"sudo journalctl -u {MARKETPLACE_SERVICE}",
        )
    print(f"{'restarted' if active else 'started'} {MARKETPLACE_SERVICE} on {host.name}; "
          "it is reachable")


def stop_marketplace(inventory: remote.Inventory) -> None:
    """Stop the marketplace service and wait until neither of its APIs listens anymore."""
    host = inventory.hosts[inventory.marketplace_host]
    if _service_active(host):
        _systemctl(host, "stop")
        print(f"stopped {MARKETPLACE_SERVICE} on {host.name}")
    if not _wait_for(host, _marketplace_probe(inventory.marketplace), False,
                     MARKETPLACE_WAIT_SECONDS):
        raise remote.SSHError(
            f"the marketplace on {host.name} is still reachable after stopping "
            f"{MARKETPLACE_SERVICE}",
        )


def note_entry(marketplace: workload.MarketplaceConfig) -> dict[str, str] | None:
    """The hummingbird Note entry advertising the configured marketplace over SCION."""
    if marketplace.scion_address is None:
        return None
    return {
        "name": NOTE_NAME,
        "api_protocol": NOTE_PROTOCOL,
        "api_address": marketplace.scion_address,
        "client_registration_website": marketplace.url,
    }


def note_hosts(inventory: remote.Inventory) -> list[str]:
    """The hosts whose ASes advertise the marketplace: every participant and the marketplace."""
    return sorted({*inventory.participant_hosts, inventory.marketplace_host})


def _static_info_note(
    host: remote.SSHHost, action: str, *arguments: str, dry_run: bool = False,
) -> bool:
    """Run the Note helper as root on host, piping it in; return whether the file changed.

    With dry_run the helper only reports whether the file would change.
    """
    command = shlex.join([
        "sudo", "-n", "python3", "-", action, "--file", host.static_info, *arguments,
        *(["--dry-run"] if dry_run else []),
    ])
    result = remote.ssh_command(
        host, command, check=False, capture_output=True, input=STATIC_INFO_HELPER.read_text(),
    )
    if result.returncode:
        raise remote.SSHError(
            f"updating {host.static_info} on {host.name}: {result.stderr.strip()}",
        )
    try:
        return json.loads(result.stdout)["changed"] is True
    except (json.JSONDecodeError, KeyError, TypeError) as err:
        raise remote.SSHError(
            f"unexpected static info helper output on {host.name}: {result.stdout!r}",
        ) from err


def restart_instruction(host: remote.SSHHost) -> str:
    """The manual step that makes the control service of host read a changed static info file."""
    # Setup never restarts production services; the operator decides when the restart happens.
    unit = host.control_service or "<control service unit>"
    return (f"restart the control service on {host.name} so it reads {host.static_info}: "
            f"ssh -t {host.alias} sudo systemctl restart {unit}")


def ensure_marketplace_notes(inventory: remote.Inventory, dry_run: bool = False) -> list[str]:
    """Advertise the marketplace, or withdraw it with location_override, and return the manual
    steps the changed files need."""
    entry = note_entry(inventory.marketplace)
    if entry is None:
        print("hummingbird.marketplace.scion_address is not set; static info Notes are unchanged")
        return []
    if inventory.marketplace.location_override:
        return withdraw_marketplace_notes(inventory, entry, dry_run)
    manual = []
    for name in note_hosts(inventory):
        host = inventory.hosts[name]
        if _static_info_note(host, "ensure", "--entry", json.dumps(entry, sort_keys=True),
                             dry_run=dry_run):
            print(f"{'dry-run: would advertise' if dry_run else 'advertised'} marketplace "
                  f"{entry['api_address']} in {host.static_info} on {name}")
            manual.append(restart_instruction(host))
        else:
            print(f"marketplace Note in {host.static_info} on {name} is current")
    return manual


def withdraw_marketplace_notes(
    inventory: remote.Inventory, entry: dict[str, str], dry_run: bool = False,
) -> list[str]:
    """Remove the marketplace from the Notes, so that no path announces where it is.

    With location_override the clients are told the location instead, and the experiment must
    work without the metadata. Entries for its address under another name go too.
    """
    manual = []
    for name in note_hosts(inventory):
        host = inventory.hosts[name]
        if _static_info_note(host, "remove", "--name", entry["name"],
                             "--api-address", entry["api_address"], dry_run=dry_run):
            print(f"{'dry-run: would withdraw' if dry_run else 'withdrew'} marketplace "
                  f"{entry['api_address']} from {host.static_info} on {name} "
                  "(location_override)")
            manual.append(restart_instruction(host))
        else:
            print(f"{host.static_info} on {name} does not advertise the marketplace "
                  "(location_override)")
    return manual


def print_manual_steps(action: str, steps: list[str], dry_run: bool = False) -> None:
    if dry_run:
        if not steps:
            print(f"dry-run: {action} would require no manual steps")
            return
        print(f"dry-run: {action} would require these manual steps:")
    elif not steps:
        print(f"{action}: no manual steps are required")
        return
    else:
        print(f"{action}: the following steps must be done manually:")
    for index, step in enumerate(steps, start=1):
        print(f"  {index}. {step}")


def selective_config(shape: remote.ShapedFlow, tc: dict[str, str]) -> selective_qdisc.Config:
    try:
        args = SimpleNamespace(
            name=shape.name,
            device=shape.device,
            local=selective_qdisc.Endpoint.parse(shape.local),
            remote=selective_qdisc.Endpoint.parse(shape.remote),
            rate=selective_qdisc.validate_tc(tc["rate"]),
            burst=selective_qdisc.validate_tc(tc["burst"]),
            limit=selective_qdisc.validate_tc(tc["limit"]),
            expected_root=shape.expected_root,
        )
        return selective_qdisc.build_config(args)
    except (argparse.ArgumentTypeError, selective_qdisc.Error) as err:
        raise workload.ConfigError(f"invalid shaping entry {shape.name}: {err}") from err


def _selective_command(helper: str, action: str, config: selective_qdisc.Config) -> str:
    arguments = ["sudo", "-n", "python3", helper, action, "--name", config.name]
    if action in ("up", "diagnose"):
        arguments.extend([
            "--device", config.device,
            "--local", str(config.local),
            "--remote", str(config.remote),
            "--rate", config.rate,
            "--burst", config.burst,
            "--limit", config.limit,
            "--expected-root", config.expected_root,
        ])
    return shlex.join(arguments)


def _qdisc_state(host: remote.SSHHost, name: str) -> dict[str, object] | None:
    path = f"/run/hummbwtester-qdisc/{name}.json"
    script = f"if test -f {shlex.quote(path)}; then cat {shlex.quote(path)}; else exit 44; fi"
    result = remote.ssh_command(
        host, "sudo -n sh -c " + shlex.quote(script), check=False, capture_output=True,
    )
    if result.returncode == 44:
        return None
    if result.returncode:
        raise remote.SSHError(
            f"reading selective qdisc state {name} on {host.name}: {result.stderr.strip()}",
        )
    try:
        value = json.loads(result.stdout)
    except json.JSONDecodeError as err:
        raise remote.SSHError(
            f"invalid selective qdisc state {name} on {host.name}: {err}",
        ) from err
    if not isinstance(value, dict):
        raise remote.SSHError(f"invalid selective qdisc state {name} on {host.name}")
    return value


def _run_selective(
    host: remote.SSHHost, helper: str, action: str, config: selective_qdisc.Config, **kwargs,
) -> subprocess.CompletedProcess[str]:
    """Run the qdisc helper; helper "-" pipes the local copy in instead of using a deployed one."""
    if helper == "-":
        kwargs["input"] = SELECTIVE_HELPER.read_text()
    return remote.ssh_command(host, _selective_command(helper, action, config), **kwargs)


def _qdisc_is_active(
    host: remote.SSHHost, helper: str, config: selective_qdisc.Config,
) -> bool:
    result = _run_selective(host, helper, "status", config, check=False, capture_output=True)
    if result.returncode:
        return False
    try:
        return json.loads(result.stdout).get("managed_root_present") is True
    except json.JSONDecodeError:
        return False


def _diagnose_selective_qdisc(host: remote.SSHHost, config: selective_qdisc.Config) -> None:
    """Run the read-only diagnose action; raise if installing the qdisc would fail."""
    result = _run_selective(host, "-", "diagnose", config, check=False, capture_output=True)
    try:
        report = json.loads(result.stdout)
    except json.JSONDecodeError:
        report = None
    if not isinstance(report, dict):
        raise remote.SSHError(
            f"diagnosing selective qdisc {config.name} on {host.name}: {result.stderr.strip()}",
        )
    if report.get("warnings"):
        raise remote.SSHError(
            f"selective qdisc {config.name} on {host.name} could not be installed: "
            + "; ".join(report["warnings"]),
        )


def ensure_selective_qdiscs(
    inventory: remote.Inventory, tc: dict[str, str], dry_run: bool = False,
) -> None:
    for shape in inventory.shaping:
        host = inventory.hosts[shape.host]
        config = selective_config(shape, tc)
        if dry_run:
            helper = "-"
        else:
            helper = remote.setup_dir(host) + "/selective_qdisc.py"
            copy_if_changed(host, SELECTIVE_HELPER, helper, "700")
        desired = asdict(config)
        current = _qdisc_state(host, config.name)
        if current == desired and _qdisc_is_active(host, helper, config):
            print(f"selective qdisc {config.name} on {host.name} is current")
            continue
        if dry_run:
            if current is None:
                # The helper checks the interface, address, route, and root qdisc that up needs.
                _diagnose_selective_qdisc(host, config)
                print(f"dry-run: would install selective qdisc {config.name} on {host.name}")
            else:
                # Its diagnosis would only report the qdisc that setup first removes.
                print(f"dry-run: would replace selective qdisc {config.name} on {host.name}")
            continue
        if current is not None:
            remote.ssh_command(host, _selective_command(helper, "down", config))
        # up prints a full status report for manual use; setup prints its own line instead,
        # and errors still reach the terminal on stderr.
        remote.ssh_command(host, _selective_command(helper, "up", config),
                           stdout=subprocess.DEVNULL)
        print(f"installed selective qdisc {config.name} on {host.name}")


def remove_selective_qdiscs(inventory: remote.Inventory, tc: dict[str, str]) -> None:
    for shape in inventory.shaping:
        host = inventory.hosts[shape.host]
        helper = remote.setup_dir(host) + "/selective_qdisc.py"
        config = selective_config(shape, tc)
        if _qdisc_state(host, config.name) is None:
            continue
        copy_if_changed(host, SELECTIVE_HELPER, helper, "700")
        remote.ssh_command(host, _selective_command(helper, "down", config))
        print(f"removed selective qdisc {config.name} from {host.name}")


CLIENT_TARGETS_FILE = "targets/clients.json"
ROUTER_TARGETS_FILE = "targets/border_routers.json"


def client_metric_sources(
    inventory: remote.Inventory, clients: list[workload.Client],
) -> list[MetricSource]:
    """Every client metrics port, each with relay port base + i."""
    return [
        MetricSource(inventory.client_hosts[client.client_id], f"127.0.0.1:{client.metrics_port}",
                     inventory.local_port_base + index, {"client_id": client.client_id},
                     CLIENT_TARGETS_FILE)
        for index, client in enumerate(clients)
    ]


def router_metric_sources(inventory: remote.Inventory, first_port: int) -> list[MetricSource]:
    """Every router target, each with relay port first_port + i."""
    return [
        MetricSource(router.host, router.address, first_port + index,
                     router.labels if router.display
                     else {**router.labels, remote.DISPLAY_LABEL: "false"},
                     ROUTER_TARGETS_FILE)
        for index, router in enumerate(inventory.routers)
    ]


def metric_sources(
    inventory: remote.Inventory, clients: list[workload.Client],
) -> list[MetricSource]:
    """Every client metrics port, then every router target, each with relay port base + i."""
    return [
        *client_metric_sources(inventory, clients),
        *router_metric_sources(inventory, inventory.local_port_base + len(clients)),
    ]


def target_documents_for(
    inventory: remote.Inventory, sources: list[MetricSource],
    target_files: tuple[str, ...] = (CLIENT_TARGETS_FILE, ROUTER_TARGETS_FILE),
) -> dict[str, str]:
    """Prometheus file-SD targets grouped by target_file.

    A source on the Prometheus host is scraped directly, any other (and a controller-local one)
    through its relay on 127.0.0.1. target_files are always written, even without sources.
    """
    targets: dict[str, list[dict[str, object]]] = {name: [] for name in target_files}
    for source in sources:
        local = source.host is not None and source.host == inventory.prometheus_host
        targets.setdefault(source.target_file, []).append({
            "targets": [source.address if local else f"127.0.0.1:{source.port}"],
            "labels": source.labels,
        })
    return {name: json.dumps(entries, indent=2) + "\n" for name, entries in targets.items()}


def target_documents(
    inventory: remote.Inventory, clients: list[workload.Client],
) -> dict[str, str]:
    """Prometheus file-SD targets: a source on the Prometheus host directly, others via relays."""
    return target_documents_for(inventory, metric_sources(inventory, clients))


def write_local_targets(documents: dict[str, str]) -> None:
    workload.TARGET_DIR.mkdir(parents=True, exist_ok=True)
    for relative, content in documents.items():
        (workload.TARGET_DIR / Path(relative).name).write_text(content)


def _configuration_hash(documents: dict[str, str]) -> str:
    encoded = json.dumps(documents, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(encoded).hexdigest()


def _config_mounts(stack: MonitoringStack) -> str:
    """The compose volume entries that mount prometheus.yml and targets/ from stack.remote_dir.

    The long syntax with create_host_path false: with the short one, Docker creates a missing
    source as an empty root-owned directory (even for prometheus.yml) when it starts the
    container, for example at boot after the files were lost, and setup can then no longer
    write them. Without it, the start fails and the next setup rewrites the files.
    """
    return "\n".join(f"""      - type: bind
        source: {stack.remote_dir}/{source}
        target: /etc/prometheus/{source}
        read_only: true
        bind:
          create_host_path: false""" for source in ("prometheus.yml", "targets"))


def _compose_document(config_hash: str, stack: MonitoringStack | None = None) -> str:
    stack = _stack(stack)
    return f"""services:
  prometheus:
    image: {PROMETHEUS_IMAGE}
    container_name: {stack.prometheus_container}
    network_mode: host
    restart: unless-stopped
    command:
      - --config.file=/etc/prometheus/prometheus.yml
      - --storage.tsdb.retention.time=180d
      - --web.listen-address=127.0.0.1:{stack.remote_prometheus_port}
      # The hosts' Prometheus agents deliver their samples by remote write.
      - --web.enable-remote-write-receiver
    labels:
      {stack.config_hash_label}: "{config_hash}"
    volumes:
{_config_mounts(stack)}
      - prometheus-data:/prometheus

volumes:
  prometheus-data:
"""


def prometheus_documents(
    targets: dict[str, str], stack: MonitoringStack | None = None,
) -> tuple[dict[str, str], str]:
    stack = _stack(stack)
    documents = {**targets, "prometheus.yml": stack.prometheus_config.read_text()}
    config_hash = _configuration_hash(documents)
    documents["docker-compose.yml"] = _compose_document(config_hash, stack)
    return documents, config_hash


def _remote_documents_current(
    host: remote.SSHHost, documents: dict[str, str], remote_dir: str,
) -> bool:
    return _remote_test(host, " && ".join(
        remote.digest_matches(f"{remote_dir}/{relative}",
                              hashlib.sha256(content.encode()).hexdigest())
        for relative, content in sorted(documents.items())
    ))


def _remote_prometheus_files_current(
    host: remote.SSHHost, documents: dict[str, str], stack: MonitoringStack | None = None,
) -> bool:
    return _remote_documents_current(host, documents, _stack(stack).remote_dir)


def _upload_documents(host: remote.SSHHost, documents: dict[str, str], remote_dir: str) -> None:
    """Write each document to remote_dir/<relative> atomically (scp to .tmp, chmod, mv)."""
    with tempfile.TemporaryDirectory() as directory:
        staging = Path(directory)
        for relative, content in documents.items():
            local = staging / relative
            local.parent.mkdir(parents=True, exist_ok=True)
            local.write_text(content)
            target = f"{remote_dir}/{relative}"
            remote.scp_to(host, local, target + ".tmp")
            remote.ssh_command(host, " && ".join([
                f"chmod 644 {shlex.quote(target + '.tmp')}",
                f"mv -f {shlex.quote(target + '.tmp')} {shlex.quote(target)}",
            ]))


def _container_current(
    host: remote.SSHHost, docker: str, container: str, label: str, config_hash: str,
) -> bool:
    """Whether the container runs and carries config_hash in its label."""
    result = remote.ssh_command(
        host,
        f"{docker} inspect --format "
        + shlex.quote(f'{{{{.State.Running}}}} {{{{index .Config.Labels "{label}"}}}}')
        + " " + shlex.quote(container),
        check=False, capture_output=True,
    )
    return result.returncode == 0 and result.stdout.strip() == f"true {config_hash}"


def _owned_container_exists(
    host: remote.SSHHost, docker: str, container: str, expected: str,
) -> bool:
    """Whether the container exists and is the compose service expected (project/service).

    Raises if a container with that name belongs to anything else.
    """
    owner = remote.ssh_command(
        host,
        f"{docker} inspect --format "
        + shlex.quote('{{index .Config.Labels "com.docker.compose.project"}}/'
                      '{{index .Config.Labels "com.docker.compose.service"}}')
        + " " + shlex.quote(container),
        check=False, capture_output=True,
    )
    if owner.returncode:
        return False
    if owner.stdout.strip() != expected:
        raise remote.SSHError(
            f"refusing to stop {container} on {host.name}: "
            f"container belongs to {owner.stdout.strip()!r}, not {expected}",
        )
    return True


def _prometheus_container_current(
    host: remote.SSHHost, config_hash: str, stack: MonitoringStack | None = None,
) -> bool:
    stack = _stack(stack)
    return _container_current(host, "docker", stack.prometheus_container, stack.config_hash_label,
                              config_hash)


def _owned_prometheus_exists(host: remote.SSHHost, stack: MonitoringStack | None = None) -> bool:
    """Whether our Prometheus container exists; raise if a foreign one has its name."""
    stack = _stack(stack)
    return _owned_container_exists(host, "docker", stack.prometheus_container,
                                   f"{stack.prometheus_project}/prometheus")


def stop_prometheus(host: remote.SSHHost, stack: MonitoringStack | None = None) -> None:
    stack = _stack(stack)
    if _owned_prometheus_exists(host, stack):
        remote.ssh_command(host, f"docker rm -f {shlex.quote(stack.prometheus_container)}")


def ensure_prometheus(
    inventory: remote.Inventory, targets: dict[str, str], dry_run: bool = False,
    stack: MonitoringStack | None = None,
) -> None:
    given = {} if stack is None else {"stack": stack}
    stack = _stack(stack)
    host = inventory.hosts[inventory.prometheus_host]
    documents, config_hash = prometheus_documents(targets, stack)
    remote.ssh_command(host, " && ".join([
        "command -v sha256sum >/dev/null",
        "command -v docker >/dev/null",
        "docker compose version >/dev/null",
        # Fails early when the SSH user may not use the Docker daemon.
        "docker info >/dev/null",
        "true" if dry_run
        else f"install -d -m 755 {shlex.quote(stack.remote_dir + '/targets')}",
    ]))
    if (_remote_prometheus_files_current(host, documents, stack)
            and _prometheus_container_current(host, config_hash, stack)):
        print(f"Prometheus on {host.name} is current")
        return
    if dry_run:
        replacing = _owned_prometheus_exists(host, stack)
        print(f"dry-run: would {'replace' if replacing else 'create'} {stack.prometheus_container} "
              f"and its files in {stack.remote_dir} on {host.name}")
        return

    stop_prometheus(host, **given)
    _upload_documents(host, documents, stack.remote_dir)
    compose = f"{stack.remote_dir}/docker-compose.yml"
    remote.ssh_command(
        host,
        # --progress quiet hides the image pull and container progress; errors are still printed.
        f"docker compose --progress quiet --project-name {stack.prometheus_project} "
        f"-f {shlex.quote(compose)} "
        "up -d --force-recreate",
    )
    if not _prometheus_container_current(host, config_hash, stack):
        raise remote.SSHError(f"Prometheus container failed to start on {host.name}")
    print(f"started Prometheus on {host.name}")


@dataclass(frozen=True)
class AgentSpec:
    """A Prometheus agent: its host, the metric sources it scrapes there, its remote-write port.

    write_port is the loopback port, on the agent's host, of the -R tunnel to the controller's
    Prometheus forward. It is the relay port reserved for the host's first source, which needs
    no relay any more. Part of the reuse API (README.md, "Reuse from other tools").
    """
    host: str
    sources: tuple[MetricSource, ...]
    write_port: int


@dataclass(frozen=True)
class AgentDelivery:
    """What an agent reports about its remote write (prometheus_remote_storage_* metrics).

    Part of the reuse API (README.md, "Reuse from other tools").
    """
    # Timestamp of the newest sample scraped, and of the newest sample sent, in seconds.
    newest_scraped: float
    newest_sent: float
    # Samples dropped for good since the agent started: refused by the central Prometheus (a 4xx
    # answer) or cancelled in flight (prometheus_remote_storage_samples_failed_total).
    failed: float

    @property
    def backlog(self) -> float:
        """Seconds of scraped samples not yet sent; infinite before the first send."""
        return self.newest_scraped - self.newest_sent if self.newest_sent > 0 else float("inf")


def agent_specs_for(
    inventory: remote.Inventory, sources: list[MetricSource],
) -> list[AgentSpec]:
    """One agent per host with metric sources, except the Prometheus host and the controller.

    The agent's write_port is the relay port of the host's first source.
    Part of the reuse API (README.md, "Reuse from other tools").
    """
    by_host: dict[str, list[MetricSource]] = {}
    for source in sources:
        if source.host is not None and source.host != inventory.prometheus_host:
            by_host.setdefault(source.host, []).append(source)
    return [AgentSpec(host, tuple(found), found[0].port)
            for host, found in sorted(by_host.items())]


def central_sources(inventory: remote.Inventory, sources: list[MetricSource]) -> list[MetricSource]:
    """The sources the central Prometheus scrapes itself: on its host, or on the controller.

    Sources with host=None (the controller's) stay here and keep their relay.
    Part of the reuse API (README.md, "Reuse from other tools").
    """
    return [source for source in sources
            if source.host is None or source.host == inventory.prometheus_host]


def agent_tunnel_specs(
    inventory: remote.Inventory, agents: list[AgentSpec], forward_port: int,
) -> list[TunnelSpec]:
    """For each agent, an -R tunnel from its host's write_port to the controller forward_port.

    The controller forwards that port to the central Prometheus (prometheus_forward), so the
    agent's remote write reaches it through the controller. A TunnelSpec without source_alias is
    exactly that: only the -R half, on the host named by prometheus_alias.

    Part of the reuse API (README.md, "Reuse from other tools").
    """
    return [TunnelSpec(None, inventory.hosts[agent.host].alias, agent.write_port,
                       f"127.0.0.1:{forward_port}")
            for agent in agents]


def agent_container(stack: MonitoringStack | None = None) -> str:
    return f"{_stack(stack).prometheus_container}-agent"


def _agent_volume(stack: MonitoringStack) -> str:
    # docker compose names a project's volume <project>_<volume>.
    return f"{stack.prometheus_project}_agent-data"


def _file_sd_files(config: dict[str, object]) -> list[str]:
    """The file-SD files a Prometheus configuration reads, relative to /etc/prometheus/."""
    files = []
    for scrape in config.get("scrape_configs") or []:
        for discovery in (scrape or {}).get("file_sd_configs") or []:
            for path in (discovery or {}).get("files") or []:
                if isinstance(path, str) and path.startswith("/etc/prometheus/"):
                    files.append(path.removeprefix("/etc/prometheus/"))
    return files


def agent_config(write_port: int, stack: MonitoringStack | None = None) -> str:
    """The agent's configuration: the central one's global and scrape configs, plus remote write.

    The scrape configs (job names, file-SD files) are the central Prometheus's, so the agents'
    series are named and labeled as if the central Prometheus had scraped them.
    """
    central = yaml.safe_load(_stack(stack).prometheus_config.read_text()) or {}
    return yaml.safe_dump({
        "global": central.get("global") or {},
        "scrape_configs": central.get("scrape_configs") or [],
        "remote_write": [{
            "url": f"http://127.0.0.1:{write_port}/api/v1/write",
            "queue_config": {"min_shards": AGENT_SHARDS, "max_shards": AGENT_SHARDS},
        }],
    }, sort_keys=False)


def _agent_compose_document(config_hash: str, stack: MonitoringStack) -> str:
    return f"""services:
  {AGENT_SERVICE}:
    image: {PROMETHEUS_IMAGE}
    container_name: {agent_container(stack)}
    network_mode: host
    restart: unless-stopped
    command:
      - --agent
      - --config.file=/etc/prometheus/prometheus.yml
      - --storage.agent.path=/prometheus
      # Keep samples that could not be delivered until they are, up to the central retention.
      - --storage.agent.retention.max-time={AGENT_RETENTION}
      - --web.listen-address=127.0.0.1:{stack.remote_prometheus_port}
    labels:
      {stack.config_hash_label}: "{config_hash}"
    volumes:
{_config_mounts(stack)}
      - agent-data:/prometheus

volumes:
  agent-data:
"""


def agent_documents(
    agent: AgentSpec, stack: MonitoringStack | None = None,
) -> tuple[dict[str, str], str]:
    """The agent's files (file-SD targets, prometheus.yml, docker-compose.yml) and their hash.

    Every file-SD file of the central configuration is written, listing this host's sources
    at their own addresses; the agent scrapes them on its host.
    """
    stack = _stack(stack)
    central = yaml.safe_load(stack.prometheus_config.read_text()) or {}
    targets: dict[str, list[dict[str, object]]] = {
        name: [] for name in _file_sd_files(central)}
    for source in agent.sources:
        targets.setdefault(source.target_file, []).append(
            {"targets": [source.address], "labels": source.labels})
    documents = {name: json.dumps(entries, indent=2) + "\n" for name, entries in targets.items()}
    documents["prometheus.yml"] = agent_config(agent.write_port, stack)
    config_hash = _configuration_hash(documents)
    documents["docker-compose.yml"] = _agent_compose_document(config_hash, stack)
    return documents, config_hash


def _docker_command(host: remote.SSHHost) -> str:
    """How the SSH user runs Docker with compose on host: directly, or through sudo -n."""
    for docker in ("docker", "sudo -n docker"):
        if remote.ssh_command(
            host, f"{docker} info >/dev/null 2>&1 && {docker} compose version >/dev/null 2>&1",
            check=False,
        ).returncode == 0:
            return docker
    raise remote.SSHError(
        f"{host.name}: the SSH user can use Docker with compose neither directly nor with sudo -n")


_REMOTE_WRITE_METRICS = (
    "import sys, urllib.request\n"
    "text = urllib.request.urlopen(sys.argv[1], timeout=5).read().decode()\n"
    "lines = text.splitlines()\n"
    "print('\\n'.join(l for l in lines if l.startswith('prometheus_remote_storage_')))\n"
)


def agent_delivery(
    host: remote.SSHHost, stack: MonitoringStack | None = None,
) -> AgentDelivery | None:
    """The agent's remote-write state from its /metrics, or None if it does not answer.

    An agent that does not answer is never an error here. The agent listens on
    stack.remote_prometheus_port. Part of the reuse API (README.md, "Reuse from other tools").
    """
    stack = _stack(stack)
    url = f"http://127.0.0.1:{stack.remote_prometheus_port}/metrics"
    result = remote.ssh_command(
        host, f"python3 -c {shlex.quote(_REMOTE_WRITE_METRICS)} {shlex.quote(url)}",
        check=False, capture_output=True,
    )
    if result.returncode:
        return None
    newest_scraped, newest_sent, failed = 0.0, 0.0, 0.0
    for line in result.stdout.splitlines():
        name, _, value = line.rpartition(" ")
        try:
            number = float(value)
        except ValueError:
            continue
        metric = name.split("{", 1)[0]
        if metric == "prometheus_remote_storage_highest_timestamp_in_seconds":
            newest_scraped = max(newest_scraped, number)
        elif metric == "prometheus_remote_storage_queue_highest_sent_timestamp_seconds":
            newest_sent = max(newest_sent, number)
        elif metric == "prometheus_remote_storage_samples_failed_total":
            failed += number
    return AgentDelivery(newest_scraped, newest_sent, failed)


def _wait_agent_flushed(
    host: remote.SSHHost, seconds: int, stack: MonitoringStack | None = None,
) -> AgentDelivery | None:
    """Wait until the agent has sent what it scraped; return its last state (None: no answer)."""
    deadline = time.monotonic() + seconds
    while True:
        delivery = agent_delivery(host, stack)
        if delivery is None or delivery.backlog <= AGENT_MAX_BACKLOG_SECONDS \
                or time.monotonic() >= deadline:
            return delivery
        time.sleep(2)


def _flush_and_remove_agent(
    host: remote.SSHHost, docker: str, stack: MonitoringStack, remove_volume: bool,
) -> None:
    """Remove the agent container once it has sent its backlog.

    Remote write only sends what a running agent recorded after it started: samples still unsent
    when the agent stops are never sent. So this waits up to AGENT_FLUSH_WAIT_SECONDS for the
    backlog to drain, and reports what is lost if it does not.
    """
    delivery = _wait_agent_flushed(host, AGENT_FLUSH_WAIT_SECONDS, stack)
    if delivery is None:
        print(f"warning: the Prometheus agent on {host.name} does not answer; "
              "samples it had not sent are lost")
    elif delivery.backlog > AGENT_MAX_BACKLOG_SECONDS:
        print(f"warning: the Prometheus agent on {host.name} still had "
              f"{delivery.backlog:.0f} s of samples unsent after {AGENT_FLUSH_WAIT_SECONDS} s; "
              "they are lost")
    container = agent_container(stack)
    remote.ssh_command(host, f"{docker} rm -f {shlex.quote(container)} >/dev/null")
    if remove_volume:
        remote.ssh_command(host, f"{docker} volume rm -f {shlex.quote(_agent_volume(stack))} "
                                 ">/dev/null")


def ensure_agents(
    inventory: remote.Inventory, agents: list[AgentSpec], dry_run: bool = False,
    stack: MonitoringStack | None = None,
) -> None:
    """Run a Prometheus agent on each agent host, (re)created only when its files changed.

    Like the central Prometheus: files in stack.remote_dir, a compose project named after the
    stack, host networking, a config hash label. Docker is used directly or through sudo -n. A
    running agent is recreated only after it has sent its backlog (_flush_and_remove_agent).

    The container is agent_container(stack), named after stack.prometheus_container, and its
    scrape configs are those of stack.prometheus_config, job names and file-SD files unchanged.
    Part of the reuse API (README.md, "Reuse from other tools").
    """
    stack = _stack(stack)
    container = agent_container(stack)
    expected = f"{stack.prometheus_project}/{AGENT_SERVICE}"
    for agent in agents:
        host = inventory.hosts[agent.host]
        docker = _docker_command(host)
        remote.ssh_command(host, "command -v sha256sum >/dev/null && command -v python3 >/dev/null")
        documents, config_hash = agent_documents(agent, stack)
        if (_remote_documents_current(host, documents, stack.remote_dir)
                and _container_current(host, docker, container, stack.config_hash_label,
                                       config_hash)):
            print(f"Prometheus agent on {host.name} is current")
            continue
        exists = _owned_container_exists(host, docker, container, expected)
        if dry_run:
            print(f"dry-run: would {'replace' if exists else 'create'} {container} "
                  f"and its files in {stack.remote_dir} on {host.name}")
            continue
        if exists:
            _flush_and_remove_agent(host, docker, stack, remove_volume=False)
        remote.ssh_command(host, f"install -d -m 755 {shlex.quote(stack.remote_dir + '/targets')}")
        _upload_documents(host, documents, stack.remote_dir)
        remote.ssh_command(
            host,
            f"{docker} compose --progress quiet --project-name {stack.prometheus_project} "
            f"-f {shlex.quote(stack.remote_dir + '/docker-compose.yml')} up -d --force-recreate",
        )
        if not _container_current(host, docker, container, stack.config_hash_label, config_hash):
            raise remote.SSHError(f"Prometheus agent failed to start on {host.name}")
        print(f"started the Prometheus agent on {host.name}")


def check_agents_delivering(
    inventory: remote.Inventory, agents: list[AgentSpec], stack: MonitoringStack | None = None,
) -> None:
    """Wait until every agent has delivered recent samples and dropped none meanwhile.

    A dropped sample (failed) is gone for good. Drops while this check waits are an error: a
    persistent cause, usually a central Prometheus without the remote-write receiver or the
    out-of-order window, drops every batch. Earlier drops, counted since the agent started, are
    reported as a warning.

    Part of the reuse API (README.md, "Reuse from other tools").
    """
    problems = []
    for agent in agents:
        host = inventory.hosts[agent.host]
        before = agent_delivery(host, stack)
        delivery = _wait_agent_flushed(host, AGENT_DELIVERY_WAIT_SECONDS, stack)
        if delivery is None:
            problems.append(f"{host.name}: the Prometheus agent does not answer")
        elif before is not None and delivery.failed > before.failed:
            problems.append(f"{host.name}: the Prometheus agent dropped "
                            f"{delivery.failed - before.failed:.0f} samples; see its log")
        elif delivery.backlog > AGENT_MAX_BACKLOG_SECONDS:
            problems.append(f"{host.name}: the Prometheus agent has not delivered for "
                            f"{delivery.backlog:.0f} s" if delivery.newest_sent else
                            f"{host.name}: the Prometheus agent has delivered nothing yet")
        else:
            print(f"the Prometheus agent on {host.name} delivers "
                  f"(backlog {delivery.backlog:.1f} s)")
            if delivery.failed:
                print(f"warning: the Prometheus agent on {host.name} has dropped "
                      f"{delivery.failed:.0f} samples since it started; see its log")
    if problems:
        raise remote.SSHError("Prometheus agents are not delivering: " + "; ".join(problems))


def stop_agents(
    inventory: remote.Inventory, agents: list[AgentSpec], stack: MonitoringStack | None = None,
) -> None:
    """Remove each agent once it has sent its backlog, with its volume and files.

    Part of the reuse API (README.md, "Reuse from other tools").
    """
    stack = _stack(stack)
    container = agent_container(stack)
    expected = f"{stack.prometheus_project}/{AGENT_SERVICE}"
    for agent in agents:
        host = inventory.hosts[agent.host]
        docker = _docker_command(host)
        if _owned_container_exists(host, docker, container, expected):
            _flush_and_remove_agent(host, docker, stack, remove_volume=True)
            print(f"stopped the Prometheus agent on {host.name}")
        remote.ssh_command(host, f"rm -rf {shlex.quote(stack.remote_dir)}",
                           stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def tunnel_specs_for(
    inventory: remote.Inventory, sources: list[MetricSource],
) -> list[TunnelSpec]:
    """One relay per metric source that is not on the Prometheus host.

    Prometheus uses host networking, so it scrapes sources on its own host directly. A source
    without host (the controller's own) needs only the -R half of a relay.
    """
    prometheus_alias = inventory.hosts[inventory.prometheus_host].alias
    return [
        TunnelSpec(None if source.host is None else inventory.hosts[source.host].alias,
                   prometheus_alias, source.port, source.address)
        for source in sources
        if source.host != inventory.prometheus_host
    ]


def tunnel_specs(
    inventory: remote.Inventory, clients: list[workload.Client],
) -> list[TunnelSpec]:
    """The relays of the clients' and routers' metric sources."""
    return tunnel_specs_for(inventory, metric_sources(inventory, clients))


def docker_bridge_gateway() -> str | None:
    """The address that host.docker.internal (host-gateway) resolves to in local containers."""
    result = subprocess.run(
        ["docker", "network", "inspect", "bridge", "--format",
         "{{(index .IPAM.Config 0).Gateway}}"],
        check=False, capture_output=True, text=True,
    )
    if result.returncode:
        return None
    return result.stdout.strip() or None


def prometheus_forward(inventory: remote.Inventory) -> PrometheusForward:
    """Forward for the browser (127.0.0.1) and for local Grafana containers (bridge gateway).

    Part of the reuse API (README.md, "Reuse from other tools").
    """
    gateway = docker_bridge_gateway()
    return PrometheusForward(
        inventory.hosts[inventory.prometheus_host].alias, inventory.local_prometheus_port,
        ("127.0.0.1", *((gateway,) if gateway else ())),
    )


def _tunnel_hash(specs: list[TunnelSpec], forward: PrometheusForward | None) -> str:
    content = json.dumps({"relays": [asdict(spec) for spec in specs],
                          "prometheus": asdict(forward) if forward else None},
                         sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(content.encode()).hexdigest()


# An ssh-control-master is a background `ssh -M -S <socket> -f -N` connection that only carries
# port forwards. It is identified by its control socket and host alias: `ssh -S <socket> -O check`
# tells whether it is alive and `-O exit` closes it, so no PIDs need to be tracked.
ControlMaster = dict[str, str]


def _control_command(socket: str, operation: str, alias: str) -> list[str]:
    return ["ssh", *remote.SSH_OPTIONS, "-S", socket, "-O", operation, "--", alias]


def _start_ssh_control_master(arguments: list[str], socket: Path, alias: str) -> ControlMaster:
    """Start an ssh-control-master carrying the given -L or -R forward."""
    command = [
        "ssh", *remote.SSH_OPTIONS, "-M", "-S", str(socket), "-f", "-N",
        # Fail instead of running without the forward, e.g. when its port is taken.
        "-o", "ExitOnForwardFailure=yes",
        "-o", "ServerAliveInterval=15",
        "-o", "ServerAliveCountMax=3",
        *arguments, "--", alias,
    ]
    subprocess.run(command, check=True, text=True)
    return {"socket": str(socket), "alias": alias}


def _ssh_control_master_running(control_master: ControlMaster) -> bool:
    result = subprocess.run(
        _control_command(control_master["socket"], "check", control_master["alias"]),
        check=False, text=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def _stop_ssh_control_master(control_master: ControlMaster) -> bool:
    """Close an ssh-control-master; return whether it is gone."""
    result = subprocess.run(
        _control_command(control_master["socket"], "exit", control_master["alias"]),
        check=False, text=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def _load_tunnel_manifest(stack: MonitoringStack | None = None) -> dict[str, object] | None:
    try:
        value = json.loads(_stack(stack).manifest.read_text())
    except (FileNotFoundError, json.JSONDecodeError, OSError):
        return None
    return value if isinstance(value, dict) else None


def _manifest_ssh_control_masters(
    manifest: dict[str, object] | None,
) -> tuple[list[ControlMaster], bool]:
    """The well-formed ssh-control-masters of a manifest, and whether all recorded ones were."""
    recorded = manifest.get("ssh_control_masters", []) if manifest is not None else []
    if not isinstance(recorded, list):
        return [], False
    valid = [
        {"socket": entry["socket"], "alias": entry["alias"]} for entry in recorded
        if isinstance(entry, dict)
        and isinstance(entry.get("socket"), str) and isinstance(entry.get("alias"), str)
    ]
    return valid, len(valid) == len(recorded)


def stop_tunnels(stack: MonitoringStack | None = None) -> None:
    """Close every ssh-control-master in the manifest, newest first, and remove the state."""
    stack = _stack(stack)
    control_masters, _ = _manifest_ssh_control_masters(_load_tunnel_manifest(stack))
    failures = []
    for control_master in reversed(control_masters):
        if _ssh_control_master_running(control_master) and not _stop_ssh_control_master(
                control_master):
            failures.append(control_master["alias"])
            continue
        Path(control_master["socket"]).unlink(missing_ok=True)
    if failures:
        raise remote.SSHError("failed to stop ssh-control-masters for " + ", ".join(failures))
    stack.manifest.unlink(missing_ok=True)
    try:
        stack.tunnel_state_dir.rmdir()
    except OSError:
        pass


def _check_local_port_free(forward: PrometheusForward) -> None:
    for bind in forward.binds:
        with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as probe:
            try:
                probe.bind((bind, forward.port))
            except OSError as err:
                raise remote.SSHError(
                    f"controller port {bind}:{forward.port} for the Prometheus forward is in use "
                    f"({err.strerror}); set deployment.metrics.local_prometheus_port",
                ) from err


def _wait_local_port_free(forward: PrometheusForward, seconds: float) -> None:
    """_check_local_port_free, retried for up to seconds while the port is still taken."""
    deadline = time.monotonic() + seconds
    while True:
        try:
            _check_local_port_free(forward)
            return
        except remote.SSHError:
            if time.monotonic() >= deadline:
                raise
            time.sleep(0.5)


def ensure_tunnels(
    specs: list[TunnelSpec], forward: PrometheusForward | None = None, dry_run: bool = False,
    stack: MonitoringStack | None = None,
) -> None:
    """Relay every metric source to the Prometheus host through two ssh-control-masters.

    For relay port P, one ssh-control-master to the source host forwards controller 127.0.0.1:P
    to the source address (-L), and one to the Prometheus host forwards its 127.0.0.1:P back to
    controller 127.0.0.1:P (-R). Prometheus therefore scrapes its own loopback. A further
    ssh-control-master forwards the controller port of forward, on each of its bind addresses,
    to the remote Prometheus, for the browser and for Grafana. The manifest records a hash of
    the specs and the forward, and every ssh-control-master; if the hash matches and all of
    them answer -O check, nothing changes, otherwise all are replaced. A spec without
    source_alias has no -L half: the controller reaches its source directly.
    """
    stack = _stack(stack)
    manifest = _load_tunnel_manifest(stack)
    if not specs and forward is None:
        if dry_run:
            if manifest is not None:
                print("dry-run: would stop the existing Prometheus metric relays")
            return
        stop_tunnels(stack)
        print("no Prometheus metric relays are configured")
        return
    digest = _tunnel_hash(specs, forward)
    if manifest is not None and manifest.get("spec_sha256") == digest:
        control_masters, complete = _manifest_ssh_control_masters(manifest)
        if control_masters and complete and all(map(_ssh_control_master_running, control_masters)):
            print("Prometheus SSH tunnels are current")
            return
    forwarding = (f" and forward controller port {forward.port} to Prometheus"
                  if forward is not None else "")
    if dry_run:
        if forward is not None and manifest is None:
            # Only without our own relays could the port be taken by something else.
            _check_local_port_free(forward)
        print(f"dry-run: would {'replace the relays and ' if manifest is not None else ''}"
              f"start {len(specs)} Prometheus metric relays "
              # A spec without source_alias has only the -R half.
              f"({sum(2 if spec.source_alias else 1 for spec in specs)} ssh-control-masters)"
              f"{forwarding}")
        return

    stop_tunnels(stack)
    if forward is not None:
        # `ssh -O exit` returns before the old forward's process has released its port.
        _wait_local_port_free(forward, PORT_RELEASE_WAIT_SECONDS if manifest is not None else 0)
    stack.tunnel_state_dir.mkdir(mode=0o700, parents=True, exist_ok=True)
    control_masters: list[ControlMaster] = []
    try:
        for index, spec in enumerate(specs):
            if spec.source_alias is not None:
                control_masters.append(_start_ssh_control_master(
                    ["-L", f"127.0.0.1:{spec.port}:{spec.source_address}"],
                    stack.tunnel_state_dir / f"source-{index}.sock", spec.source_alias,
                ))
                remote_target = f"127.0.0.1:{spec.port}"
            else:
                remote_target = spec.source_address
            control_masters.append(_start_ssh_control_master(
                ["-R", f"127.0.0.1:{spec.port}:{remote_target}"],
                stack.tunnel_state_dir / f"prometheus-{index}.sock", spec.prometheus_alias,
            ))
        if forward is not None:
            ui_target = f"127.0.0.1:{stack.remote_prometheus_port}"
            control_masters.append(_start_ssh_control_master(
                [argument for bind in forward.binds
                 for argument in ("-L", f"{bind}:{forward.port}:{ui_target}")],
                stack.tunnel_state_dir / "prometheus-ui.sock", forward.alias,
            ))
    except BaseException:
        # Record what did start, so that stop_tunnels can close it.
        stack.manifest.write_text(json.dumps({"ssh_control_masters": control_masters}))
        stop_tunnels(stack)
        raise
    stack.manifest.write_text(json.dumps({
        "spec_sha256": digest,
        "ssh_control_masters": control_masters,
    }, indent=2) + "\n")
    stack.manifest.chmod(0o600)
    print(f"started {len(specs)} Prometheus metric relays"
          + (f" and forwarded controller port {forward.port} to Prometheus" if forward else ""))


def _http_answers(url: str) -> bool:
    try:
        urllib.request.urlopen(url, timeout=3)
    except urllib.error.HTTPError:
        return True
    except (urllib.error.URLError, OSError):
        return False
    return True


def _wait_http(url: str, seconds: int) -> bool:
    for _ in range(seconds):
        if _http_answers(url):
            return True
        time.sleep(1)
    return _http_answers(url)


def _grafana_state(stack: MonitoringStack | None = None) -> tuple[bool, str | None]:
    """Whether the local Grafana container runs, and the Prometheus port it is configured for."""
    result = subprocess.run(
        ["docker", "inspect", "--format",
         "{{.State.Running}}{{range .Config.Env}}\n{{.}}{{end}}",
         _stack(stack).grafana_container],
        check=False, capture_output=True, text=True,
    )
    if result.returncode:
        return False, None
    running, *environment = result.stdout.splitlines()
    port = next((line.split("=", 1)[1] for line in environment
                 if line.startswith("PROMETHEUS_PORT=")), None)
    return running == "true", port


def ensure_grafana(
    forward: PrometheusForward, dry_run: bool = False, stack: MonitoringStack | None = None,
) -> None:
    """Run the monitoring stack's Grafana on the controller, pointed at the Prometheus forward.

    It is the same container, provisioning, dashboards, and volume as in the Docker mode. Its
    data source is http://host.docker.internal:$PROMETHEUS_PORT, which the forward serves on the
    Docker bridge gateway. A running Grafana configured for that port is left alone; otherwise it
    is recreated with PROMETHEUS_PORT set to it. Prometheus itself is not started (--no-deps).
    """
    stack = _stack(stack)
    owner = workload.local_container_owner(stack.grafana_container)
    expected = f"{stack.grafana_project}/grafana"
    if owner is not None and owner != expected:
        raise remote.SSHError(
            f"refusing to replace {stack.grafana_container}: it belongs to {owner!r}, "
            f"not {expected}",
        )
    running, configured = _grafana_state(stack) if owner is not None else (False, None)
    port = str(forward.port)
    grafana_url = f"http://127.0.0.1:{os.environ.get('GRAFANA_PORT', '3000')}"
    if running and configured == port:
        print(f"Grafana {grafana_url} is running with Prometheus at controller port {port}")
    else:
        verb, change = (("re-point", f"from Prometheus port {configured} to {port}") if running
                        else ("start", f"{grafana_url} with Prometheus at controller port {port}"))
        if dry_run:
            print(f"dry-run: would {verb} Grafana {change}")
            return
        # Recreate rather than restart a stopped container: it may refer to a network that no
        # longer exists, e.g. after `docker network prune`. Its data is in a named volume.
        subprocess.run(
            workload.monitoring_compose(
                "up", "-d", "--no-deps", "--force-recreate", "grafana", quiet=True,
                compose=stack.grafana_compose, project=stack.grafana_project),
            env={**os.environ, "PROMETHEUS_PORT": port}, check=True, text=True)
        done = {"start": "started", "re-point": "re-pointed"}[verb]
        print(f"{done} Grafana {change}")
    if dry_run:
        return
    if not _wait_http(f"{grafana_url}/api/health", GRAFANA_WAIT_SECONDS):
        raise remote.SSHError(f"Grafana does not answer at {grafana_url}")
    # Grafana's data source host.docker.internal is the last bind address of the forward.
    data_source = f"http://{forward.binds[-1]}:{port}/-/ready"
    if not _wait_http(data_source, GRAFANA_WAIT_SECONDS):
        raise remote.SSHError(f"Grafana's data source {data_source} does not answer")


def setup(
    config_path: Path,
    config: tuple[
        workload.Endpoint, list[workload.Client], dict[str, int], dict[str, str],
    ] | None = None,
    inventory: remote.Inventory | None = None,
    dry_run: bool = False,
) -> int:
    """Bring the hosts to the configured state; the steps are listed in the module docstring.

    A real setup stops at the first failing step. With dry_run, every step performs its checks
    and reads but only reports what it would change; a failing step is reported and the next one
    still runs, so that one dry run shows every problem. It then returns 1.
    """
    workload.require_built_binary()
    server, clients, _, tc = config if config is not None else workload.load_config(config_path)
    if inventory is None:
        inventory = remote.load_inventory(config_path, server, clients)
    if dry_run:
        print("dry-run: checking only; nothing is changed on the hosts")
    local_jwt: Path | None = None
    manual: list[str] = []

    def binary() -> None:
        # No tester may run while setup changes the hosts, not even on hosts without one.
        for name in sorted(inventory.hosts):
            remote.stop_stray_testers(inventory.hosts[name], dry_run)
        for name in setup_hosts(inventory):
            host = inventory.hosts[name]
            remote.preflight(host, dry_run)
            deploy_binary(host, dry_run)

    def marketplace_jwt() -> None:
        # Logging in only reads the marketplace, so a dry run does it too.
        nonlocal local_jwt
        if remote.hummingbird_hosts(inventory, clients):
            local_jwt = workload.write_private_jwt(obtain_marketplace_jwt(inventory))
            deploy_jwt(inventory, clients, local_jwt, dry_run)

    sources = metric_sources(inventory, clients)
    agents = agent_specs_for(inventory, sources)
    # The central Prometheus scrapes what is on its host (and relayed controller sources); the
    # agents scrape everything else and remote-write it.
    central = central_sources(inventory, sources)

    def prometheus() -> None:
        targets = target_documents_for(inventory, central)
        if not dry_run:
            write_local_targets(targets)
        ensure_prometheus(inventory, targets, dry_run)

    def tunnels() -> None:
        forward = prometheus_forward(inventory)
        ensure_tunnels([*tunnel_specs_for(inventory, central),
                        *agent_tunnel_specs(inventory, agents, forward.port)], forward, dry_run)
        if dry_run:
            if agents:
                print(f"dry-run: would check that {len(agents)} Prometheus agents deliver")
            return
        check_agents_delivering(inventory, agents)

    steps = [
        ("1. clocks", lambda: remote.check_clocks(
            [inventory.hosts[name] for name in sorted(inventory.hosts)])),
        ("2. binary", binary),
        ("3. marketplace service", lambda: ensure_marketplace(inventory, dry_run)),
        ("4. marketplace JWT", marketplace_jwt),
        ("5. selective qdiscs", lambda: ensure_selective_qdiscs(inventory, tc, dry_run)),
        ("6. static info Note",
         lambda: manual.extend(ensure_marketplace_notes(inventory, dry_run))),
        ("7. Prometheus", prometheus),
        ("8. Prometheus agents", lambda: ensure_agents(inventory, agents, dry_run)),
        ("9. metric tunnels", tunnels),
        ("10. Grafana", lambda: ensure_grafana(prometheus_forward(inventory), dry_run)),
    ]
    failures: list[str] = []
    completed = False
    try:
        for label, step in steps:
            if not dry_run:
                step()
                continue
            try:
                step()
            except (ValueError, RuntimeError, OSError, subprocess.SubprocessError) as err:
                failures.append(f"{label}: {err}")
                print(f"dry-run: step {label} FAILED: {err}")
        completed = True
    finally:
        if local_jwt is not None:
            local_jwt.unlink(missing_ok=True)
        # A later failure must not hide the steps that files already changed on the hosts need.
        if completed or manual:
            # 11. Manual steps.
            print_manual_steps("setup", manual, dry_run)
    if failures:
        print(f"dry-run: setup would fail; {len(failures)} step(s) failed:")
        for failure in failures:
            print(f"  - {failure}")
    if dry_run:
        print(workload.DRY_RUN_NOTICE)
    return 1 if failures else 0


def teardown(
    config_path: Path,
    config: tuple[
        workload.Endpoint, list[workload.Client], dict[str, int], dict[str, str],
    ] | None = None,
    inventory: remote.Inventory | None = None,
) -> int:
    """Remove what setup installed, except the static info Note; continue past failures.

    The marketplace service is stopped, but its binary, unit, config, and database stay.
    """
    server, clients, _, tc = config if config is not None else workload.load_config(config_path)
    if inventory is None:
        inventory = remote.load_inventory(config_path, server, clients)
    errors = []
    agents = agent_specs_for(inventory, metric_sources(inventory, clients))
    # The static info Note that setup advertised is deliberately kept. The agents go before the
    # tunnels, so that they can still send their backlog to the central Prometheus.
    actions = [
        lambda: stop_marketplace(inventory),
        lambda: remove_selective_qdiscs(inventory, tc),
        lambda: stop_agents(inventory, agents),
        stop_tunnels,
        lambda: stop_prometheus(inventory.hosts[inventory.prometheus_host]),
    ]
    for action in actions:
        try:
            action()
        except (OSError, RuntimeError, subprocess.SubprocessError) as err:
            errors.append(str(err))

    for name in setup_hosts(inventory):
        host = inventory.hosts[name]
        try:
            remote.ssh_command(
                host, f"rm -rf {shlex.quote(remote.setup_dir(host))}",
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )
        except subprocess.SubprocessError as err:
            errors.append(str(err))
    prometheus = inventory.hosts[inventory.prometheus_host]
    try:
        remote.ssh_command(
            prometheus, f"rm -rf {shlex.quote(REMOTE_PROMETHEUS_DIR)}",
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        )
    except subprocess.SubprocessError as err:
        errors.append(str(err))
    if errors:
        raise remote.SSHError("SSH teardown encountered errors: " + "; ".join(errors))
    return 0
