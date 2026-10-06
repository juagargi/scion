#!/usr/bin/env python3
"""Run hummbwtester on explicitly inventoried SSH-accessible SCION hosts.

This module deliberately knows nothing about generated Docker topology. The SSH deployment and
workload are both read from one experiment configuration.
"""

from __future__ import annotations

from dataclasses import dataclass
import hashlib
from pathlib import Path
import shlex
import signal
import subprocess
import time
import uuid
from typing import Any

try:  # Support both ``python -m``/tests and the small direct entrypoint script.
    from . import orchestration as workload
except ImportError:
    import orchestration as workload


ROOT = workload.ROOT
BIN = workload.BIN
SETUP_DIR_NAME = "setup"
RUNS_DIR_NAME = "runs"


@dataclass(frozen=True)
class SSHHost:
    name: str
    alias: str
    sciond: str
    run_dir: str
    readiness_command: str | None


@dataclass(frozen=True)
class RouterMetrics:
    host: str
    address: str
    labels: dict[str, str]


@dataclass(frozen=True)
class ShapedFlow:
    host: str
    name: str
    device: str
    local: str
    remote: str
    expected_root: str


@dataclass(frozen=True)
class Inventory:
    hosts: dict[str, SSHHost]
    server_host: str
    client_hosts: dict[str, str]
    prometheus_host: str
    local_port_base: int
    routers: tuple[RouterMetrics, ...]
    shaping: tuple[ShapedFlow, ...]
    # Host through which setup tunnels to the marketplace; None means reach its url directly.
    marketplace_host: str | None = None


class SSHError(RuntimeError):
    pass


def _object(value: Any, context: str) -> dict[str, Any]:
    if not isinstance(value, dict):
        raise workload.ConfigError(f"{context} must be an object")
    return value


def _fields(value: dict[str, Any], required: set[str], context: str, optional: set[str] = set()) -> None:
    workload.require_fields(value, required, context, optional)


def _name(value: Any, context: str) -> str:
    if not isinstance(value, str) or not value or any(char.isspace() for char in value):
        raise workload.ConfigError(f"{context} must be a non-empty string without whitespace")
    return value


def _host_port(value: str, context: str) -> tuple[str, str]:
    """Parse the host:port form used for the daemon and tunneled TCP endpoints."""
    host, separator, port = value.rpartition(":")
    if not separator or not host or not port.isdecimal() or not 1 <= int(port) <= 65535:
        raise workload.ConfigError(f"{context} must be a host:port endpoint")
    return host.strip("[]"), port


def load_inventory(path: Path, server: workload.Endpoint, clients: list[workload.Client]) -> Inventory:
    """Read SSH deployment details and inline participant placements from one experiment file."""
    root = workload.read_json(path)
    deployment = root.get("deployment")
    if not isinstance(deployment, dict) or deployment.get("kind") != "ssh":
        raise workload.ConfigError("deployment.kind must be ssh")
    _fields(deployment, {"kind", "hosts", "metrics", "shaping"}, "deployment")
    raw_hosts = _object(deployment["hosts"], "deployment.hosts")
    if not raw_hosts:
        raise workload.ConfigError("ssh inventory.hosts must not be empty")
    hosts: dict[str, SSHHost] = {}
    for name, raw in raw_hosts.items():
        name = _name(name, "ssh inventory host name")
        entry = _object(raw, f"ssh inventory.hosts.{name}")
        _fields(entry, {"ssh", "sciond", "run_dir"}, f"ssh inventory.hosts.{name}",
                {"readiness_command"})
        run_dir = _name(entry["run_dir"], f"ssh inventory.hosts.{name}.run_dir")
        if not run_dir.startswith("/"):
            raise workload.ConfigError(f"ssh inventory.hosts.{name}.run_dir must be absolute")
        readiness = entry.get("readiness_command")
        if readiness is not None and (not isinstance(readiness, str) or not readiness):
            raise workload.ConfigError(f"ssh inventory.hosts.{name}.readiness_command must be a string")
        sciond = _name(entry["sciond"], f"ssh inventory.hosts.{name}.sciond")
        _host_port(sciond, f"ssh inventory.hosts.{name}.sciond")
        hosts[name] = SSHHost(name, _name(entry["ssh"], f"ssh inventory.hosts.{name}.ssh"), sciond,
                              run_dir, readiness)

    server_host = _name(server.node, "server.node")
    client_hosts = {client.client_id: _name(client.endpoint.node, f"{client.client_id}.node")
                    for client in clients}
    for host in [server_host, *client_hosts.values()]:
        if host not in hosts:
            raise workload.ConfigError(f"participant node references unknown host {host}")
    # Every client carries the same global marketplace configuration.
    marketplace_host = next(
        (client.marketplace.host for client in clients if client.marketplace is not None), None,
    )
    if marketplace_host is not None and marketplace_host not in hosts:
        raise workload.ConfigError(
            f"hummingbird.marketplace.host references unknown host {marketplace_host}",
        )

    metrics = _object(deployment["metrics"], "deployment.metrics")
    _fields(metrics, {"prometheus", "local_port_base", "routers"}, "ssh inventory.metrics")
    prometheus_host = _name(metrics["prometheus"], "ssh inventory.metrics.prometheus")
    if prometheus_host not in hosts:
        raise workload.ConfigError(
            "ssh inventory.metrics.prometheus must reference a declared host",
        )
    base = metrics["local_port_base"]
    if not isinstance(base, int) or isinstance(base, bool) or not 1024 <= base <= 65000:
        raise workload.ConfigError("ssh inventory.metrics.local_port_base must be an integer from 1024 through 65000")
    raw_routers = metrics["routers"]
    if not isinstance(raw_routers, list):
        raise workload.ConfigError("ssh inventory.metrics.routers must be an array")
    routers: list[RouterMetrics] = []
    for index, raw in enumerate(raw_routers):
        entry = _object(raw, f"ssh inventory.metrics.routers[{index}]")
        _fields(entry, {"host", "address", "labels"}, f"ssh inventory.metrics.routers[{index}]")
        host, address = (_name(entry["host"], f"ssh inventory.metrics.routers[{index}].host"),
                         _name(entry["address"], f"ssh inventory.metrics.routers[{index}].address"))
        if host not in hosts:
            raise workload.ConfigError(f"ssh inventory.metrics.routers[{index}] has an unknown host or invalid address")
        _host_port(address, f"ssh inventory.metrics.routers[{index}].address")
        labels = _object(entry["labels"], f"ssh inventory.metrics.routers[{index}].labels")
        if not all(isinstance(key, str) and isinstance(value, str) for key, value in labels.items()):
            raise workload.ConfigError(f"ssh inventory.metrics.routers[{index}].labels must be string pairs")
        routers.append(RouterMetrics(host, address, labels))

    raw_shaping = deployment["shaping"]
    if not isinstance(raw_shaping, list):
        raise workload.ConfigError("ssh inventory.shaping must be an array")
    shaping: list[ShapedFlow] = []
    seen_devices: set[tuple[str, str]] = set()
    seen_names: set[tuple[str, str]] = set()
    for index, raw in enumerate(raw_shaping):
        entry = _object(raw, f"ssh inventory.shaping[{index}]")
        _fields(
            entry, {"host", "name", "device", "local", "remote", "expected_root"},
            f"ssh inventory.shaping[{index}]",
        )
        shape = ShapedFlow(
            _name(entry["host"], f"ssh inventory.shaping[{index}].host"),
            _name(entry["name"], f"ssh inventory.shaping[{index}].name"),
            _name(entry["device"], f"ssh inventory.shaping[{index}].device"),
            _name(entry["local"], f"ssh inventory.shaping[{index}].local"),
            _name(entry["remote"], f"ssh inventory.shaping[{index}].remote"),
            _name(entry["expected_root"], f"ssh inventory.shaping[{index}].expected_root"),
        )
        if shape.host not in hosts:
            raise workload.ConfigError(
                f"ssh inventory.shaping[{index}] references unknown host {shape.host}",
            )
        if shape.expected_root not in {"noqueue", "mq"}:
            raise workload.ConfigError(
                f"ssh inventory.shaping[{index}].expected_root must be noqueue or mq",
            )
        if (shape.host, shape.device) in seen_devices:
            raise workload.ConfigError(
                f"ssh inventory.shaping[{index}] duplicates a host device",
            )
        if (shape.host, shape.name) in seen_names:
            raise workload.ConfigError(
                f"ssh inventory.shaping[{index}] duplicates a host qdisc name",
            )
        seen_devices.add((shape.host, shape.device))
        seen_names.add((shape.host, shape.name))
        shaping.append(shape)
    return Inventory(
        hosts, server_host, client_hosts, prometheus_host, base, tuple(routers), tuple(shaping),
        marketplace_host,
    )


def _ssh_argv(host: SSHHost, command: str) -> list[str]:
    # SSH joins remote arguments into a command line; quote the whole sh -c argument for that shell.
    return ["ssh", "--", host.alias, shlex.join(["sh", "-c", command])]


def ssh_command(host: SSHHost, command: str, *, check: bool = True, **kwargs: Any) -> subprocess.CompletedProcess[str]:
    """Run an already quoted, non-secret command through the configured SSH alias."""
    return subprocess.run(_ssh_argv(host, command), check=check, text=True, **kwargs)


def scp_to(host: SSHHost, local: Path, remote: str) -> None:
    subprocess.run(["scp", str(local), f"{host.alias}:{remote}"], check=True, text=True)


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as file:
        for chunk in iter(lambda: file.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def setup_dir(host: SSHHost) -> str:
    return f"{host.run_dir}/{SETUP_DIR_NAME}"


def remote_dir(host: SSHHost, run_id: str) -> str:
    return f"{host.run_dir}/{RUNS_DIR_NAME}/{run_id}"


def preflight(host: SSHHost) -> None:
    daemon_host, daemon_port = _host_port(host.sciond, f"SSH host {host.name} sciond")
    command = " && ".join([
        "command -v sha256sum >/dev/null",
        "command -v nc >/dev/null",
        f"install -d -m 700 {shlex.quote(host.run_dir)}",
        f"nc -z -w 3 {shlex.quote(daemon_host)} {shlex.quote(daemon_port)}",
        host.readiness_command or "true",
    ])
    ssh_command(host, command)


def verify_setup(host: SSHHost, digest: str, need_jwt: bool) -> None:
    binary = setup_dir(host) + "/hummbwtester"
    checks = [
        f"test -x {shlex.quote(binary)}",
        f"test \"$(sha256sum {shlex.quote(binary)} | awk '{{print $1}}')\" = {shlex.quote(digest)}",
    ]
    if need_jwt:
        checks.append(f"test -r {shlex.quote(setup_dir(host) + '/marketplace.jwt')}")
    result = ssh_command(
        host, " && ".join(checks), check=False,
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    if result.returncode:
        raise SSHError(
            f"SSH setup on {host.name} is missing or stale; run experiment.py setup first",
        )


def launch(host: SSHHost, run_id: str, name: str, args: list[str], logfile: Path,
           jwt_file: str | None = None) -> subprocess.Popen[str]:
    """Launch a tester tied to its SSH session and retain a remote PID for cleanup."""
    logfile.parent.mkdir(parents=True, exist_ok=True)
    directory = remote_dir(host, run_id)
    args = [setup_dir(host) + "/hummbwtester", *args[1:]]
    command = f"echo $$ > {shlex.quote(directory + '/' + name + '.pid')}; "
    if jwt_file is not None:
        command += f"export SCION_MARKETPLACE_JWT=$(cat {shlex.quote(jwt_file)}); "
    command += "exec " + shlex.join(args)
    print(f"logging {name} on {host.name} to {logfile}")
    output = logfile.open("w")
    try:
        process = subprocess.Popen(_ssh_argv(host, command), text=True,
                                   stdout=output, stderr=subprocess.STDOUT)
    finally:
        output.close()
    return process


def stop(host: SSHHost, run_id: str, name: str) -> None:
    pidfile = remote_dir(host, run_id) + "/" + name + ".pid"
    ssh_command(host, f"test ! -f {shlex.quote(pidfile)} || kill -TERM $(cat {shlex.quote(pidfile)})",
                check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def cleanup_host(host: SSHHost, run_id: str) -> None:
    ssh_command(host, f"rm -rf {shlex.quote(remote_dir(host, run_id))}", check=False,
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def run_experiment(
    config_path: Path,
    config: tuple[workload.Endpoint, list[workload.Client], dict[str, int], dict[str, str]] | None = None,
    inventory: Inventory | None = None,
) -> int:
    workload.require_built_binary()
    server, clients, _, _ = config if config is not None else workload.load_config(config_path)
    if inventory is None:
        inventory = load_inventory(config_path, server, clients)
    if inventory.local_port_base + len(clients) + len(inventory.routers) > 65535:
        raise workload.ConfigError("SSH metrics tunnel ports exceed 65535")
    run_id = "hummbwtester-" + uuid.uuid4().hex[:12]
    used_hosts = {inventory.server_host, *inventory.client_hosts.values()}
    digest = sha256(BIN)
    processes: list[tuple[SSHHost, str, subprocess.Popen[str]]] = []
    try:
        hummingbird_clients = [client for client in clients if client.hummingbird]
        marketplace_mode = bool(
            hummingbird_clients and hummingbird_clients[0].reservation_source == "marketplace"
        )
        marketplace_hosts = {
            inventory.client_hosts[client.client_id] for client in hummingbird_clients
        } if marketplace_mode else set()
        for name in sorted(used_hosts):
            host = inventory.hosts[name]
            preflight(host)
            verify_setup(host, digest, name in marketplace_hosts)
            ssh_command(host, f"install -d -m 700 {shlex.quote(remote_dir(host, run_id))}")
        server_host = inventory.hosts[inventory.server_host]
        processes.append((server_host, "server", launch(
            server_host, run_id, "server", workload.server_args(server, server_host.sciond),
            ROOT / "logs" / "hummbwtester" / "ssh-server.log")))
        time.sleep(2)
        for client in clients:
            host = inventory.hosts[inventory.client_hosts[client.client_id]]
            jwt_file = (
                setup_dir(host) + "/marketplace.jwt"
                if client.hummingbird and marketplace_mode else None
            )
            processes.append((host, client.client_id, launch(
                host, run_id, client.client_id, workload.client_args(client, server, host.sciond),
                ROOT / "logs" / "hummbwtester" / f"ssh-{client.client_id}.log", jwt_file)))
        failure = False
        while any(process.poll() is None for _, _, process in processes[1:]):
            if processes[0][2].poll() is not None:
                failure = True
                break
            time.sleep(0.25)
        return 1 if failure or any(process.wait() != 0 for _, _, process in processes) else 0
    except KeyboardInterrupt:
        return 130
    finally:
        previous_sigint = workload.ignore_sigint_during_cleanup()
        try:
            for host, name, process in processes:
                stop(host, run_id, name)
                if process.poll() is None:
                    process.terminate()
            for _, _, process in processes:
                process.wait(timeout=10)
            for name in used_hosts:
                cleanup_host(inventory.hosts[name], run_id)
        finally:
            signal.signal(signal.SIGINT, previous_sigint)
