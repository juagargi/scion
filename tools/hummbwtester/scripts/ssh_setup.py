#!/usr/bin/env python3
"""Install and remove persistent infrastructure for SSH hummbwtester experiments.

Everything runs on the controller, the machine where experiment.py is started. Remote work is
done through the configured SSH aliases: commands go through ssh_orchestration.ssh_command
(`ssh -- <alias> sh -c '<script>'`) and files through scp. Every step compares the current state
with the desired one first, so a repeated setup only changes what differs.

setup performs, in this order:

1. Binary: on the server, client and shaping hosts, preflight checks (tools, run_dir, SCION
   daemon, readiness_command), then copy bin/hummbwtester to <run_dir>/setup/ unless its SHA-256
   already matches. Copies are atomic: scp to a .tmp file, verify its digest, chmod, mv.
2. Marketplace service: on hummingbird.marketplace.host, install bin/marketplace as
   /usr/local/bin/hummingbird-marketplace and the hummingbird-marketplace.service unit with sudo,
   and its config (/etc/scion/marketplace/) and database directory (/var/lib/scion/marketplace/)
   for the SSH user, which the service runs as. Each is replaced only when it differs. The
   service is never enabled and does not restart by itself. It is (re)started, with a database
   rebuilt from the Docker topology's default entries, only when something was replaced, it is
   not running, or its APIs are unreachable; setup then waits until both APIs answer.
3. Marketplace JWT: open a temporary ssh-control-master that forwards a free controller loopback
   port to hummingbird.marketplace.url as seen from hummingbird.marketplace.host, log in through
   it with marketplace/tools/get_jwt.py, close it, and copy the JWT (mode 600) to the hosts of the
   Hummingbird clients. The controller's copy is deleted.
4. Selective qdiscs: on each shaping host, copy selective_qdisc.py to <run_dir>/setup/ and run it
   with sudo -n, unless its recorded state and the live root qdisc already match.
5. Static info Note: on the server, client and marketplace hosts, pipe static_info_note.py to
   `sudo -n python3 -` to insert or update the hummbwtester entry of the Note's hummingbird list.
   Control services are never restarted; a changed file becomes a manual step.
6. Prometheus: write the file-SD targets locally and to /tmp/hummbwtester/prometheus/ on the
   Prometheus host, and (re)create the hummbwtester-prometheus container (host networking,
   listening on 127.0.0.1:8090 only, data in the prometheus-data volume) when files or its
   configuration hash differ.
7. Metric relays: Prometheus scrapes sources on its own host directly. Every other source gets
   two ssh-control-masters, an -L to the source host and an -R to the Prometheus host, so that
   Prometheus scrapes its own loopback. One more ssh-control-master forwards controller port
   deployment.metrics.local_prometheus_port (default 8090), on 127.0.0.1 and on the Docker
   bridge gateway, to the remote Prometheus. Their sockets and a manifest live in
   /tmp/hummbwtester/ssh-tunnels/ on the controller.
8. Grafana: run the monitoring stack's hummbwtester-grafana container on the controller, with the
   Docker mode's provisioning, dashboards, and volume, and PROMETHEUS_PORT set to that forward;
   its data source host.docker.internal resolves to the bridge gateway. A running Grafana with
   that setting is kept. Then check that Grafana and its data source answer.
9. Print the manual steps, e.g. restarting a control service whose static info file changed.

teardown stops the marketplace service and waits until it is unreachable, removes the qdiscs,
closes the ssh-control-masters of the manifest (relays and Prometheus forward; Grafana keeps
running, as in the Docker mode), removes the Prometheus container and files, and
deletes <run_dir>/setup/ (binary, JWT, helpers). It keeps the static info Note and the
marketplace's binary, unit, config, and database.

With dry_run (`experiment.py setup --dry-run`), every step runs its checks and reads and prints
what it would change ("dry-run: would ..."), but nothing is written on the hosts, no service or
container is started, stopped, or restarted, and no relay is started or stopped. The qdisc and
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
from dataclasses import asdict, dataclass, replace
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
# The SCION configuration of every host: topology.json, certs/, and keys/master0.key.
SCION_CONFIG_DIR = "/etc/scion"
# How long setup waits for a started marketplace, and teardown for a stopped one.
MARKETPLACE_WAIT_SECONDS = 30
STATIC_INFO_HELPER = workload.ROOT / "tools" / "hummbwtester" / "scripts" / "static_info_note.py"
# The Note entry name that marks the marketplace advertisement owned by these experiments.
NOTE_NAME = "hummbwtester"
NOTE_PROTOCOL = "connectrpc/TLS/QUIC/SCION"
PROMETHEUS_CONFIG = workload.ROOT / "tools" / "hummbwtester" / "monitoring" / "prometheus.yml"
REMOTE_PROMETHEUS_DIR = "/tmp/hummbwtester/prometheus"
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


@dataclass(frozen=True)
class MetricSource:
    """A metrics endpoint: address on its host, and the relay port reserved for it."""
    host: str
    address: str
    port: int
    labels: dict[str, str]
    router: bool


@dataclass(frozen=True)
class PrometheusForward:
    """Controller port, bound on each address of binds, forwarded to the remote Prometheus."""
    alias: str
    port: int
    binds: tuple[str, ...]


@dataclass(frozen=True)
class TunnelSpec:
    source_alias: str
    prometheus_alias: str
    port: int
    source_address: str


def setup_hosts(inventory: remote.Inventory) -> list[str]:
    """The hosts that receive the binary and helpers: participants and shaping hosts."""
    return sorted({
        inventory.server_host,
        *inventory.client_hosts.values(),
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
            f"hummingbird.marketplace.url has an invalid port: {url}",
        ) from err
    if not parts.hostname:
        raise workload.ConfigError(f"hummingbird.marketplace.url has no host: {url}")
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
    """Log in at the marketplace url as seen from its host, through a temporary tunnel."""
    marketplace = inventory.marketplace
    host = inventory.hosts[inventory.marketplace_host]
    if not os.environ.get(marketplace.password_env):
        # Checked before the tunnel is opened, which would be pointless without a password.
        raise workload.ConfigError(
            f"marketplace password environment variable {marketplace.password_env} is not set")
    with marketplace_tunnel(host, marketplace.url) as local_url:
        try:
            return workload.obtain_marketplace_jwt(replace(marketplace, url=local_url))
        except workload.ConfigError as err:
            raise workload.ConfigError(
                f"{err} (tunneled to {marketplace.url} on {host.name})",
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


def _url_endpoint(url: str) -> tuple[str, int]:
    parts = urllib.parse.urlsplit(url)
    return parts.hostname or "", parts.port or (443 if parts.scheme == "https" else 80)


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
        api_addr=_host_port(*_url_endpoint(marketplace.url)),
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


def marketplace_entries(inventory: remote.Inventory) -> dict[str, object]:
    """The Docker topology's default marketplace entries for the ASes advertising the marketplace.

    Users, accounts, assets for every interface pair, and redemption delegations are exactly the
    ones a generated Docker topology starts with. Only topology.json and the derived secret value
    of every AS are read from the hosts.
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
        return topology_marketplace.defaultEntries(directory, secret_values=secret_values)


def _reconfigure_marketplace_db(host: remote.SSHHost, entries: dict[str, object]) -> None:
    """Replace the database of the stopped marketplace with a freshly built one."""
    with tempfile.TemporaryDirectory(prefix="hummbwtester-market-db-") as directory:
        database = Path(directory) / "marketplace.db"
        _topology_marketplace().populateDB(str(database), str(MARKETPLACE_SCHEMA), entries)
        # Journal files of the old database would be applied to the new one.
        remote.ssh_command(host, "rm -f " + " ".join(
            shlex.quote(REMOTE_MARKETPLACE_DB + suffix)
            for suffix in ("", "-wal", "-shm", "-journal")
        ))
        copy_if_changed(host, database, REMOTE_MARKETPLACE_DB, "600")


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
              f"{len(entries['ases'])} ASes) and wait until it is reachable")
        return
    if active:
        _systemctl(host, "stop")
    _reconfigure_marketplace_db(host, entries)
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
    return sorted({
        inventory.server_host, *inventory.client_hosts.values(), inventory.marketplace_host,
    })


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
    """Advertise the marketplace and return the manual steps the changed files need."""
    entry = note_entry(inventory.marketplace)
    if entry is None:
        print("hummingbird.marketplace.scion_address is not set; static info Notes are unchanged")
        return []
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


def metric_sources(
    inventory: remote.Inventory, clients: list[workload.Client],
) -> list[MetricSource]:
    """Every client metrics port, then every router target, each with relay port base + i."""
    sources = [
        MetricSource(inventory.client_hosts[client.client_id], f"127.0.0.1:{client.metrics_port}",
                     inventory.local_port_base + index, {"client_id": client.client_id}, False)
        for index, client in enumerate(clients)
    ]
    sources.extend(
        MetricSource(router.host, router.address, inventory.local_port_base + index,
                     router.labels, True)
        for index, router in enumerate(inventory.routers, start=len(clients))
    )
    return sources


def target_documents(
    inventory: remote.Inventory, clients: list[workload.Client],
) -> dict[str, str]:
    """Prometheus file-SD targets: a source on the Prometheus host directly, others via relays."""
    targets: dict[bool, list[dict[str, object]]] = {False: [], True: []}
    for source in metric_sources(inventory, clients):
        local = source.host == inventory.prometheus_host
        targets[source.router].append({
            "targets": [source.address if local else f"127.0.0.1:{source.port}"],
            "labels": source.labels,
        })
    return {
        "targets/clients.json": json.dumps(targets[False], indent=2) + "\n",
        "targets/border_routers.json": json.dumps(targets[True], indent=2) + "\n",
    }


def write_local_targets(documents: dict[str, str]) -> None:
    workload.TARGET_DIR.mkdir(parents=True, exist_ok=True)
    for relative, content in documents.items():
        (workload.TARGET_DIR / Path(relative).name).write_text(content)


def _configuration_hash(documents: dict[str, str]) -> str:
    encoded = json.dumps(documents, sort_keys=True, separators=(",", ":")).encode()
    return hashlib.sha256(encoded).hexdigest()


def _compose_document(config_hash: str) -> str:
    return f"""services:
  prometheus:
    image: {PROMETHEUS_IMAGE}
    container_name: {PROMETHEUS_CONTAINER}
    network_mode: host
    restart: unless-stopped
    command:
      - --config.file=/etc/prometheus/prometheus.yml
      - --storage.tsdb.retention.time=7d
      - --web.listen-address=127.0.0.1:{PROMETHEUS_PORT}
    labels:
      {CONFIG_HASH_LABEL}: "{config_hash}"
    volumes:
      - {REMOTE_PROMETHEUS_DIR}/prometheus.yml:/etc/prometheus/prometheus.yml:ro
      - {REMOTE_PROMETHEUS_DIR}/targets:/etc/prometheus/targets:ro
      - prometheus-data:/prometheus

volumes:
  prometheus-data:
"""


def prometheus_documents(targets: dict[str, str]) -> tuple[dict[str, str], str]:
    documents = {**targets, "prometheus.yml": PROMETHEUS_CONFIG.read_text()}
    config_hash = _configuration_hash(documents)
    documents["docker-compose.yml"] = _compose_document(config_hash)
    return documents, config_hash


def _remote_prometheus_files_current(
    host: remote.SSHHost, documents: dict[str, str],
) -> bool:
    return _remote_test(host, " && ".join(
        remote.digest_matches(f"{REMOTE_PROMETHEUS_DIR}/{relative}",
                              hashlib.sha256(content.encode()).hexdigest())
        for relative, content in sorted(documents.items())
    ))


def _prometheus_container_current(host: remote.SSHHost, config_hash: str) -> bool:
    result = remote.ssh_command(
        host,
        "docker inspect --format "
        + shlex.quote(f'{{{{.State.Running}}}} {{{{index .Config.Labels "{CONFIG_HASH_LABEL}"}}}}')
        + " " + shlex.quote(PROMETHEUS_CONTAINER),
        check=False, capture_output=True,
    )
    return result.returncode == 0 and result.stdout.strip() == f"true {config_hash}"


def _owned_prometheus_exists(host: remote.SSHHost) -> bool:
    """Whether our Prometheus container exists; raise if a foreign one has its name."""
    owner = remote.ssh_command(
        host,
        "docker inspect --format "
        + shlex.quote('{{index .Config.Labels "com.docker.compose.project"}}/'
                      '{{index .Config.Labels "com.docker.compose.service"}}')
        + " " + shlex.quote(PROMETHEUS_CONTAINER),
        check=False, capture_output=True,
    )
    if owner.returncode:
        return False
    expected = f"{PROMETHEUS_PROJECT}/prometheus"
    if owner.stdout.strip() != expected:
        raise remote.SSHError(
            f"refusing to stop {PROMETHEUS_CONTAINER} on {host.name}: "
            f"container belongs to {owner.stdout.strip()!r}, not {expected}",
        )
    return True


def stop_prometheus(host: remote.SSHHost) -> None:
    if _owned_prometheus_exists(host):
        remote.ssh_command(host, f"docker rm -f {shlex.quote(PROMETHEUS_CONTAINER)}")


def ensure_prometheus(
    inventory: remote.Inventory, targets: dict[str, str], dry_run: bool = False,
) -> None:
    host = inventory.hosts[inventory.prometheus_host]
    documents, config_hash = prometheus_documents(targets)
    remote.ssh_command(host, " && ".join([
        "command -v sha256sum >/dev/null",
        "command -v docker >/dev/null",
        "docker compose version >/dev/null",
        # Fails early when the SSH user may not use the Docker daemon.
        "docker info >/dev/null",
        "true" if dry_run
        else f"install -d -m 755 {shlex.quote(REMOTE_PROMETHEUS_DIR + '/targets')}",
    ]))
    if (_remote_prometheus_files_current(host, documents)
            and _prometheus_container_current(host, config_hash)):
        print(f"Prometheus on {host.name} is current")
        return
    if dry_run:
        replacing = _owned_prometheus_exists(host)
        print(f"dry-run: would {'replace' if replacing else 'create'} {PROMETHEUS_CONTAINER} "
              f"and its files in {REMOTE_PROMETHEUS_DIR} on {host.name}")
        return

    stop_prometheus(host)
    with tempfile.TemporaryDirectory() as directory:
        staging = Path(directory)
        for relative, content in documents.items():
            local = staging / relative
            local.parent.mkdir(parents=True, exist_ok=True)
            local.write_text(content)
            target = f"{REMOTE_PROMETHEUS_DIR}/{relative}"
            remote.scp_to(host, local, target + ".tmp")
            remote.ssh_command(host, " && ".join([
                f"chmod 644 {shlex.quote(target + '.tmp')}",
                f"mv -f {shlex.quote(target + '.tmp')} {shlex.quote(target)}",
            ]))
    compose = f"{REMOTE_PROMETHEUS_DIR}/docker-compose.yml"
    remote.ssh_command(
        host,
        # --progress quiet hides the image pull and container progress; errors are still printed.
        f"docker compose --progress quiet --project-name {PROMETHEUS_PROJECT} "
        f"-f {shlex.quote(compose)} "
        "up -d --force-recreate",
    )
    if not _prometheus_container_current(host, config_hash):
        raise remote.SSHError(f"Prometheus container failed to start on {host.name}")
    print(f"started Prometheus on {host.name}")


def tunnel_specs(
    inventory: remote.Inventory, clients: list[workload.Client],
) -> list[TunnelSpec]:
    """One relay per metric source that is not on the Prometheus host.

    Prometheus uses host networking, so it scrapes sources on its own host directly.
    """
    prometheus_alias = inventory.hosts[inventory.prometheus_host].alias
    return [
        TunnelSpec(inventory.hosts[source.host].alias, prometheus_alias, source.port,
                   source.address)
        for source in metric_sources(inventory, clients)
        if source.host != inventory.prometheus_host
    ]


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
    """Forward for the browser (127.0.0.1) and for local Grafana containers (bridge gateway)."""
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


def _load_tunnel_manifest() -> dict[str, object] | None:
    try:
        value = json.loads(TUNNEL_MANIFEST.read_text())
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


def stop_tunnels() -> None:
    """Close every ssh-control-master in the manifest, newest first, and remove the state."""
    control_masters, _ = _manifest_ssh_control_masters(_load_tunnel_manifest())
    failures = []
    for control_master in reversed(control_masters):
        if _ssh_control_master_running(control_master) and not _stop_ssh_control_master(
                control_master):
            failures.append(control_master["alias"])
            continue
        Path(control_master["socket"]).unlink(missing_ok=True)
    if failures:
        raise remote.SSHError("failed to stop ssh-control-masters for " + ", ".join(failures))
    TUNNEL_MANIFEST.unlink(missing_ok=True)
    try:
        TUNNEL_STATE_DIR.rmdir()
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


def ensure_tunnels(
    specs: list[TunnelSpec], forward: PrometheusForward | None = None, dry_run: bool = False,
) -> None:
    """Relay every metric source to the Prometheus host through two ssh-control-masters.

    For relay port P, one ssh-control-master to the source host forwards controller 127.0.0.1:P
    to the source address (-L), and one to the Prometheus host forwards its 127.0.0.1:P back to
    controller 127.0.0.1:P (-R). Prometheus therefore scrapes its own loopback. A further
    ssh-control-master forwards the controller port of forward, on each of its bind addresses,
    to the remote Prometheus, for the browser and for Grafana. The manifest records a hash of
    the specs and the forward, and every ssh-control-master; if the hash matches and all of
    them answer -O check, nothing changes, otherwise all are replaced.
    """
    manifest = _load_tunnel_manifest()
    if not specs and forward is None:
        if dry_run:
            if manifest is not None:
                print("dry-run: would stop the existing Prometheus metric relays")
            return
        stop_tunnels()
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
              f"start {len(specs)} Prometheus metric relays ({2 * len(specs)} ssh-control-masters)"
              f"{forwarding}")
        return

    stop_tunnels()
    if forward is not None:
        _check_local_port_free(forward)
    TUNNEL_STATE_DIR.mkdir(mode=0o700, parents=True, exist_ok=True)
    control_masters: list[ControlMaster] = []
    try:
        for index, spec in enumerate(specs):
            control_masters.append(_start_ssh_control_master(
                ["-L", f"127.0.0.1:{spec.port}:{spec.source_address}"],
                TUNNEL_STATE_DIR / f"source-{index}.sock", spec.source_alias,
            ))
            control_masters.append(_start_ssh_control_master(
                ["-R", f"127.0.0.1:{spec.port}:127.0.0.1:{spec.port}"],
                TUNNEL_STATE_DIR / f"prometheus-{index}.sock", spec.prometheus_alias,
            ))
        if forward is not None:
            control_masters.append(_start_ssh_control_master(
                [argument for bind in forward.binds
                 for argument in ("-L", f"{bind}:{forward.port}:127.0.0.1:{PROMETHEUS_PORT}")],
                TUNNEL_STATE_DIR / "prometheus-ui.sock", forward.alias,
            ))
    except BaseException:
        # Record what did start, so that stop_tunnels can close it.
        TUNNEL_MANIFEST.write_text(json.dumps({"ssh_control_masters": control_masters}))
        stop_tunnels()
        raise
    TUNNEL_MANIFEST.write_text(json.dumps({
        "spec_sha256": digest,
        "ssh_control_masters": control_masters,
    }, indent=2) + "\n")
    TUNNEL_MANIFEST.chmod(0o600)
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


def _grafana_state() -> tuple[bool, str | None]:
    """Whether the local Grafana container runs, and the Prometheus port it is configured for."""
    result = subprocess.run(
        ["docker", "inspect", "--format",
         "{{.State.Running}}{{range .Config.Env}}\n{{.}}{{end}}", GRAFANA_CONTAINER],
        check=False, capture_output=True, text=True,
    )
    if result.returncode:
        return False, None
    running, *environment = result.stdout.splitlines()
    port = next((line.split("=", 1)[1] for line in environment
                 if line.startswith("PROMETHEUS_PORT=")), None)
    return running == "true", port


def ensure_grafana(forward: PrometheusForward, dry_run: bool = False) -> None:
    """Run the monitoring stack's Grafana on the controller, pointed at the Prometheus forward.

    It is the same container, provisioning, dashboards, and volume as in the Docker mode. Its
    data source is http://host.docker.internal:$PROMETHEUS_PORT, which the forward serves on the
    Docker bridge gateway. A running Grafana configured for that port is left alone; otherwise it
    is (re)created with PROMETHEUS_PORT set to it. Prometheus itself is not started (--no-deps).
    """
    owner = workload.local_container_owner(GRAFANA_CONTAINER)
    expected = f"{workload.MONITORING_PROJECT}/grafana"
    if owner is not None and owner != expected:
        raise remote.SSHError(
            f"refusing to replace {GRAFANA_CONTAINER}: it belongs to {owner!r}, not {expected}",
        )
    running, configured = _grafana_state() if owner is not None else (False, None)
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
        subprocess.run(workload.monitoring_compose("up", "-d", "--no-deps", "grafana", quiet=True),
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

    def prometheus() -> None:
        targets = target_documents(inventory, clients)
        if not dry_run:
            write_local_targets(targets)
        ensure_prometheus(inventory, targets, dry_run)

    steps = [
        ("1. binary", binary),
        ("2. marketplace service", lambda: ensure_marketplace(inventory, dry_run)),
        ("3. marketplace JWT", marketplace_jwt),
        ("4. selective qdiscs", lambda: ensure_selective_qdiscs(inventory, tc, dry_run)),
        ("5. static info Note",
         lambda: manual.extend(ensure_marketplace_notes(inventory, dry_run))),
        ("6. Prometheus", prometheus),
        ("7. metric relays", lambda: ensure_tunnels(
            tunnel_specs(inventory, clients), prometheus_forward(inventory), dry_run)),
        ("8. Grafana", lambda: ensure_grafana(prometheus_forward(inventory), dry_run)),
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
            # 9. Manual steps.
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
    # The static info Note that setup advertised is deliberately kept.
    actions = [
        lambda: stop_marketplace(inventory),
        lambda: remove_selective_qdiscs(inventory, tc),
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
