#!/usr/bin/env python3
"""Install and remove persistent infrastructure for SSH hummbwtester experiments."""

from __future__ import annotations

import argparse
from dataclasses import asdict, dataclass
import hashlib
import json
from pathlib import Path
import shlex
import subprocess
import tempfile
from types import SimpleNamespace

try:  # Support both ``python -m``/tests and the small direct entrypoint scripts.
    from . import orchestration as workload
    from . import selective_qdisc
    from . import ssh_orchestration as remote
except ImportError:
    import orchestration as workload
    import selective_qdisc
    import ssh_orchestration as remote


SELECTIVE_HELPER = workload.ROOT / "tools" / "hummbwtester" / "scripts" / "selective_qdisc.py"
PROMETHEUS_CONFIG = workload.ROOT / "tools" / "hummbwtester" / "monitoring" / "prometheus.yml"
REMOTE_PROMETHEUS_DIR = "/tmp/hummbwtester/prometheus"
PROMETHEUS_CONTAINER = "hummbwtester-prometheus"
PROMETHEUS_PROJECT = "hummbwtester-ssh"
PROMETHEUS_IMAGE = "prom/prometheus:latest"
PROMETHEUS_PORT = 8090
CONFIG_HASH_LABEL = "org.scion.hummbwtester.config-sha256"
TUNNEL_STATE_DIR = Path("/tmp/hummbwtester/ssh-tunnels")
TUNNEL_MANIFEST = TUNNEL_STATE_DIR / "manifest.json"


@dataclass(frozen=True)
class TunnelSpec:
    source_alias: str
    prometheus_alias: str
    port: int
    source_address: str


def _check_metrics_ports(inventory: remote.Inventory, clients: list[workload.Client]) -> None:
    if inventory.local_port_base + len(clients) + len(inventory.routers) > 65535:
        raise workload.ConfigError("SSH metrics tunnel ports exceed 65535")


def _remote_file_matches(host: remote.SSHHost, path: str, digest: str) -> bool:
    command = (
        f"test -f {shlex.quote(path)}"
        f" && test \"$(sha256sum {shlex.quote(path)} | awk '{{print $1}}')\""
        f" = {shlex.quote(digest)}"
    )
    result = remote.ssh_command(
        host, command, check=False,
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def copy_if_changed(host: remote.SSHHost, local: Path, target: str, mode: str) -> bool:
    """Copy a file atomically when its content differs; return whether it changed."""
    digest = remote.sha256(local)
    if _remote_file_matches(host, target, digest):
        return False
    temporary = target + ".tmp"
    remote.ssh_command(
        host, f"install -d -m 700 {shlex.quote(str(Path(target).parent))}",
    )
    remote.scp_to(host, local, temporary)
    remote.ssh_command(host, " && ".join([
        f"test \"$(sha256sum {shlex.quote(temporary)} | awk '{{print $1}}')\""
        f" = {shlex.quote(digest)}",
        f"chmod {mode} {shlex.quote(temporary)}",
        f"mv -f {shlex.quote(temporary)} {shlex.quote(target)}",
    ]))
    return True


def deploy_binary(host: remote.SSHHost) -> None:
    target = remote.setup_dir(host) + "/hummbwtester"
    if copy_if_changed(host, remote.BIN, target, "700"):
        print(f"deployed hummbwtester to {host.name}")
    else:
        print(f"hummbwtester on {host.name} is current")


def deploy_jwt(
    inventory: remote.Inventory, clients: list[workload.Client], local_jwt: Path,
) -> None:
    names = {
        inventory.client_hosts[client.client_id]
        for client in clients if client.hummingbird
    }
    for name in sorted(names):
        host = inventory.hosts[name]
        target = remote.setup_dir(host) + "/marketplace.jwt"
        if copy_if_changed(host, local_jwt, target, "600"):
            print(f"uploaded marketplace JWT to {name}")
        else:
            print(f"marketplace JWT on {name} is current")


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
    if action == "up":
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


def _qdisc_is_active(
    host: remote.SSHHost, helper: str, config: selective_qdisc.Config,
) -> bool:
    result = remote.ssh_command(
        host, _selective_command(helper, "status", config), check=False, capture_output=True,
    )
    if result.returncode:
        return False
    try:
        return json.loads(result.stdout).get("managed_root_present") is True
    except json.JSONDecodeError:
        return False


def ensure_selective_qdiscs(
    inventory: remote.Inventory, tc: dict[str, str],
) -> None:
    for shape in inventory.shaping:
        host = inventory.hosts[shape.host]
        helper = remote.setup_dir(host) + "/selective_qdisc.py"
        copy_if_changed(host, SELECTIVE_HELPER, helper, "700")
        config = selective_config(shape, tc)
        desired = asdict(config)
        current = _qdisc_state(host, config.name)
        if current == desired and _qdisc_is_active(host, helper, config):
            print(f"selective qdisc {config.name} on {host.name} is current")
            continue
        if current is not None:
            remote.ssh_command(host, _selective_command(helper, "down", config))
        remote.ssh_command(host, _selective_command(helper, "up", config))
        print(f"installed selective qdisc {config.name} on {host.name}")


def remove_selective_qdiscs(inventory: remote.Inventory, tc: dict[str, str]) -> None:
    for shape in inventory.shaping:
        host = inventory.hosts[shape.host]
        helper = remote.setup_dir(host) + "/selective_qdisc.py"
        config = selective_config(shape, tc)
        if _qdisc_state(host, config.name) is None:
            continue
        if not _remote_file_matches(host, helper, remote.sha256(SELECTIVE_HELPER)):
            copy_if_changed(host, SELECTIVE_HELPER, helper, "700")
        remote.ssh_command(host, _selective_command(helper, "down", config))
        print(f"removed selective qdisc {config.name} from {host.name}")


def target_documents(
    inventory: remote.Inventory, clients: list[workload.Client],
) -> dict[str, str]:
    client_targets = []
    for index, client in enumerate(clients):
        port = inventory.local_port_base + index
        client_targets.append({
            "targets": [f"127.0.0.1:{port}"],
            "labels": {"client_id": client.client_id},
        })
    router_targets = []
    for index, router in enumerate(inventory.routers, start=len(clients)):
        port = inventory.local_port_base + index
        router_targets.append({"targets": [f"127.0.0.1:{port}"], "labels": router.labels})
    return {
        "targets/clients.json": json.dumps(client_targets, indent=2) + "\n",
        "targets/border_routers.json": json.dumps(router_targets, indent=2) + "\n",
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
      - --web.listen-address=:{PROMETHEUS_PORT}
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
    checks = []
    for relative, content in sorted(documents.items()):
        path = f"{REMOTE_PROMETHEUS_DIR}/{relative}"
        digest = hashlib.sha256(content.encode()).hexdigest()
        checks.extend([
            f"test -f {shlex.quote(path)}",
            f"test \"$(sha256sum {shlex.quote(path)} | awk '{{print $1}}')\""
            f" = {shlex.quote(digest)}",
        ])
    result = remote.ssh_command(
        host, " && ".join(checks), check=False,
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def _prometheus_container_current(host: remote.SSHHost, config_hash: str) -> bool:
    result = remote.ssh_command(
        host,
        "docker inspect --format "
        + shlex.quote(f'{{{{.State.Running}}}} {{{{index .Config.Labels "{CONFIG_HASH_LABEL}"}}}}')
        + " " + shlex.quote(PROMETHEUS_CONTAINER),
        check=False, capture_output=True,
    )
    return result.returncode == 0 and result.stdout.strip() == f"true {config_hash}"


def stop_prometheus(host: remote.SSHHost) -> None:
    owner = remote.ssh_command(
        host,
        "docker inspect --format "
        + shlex.quote('{{index .Config.Labels "com.docker.compose.project"}}/{{index .Config.Labels "com.docker.compose.service"}}')
        + " " + shlex.quote(PROMETHEUS_CONTAINER),
        check=False, capture_output=True,
    )
    if owner.returncode:
        return
    expected = f"{PROMETHEUS_PROJECT}/prometheus"
    if owner.stdout.strip() != expected:
        raise remote.SSHError(
            f"refusing to stop {PROMETHEUS_CONTAINER} on {host.name}: "
            f"container belongs to {owner.stdout.strip()!r}, not {expected}",
        )
    remote.ssh_command(host, f"docker rm -f {shlex.quote(PROMETHEUS_CONTAINER)}")


def ensure_prometheus(
    inventory: remote.Inventory, targets: dict[str, str],
) -> None:
    host = inventory.hosts[inventory.prometheus_host]
    documents, config_hash = prometheus_documents(targets)
    remote.ssh_command(host, " && ".join([
        "command -v sha256sum >/dev/null",
        "command -v docker >/dev/null",
        "docker compose version >/dev/null",
        f"install -d -m 755 {shlex.quote(REMOTE_PROMETHEUS_DIR + '/targets')}",
    ]))
    if (_remote_prometheus_files_current(host, documents)
            and _prometheus_container_current(host, config_hash)):
        print(f"Prometheus on {host.name} is current")
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
        f"docker compose --project-name {PROMETHEUS_PROJECT} -f {shlex.quote(compose)} "
        "up -d --force-recreate",
    )
    if not _prometheus_container_current(host, config_hash):
        raise remote.SSHError(f"Prometheus container failed to start on {host.name}")
    print(f"started Prometheus on {host.name}")


def tunnel_specs(
    inventory: remote.Inventory, clients: list[workload.Client],
) -> list[TunnelSpec]:
    prometheus_alias = inventory.hosts[inventory.prometheus_host].alias
    specs = []
    for index, client in enumerate(clients):
        source = inventory.hosts[inventory.client_hosts[client.client_id]]
        specs.append(TunnelSpec(
            source.alias, prometheus_alias, inventory.local_port_base + index,
            f"127.0.0.1:{client.metrics_port}",
        ))
    for index, router in enumerate(inventory.routers, start=len(clients)):
        source = inventory.hosts[router.host]
        specs.append(TunnelSpec(
            source.alias, prometheus_alias, inventory.local_port_base + index, router.address,
        ))
    return specs


def _tunnel_hash(specs: list[TunnelSpec]) -> str:
    content = json.dumps([asdict(spec) for spec in specs], sort_keys=True, separators=(",", ":"))
    return hashlib.sha256(content.encode()).hexdigest()


def _control_command(socket: str, operation: str, alias: str) -> list[str]:
    return ["ssh", "-S", socket, "-O", operation, "--", alias]


def _master_running(master: dict[str, str]) -> bool:
    result = subprocess.run(
        _control_command(master["socket"], "check", master["alias"]),
        check=False, text=True, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    return result.returncode == 0


def _load_tunnel_manifest() -> dict[str, object] | None:
    try:
        value = json.loads(TUNNEL_MANIFEST.read_text())
    except (FileNotFoundError, json.JSONDecodeError, OSError):
        return None
    return value if isinstance(value, dict) else None


def stop_tunnels() -> None:
    manifest = _load_tunnel_manifest()
    failures = []
    if manifest is not None:
        masters = manifest.get("masters", [])
        if isinstance(masters, list):
            for master in reversed(masters):
                if not isinstance(master, dict):
                    continue
                socket = master.get("socket")
                alias = master.get("alias")
                if not isinstance(socket, str) or not isinstance(alias, str):
                    continue
                if _master_running({"socket": socket, "alias": alias}):
                    result = subprocess.run(
                        _control_command(socket, "exit", alias), check=False, text=True,
                        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                    )
                    if result.returncode:
                        failures.append(alias)
                        continue
                Path(socket).unlink(missing_ok=True)
    if failures:
        raise remote.SSHError(
            "failed to stop SSH tunnel masters for " + ", ".join(failures),
        )
    TUNNEL_MANIFEST.unlink(missing_ok=True)
    try:
        TUNNEL_STATE_DIR.rmdir()
    except OSError:
        pass


def _start_master(arguments: list[str], socket: Path, alias: str) -> dict[str, str]:
    command = [
        "ssh", "-M", "-S", str(socket), "-f", "-N",
        "-o", "ExitOnForwardFailure=yes",
        "-o", "ServerAliveInterval=15",
        "-o", "ServerAliveCountMax=3",
        *arguments, "--", alias,
    ]
    subprocess.run(command, check=True, text=True)
    return {"socket": str(socket), "alias": alias}


def ensure_tunnels(specs: list[TunnelSpec]) -> None:
    if not specs:
        stop_tunnels()
        print("no Prometheus metric relays are configured")
        return
    digest = _tunnel_hash(specs)
    manifest = _load_tunnel_manifest()
    if manifest is not None and manifest.get("spec_sha256") == digest:
        masters = manifest.get("masters")
        if isinstance(masters, list) and masters and all(
            isinstance(master, dict)
            and isinstance(master.get("socket"), str)
            and isinstance(master.get("alias"), str)
            and _master_running(master)
            for master in masters
        ):
            print("Prometheus SSH tunnels are current")
            return

    stop_tunnels()
    TUNNEL_STATE_DIR.mkdir(mode=0o700, parents=True, exist_ok=True)
    masters: list[dict[str, str]] = []
    try:
        for index, spec in enumerate(specs):
            source_socket = TUNNEL_STATE_DIR / f"source-{index}.sock"
            masters.append(_start_master(
                ["-L", f"127.0.0.1:{spec.port}:{spec.source_address}"],
                source_socket, spec.source_alias,
            ))
            prometheus_socket = TUNNEL_STATE_DIR / f"prometheus-{index}.sock"
            masters.append(_start_master(
                ["-R", f"127.0.0.1:{spec.port}:127.0.0.1:{spec.port}"],
                prometheus_socket, spec.prometheus_alias,
            ))
    except BaseException:
        temporary_manifest = {"masters": masters}
        TUNNEL_MANIFEST.write_text(json.dumps(temporary_manifest))
        stop_tunnels()
        raise
    TUNNEL_MANIFEST.write_text(json.dumps({
        "spec_sha256": digest,
        "masters": masters,
    }, indent=2) + "\n")
    TUNNEL_MANIFEST.chmod(0o600)
    print(f"started {len(specs)} Prometheus metric relays")


def setup(
    config_path: Path,
    config: tuple[workload.Endpoint, list[workload.Client], dict[str, int], dict[str, str]] | None = None,
    inventory: remote.Inventory | None = None,
) -> int:
    workload.require_built_binary()
    server, clients, _, tc = config if config is not None else workload.load_config(config_path)
    if inventory is None:
        inventory = remote.load_inventory(config_path, server, clients)
    _check_metrics_ports(inventory, clients)
    used_hosts = {
        inventory.server_host,
        *inventory.client_hosts.values(),
        *(shape.host for shape in inventory.shaping),
    }
    for name in sorted(used_hosts):
        host = inventory.hosts[name]
        remote.preflight(host)
        deploy_binary(host)

    hummingbird_clients = [client for client in clients if client.hummingbird]
    local_jwt: Path | None = None
    try:
        if hummingbird_clients and hummingbird_clients[0].reservation_source == "marketplace":
            marketplace = hummingbird_clients[0].marketplace
            assert marketplace is not None
            local_jwt = workload.write_private_jwt(workload.obtain_marketplace_jwt(marketplace))
            deploy_jwt(inventory, hummingbird_clients, local_jwt)
        ensure_selective_qdiscs(inventory, tc)
        targets = target_documents(inventory, clients)
        write_local_targets(targets)
        ensure_prometheus(inventory, targets)
        ensure_tunnels(tunnel_specs(inventory, clients))
        return 0
    finally:
        if local_jwt is not None:
            local_jwt.unlink(missing_ok=True)


def teardown(
    config_path: Path,
    config: tuple[workload.Endpoint, list[workload.Client], dict[str, int], dict[str, str]] | None = None,
    inventory: remote.Inventory | None = None,
) -> int:
    server, clients, _, tc = config if config is not None else workload.load_config(config_path)
    if inventory is None:
        inventory = remote.load_inventory(config_path, server, clients)
    errors = []
    actions = [
        lambda: remove_selective_qdiscs(inventory, tc),
        stop_tunnels,
        lambda: stop_prometheus(inventory.hosts[inventory.prometheus_host]),
    ]
    for action in actions:
        try:
            action()
        except (OSError, RuntimeError, subprocess.SubprocessError) as err:
            errors.append(str(err))

    used_hosts = {
        inventory.server_host,
        *inventory.client_hosts.values(),
        *(shape.host for shape in inventory.shaping),
    }
    for name in sorted(used_hosts):
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
