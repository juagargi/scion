#!/usr/bin/env python3
"""Docker orchestration for the local hummbwtester experiment.

The JSON file deliberately contains only endpoint placement. Everything else is derived from the
generated Docker topology so that a stopped topology can be brought back with the same setup.
"""

from __future__ import annotations

import argparse
import ipaddress
import json
from pathlib import Path
import re
import shlex
import subprocess
import sys
import time
from dataclasses import dataclass
from typing import Any

import yaml


# This module lives in tools/hummbwtester/, so two parents up is the repository root.
ROOT = Path(__file__).resolve().parents[2]
GEN = ROOT / "gen"
COMPOSE = GEN / "scion-dc.yml"
SCIOND_ADDRESSES = GEN / "sciond_addresses.json"
CONFIG_DEFAULT = ROOT / "tools" / "hummbwtester" / "hummbwtester.json"
BIN = ROOT / "bin" / "hummbwtester"
TC_SCRIPT = ROOT / "tools" / "hummbwtester" / "tc_setup.sh"
TARGET_DIR = GEN / "hummbwtester-prometheus"
# Metrics ports are intentionally derived rather than stored in the JSON file.
METRICS_BASE_PORT = 9090
CLIENT_ID_RE = re.compile(r"^[A-Za-z0-9._-]+$")
TC_VALUE_RE = re.compile(r"^[0-9]+(?:\.[0-9]+)?(?:bit|kbit|mbit|gbit|b|kb|mb|gb|ms|us|s)?$", re.I)


class ConfigError(ValueError):
    pass


@dataclass(frozen=True)
class Endpoint:
    # A SCION UDP endpoint as represented in the JSON file. frozen=True makes these parsed
    # configuration values immutable after validation.
    isd_as: str
    host: str
    port: int

    def local(self) -> str:
        return f"{self.isd_as},{join_host_port(self.host, self.port)}"


@dataclass(frozen=True)
class Client:
    # The runner adds the derived metric port and the client kind to the endpoint from JSON.
    client_id: str
    endpoint: Endpoint
    hummingbird: bool
    metrics_port: int


def join_host_port(host: str, port: int) -> str:
    """Format an IP address and port, adding brackets for IPv6 addresses."""
    return f"[{host}]:{port}" if ipaddress.ip_address(host).version == 6 else f"{host}:{port}"


def tester_service(ia: str) -> str:
    """Return the generated Docker Compose tester service name for an IA."""
    return "tester_" + ia.replace(":", "_")


def read_json(path: Path) -> dict[str, Any]:
    """Load a JSON object from path and turn parsing failures into ConfigError."""
    try:
        value = json.loads(path.read_text())
    except (OSError, json.JSONDecodeError) as err:
        raise ConfigError(f"reading {path}: {err}") from err
    if not isinstance(value, dict):
        raise ConfigError("configuration root must be an object")
    return value


def require_fields(value: dict[str, Any], expected: set[str], context: str) -> None:
    """Require value to contain exactly expected keys, with contextual error text."""
    # Exact field matching prevents stale, unsupported configuration from being silently ignored.
    got = set(value)
    if got != expected:
        missing = sorted(expected - got)
        extra = sorted(got - expected)
        detail = []
        if missing:
            detail.append("missing " + ", ".join(missing))
        if extra:
            detail.append("unknown " + ", ".join(extra))
        raise ConfigError(f"{context} has " + "; ".join(detail))


def parse_endpoint(value: dict[str, Any], context: str) -> Endpoint:
    """Validate one JSON endpoint object and return its typed representation."""
    require_fields(value, {"isd_as", "host", "port"}, context)
    ia, host, port = value["isd_as"], value["host"], value["port"]
    if not isinstance(ia, str) or not ia:
        raise ConfigError(f"{context}.isd_as must be a non-empty string")
    if not isinstance(host, str):
        raise ConfigError(f"{context}.host must be an IP address")
    try:
        ipaddress.ip_address(host)
    except ValueError as err:
        raise ConfigError(f"{context}.host is not an IP address: {host}") from err
    if not isinstance(port, int) or isinstance(port, bool) or not 0 <= port <= 65535:
        raise ConfigError(f"{context}.port must be an integer from 0 through 65535")
    return Endpoint(ia, host, port)


def load_config(path: Path) -> tuple[Endpoint, list[Client], dict[str, str]]:
    """Parse experiment JSON and derive sorted clients, metrics ports, and tc settings."""
    root = read_json(path)
    require_fields(root, {"server", "hummingbird_clients", "best_effort_clients", "tc"}, "configuration")
    if not isinstance(root["server"], dict):
        raise ConfigError("server must be an object")
    server = parse_endpoint(root["server"], "server")
    if server.port == 0:
        raise ConfigError("server.port must not be zero")

    # Merge both client lists so a single ordering determines metrics ports across all clients.
    raw_clients: list[tuple[dict[str, Any], bool, str]] = []
    for key, hummingbird in (("hummingbird_clients", True), ("best_effort_clients", False)):
        entries = root[key]
        if not isinstance(entries, list):
            raise ConfigError(f"{key} must be an array")
        for index, entry in enumerate(entries):
            if not isinstance(entry, dict):
                raise ConfigError(f"{key}[{index}] must be an object")
            raw_clients.append((entry, hummingbird, f"{key}[{index}]"))
    if not raw_clients:
        raise ConfigError("at least one client is required")

    parsed: list[tuple[str, Endpoint, bool]] = []
    for entry, hummingbird, context in raw_clients:
        require_fields(entry, {"client_id", "isd_as", "host", "port"}, context)
        client_id = entry["client_id"]
        if not isinstance(client_id, str) or not CLIENT_ID_RE.fullmatch(client_id):
            raise ConfigError(f"{context}.client_id must match {CLIENT_ID_RE.pattern}")
        parsed.append((client_id, parse_endpoint({k: entry[k] for k in ("isd_as", "host", "port")}, context), hummingbird))
    # Sorting makes a client's metrics port stable when the JSON array order changes.
    parsed.sort(key=lambda item: item[0])
    if len({item[0] for item in parsed}) != len(parsed):
        raise ConfigError("client_id values must be unique")
    if METRICS_BASE_PORT + len(parsed) - 1 > 65535:
        raise ConfigError("too many clients for the derived Prometheus port range")
    clients = [Client(client_id, endpoint, hummingbird, METRICS_BASE_PORT + index)
               for index, (client_id, endpoint, hummingbird) in enumerate(parsed)]

    tc = root["tc"]
    if not isinstance(tc, dict):
        raise ConfigError("tc must be an object")
    require_fields(tc, {"rate", "burst", "latency"}, "tc")
    for key, value in tc.items():
        if not isinstance(value, str) or not TC_VALUE_RE.fullmatch(value):
            raise ConfigError(f"tc.{key} is not a safe tc value")
    return server, clients, {key: tc[key] for key in ("rate", "burst", "latency")}


def compose_data() -> dict[str, Any]:
    """Load the existing generated Docker Compose configuration without regenerating it."""
    # setup operates on an already generated Docker topology; it must not regenerate gen/.
    if not COMPOSE.is_file():
        raise ConfigError("gen/scion-dc.yml is missing; generate Docker tiny manually with "
                          "./scion.sh topology -d -c topology/tiny.topo")
    if not SCIOND_ADDRESSES.is_file():
        raise ConfigError("gen/sciond_addresses.json is missing")
    try:
        value = yaml.safe_load(COMPOSE.read_text())
    except (OSError, yaml.YAMLError) as err:
        raise ConfigError(f"reading {COMPOSE}: {err}") from err
    if not isinstance(value, dict) or not isinstance(value.get("services"), dict):
        raise ConfigError("gen/scion-dc.yml has no services section")
    return value


def sciond_map() -> dict[str, str]:
    """Load the generated mapping from ISD-AS strings to SCION daemon IP addresses."""
    try:
        value = json.loads(SCIOND_ADDRESSES.read_text())
    except (OSError, json.JSONDecodeError) as err:
        raise ConfigError(f"reading {SCIOND_ADDRESSES}: {err}") from err
    if not isinstance(value, dict):
        raise ConfigError("sciond address map is invalid")
    if not all(isinstance(ia, str) and isinstance(host, str) for ia, host in value.items()):
        raise ConfigError("sciond address map contains non-string entries")
    return value


def endpoint_sciond(endpoint: Endpoint, addresses: dict[str, str]) -> str:
    """Derive the bracketed SCION daemon connector for endpoint's AS."""
    # The generated file stores only the daemon IP. SCION daemons use the fixed API port 30255.
    try:
        return join_host_port(addresses[endpoint.isd_as], 30255)
    except KeyError as err:
        raise ConfigError(f"no sciond address for {endpoint.isd_as}") from err


def validate_endpoints(compose: dict[str, Any], server: Endpoint, clients: list[Client]) -> None:
    """Ensure every configured endpoint belongs to the generated tester for its AS."""
    # A configured endpoint must be the tester address of its AS, not an arbitrary container IP.
    services = compose["services"]
    for endpoint, context in [(server, "server")] + [(client.endpoint, client.client_id) for client in clients]:
        service = tester_service(endpoint.isd_as)
        entry = services.get(service)
        if not isinstance(entry, dict):
            raise ConfigError(f"{context}: tester service {service} is absent from generated topology")
        environment = entry.get("environment", {})
        if not isinstance(environment, dict) or environment.get("SCION_LOCAL_ADDR") != endpoint.host:
            raise ConfigError(f"{context}: host {endpoint.host} does not match {service} SCION_LOCAL_ADDR")


def br_ias() -> dict[str, str]:
    """Map generated border-router service names to their authoritative topology IAs."""
    # Compose service names alone do not reliably identify an AS, so topology.json is authoritative.
    result: dict[str, str] = {}
    for topology in GEN.glob("AS*/topology.json"):
        data = json.loads(topology.read_text())
        ia = data.get("isd_as")
        for br in data.get("border_routers", {}):
            result[br] = ia
    return result


def inter_as_bridges(compose: dict[str, Any]) -> list[str]:
    """Return Docker bridge names that have border routers from different ASes attached."""
    attached: dict[str, list[str]] = {}
    for service, entry in compose["services"].items():
        if not service.startswith("br") or not isinstance(entry, dict):
            continue
        networks = entry.get("networks", {})
        if isinstance(networks, dict):
            for network in networks:
                attached.setdefault(network, []).append(service)
    ia_by_br = br_ias()
    bridges = []
    for network, routers in attached.items():
        # AS110 has two BRs on its internal bridge. Require different IAs to avoid shaping it.
        if len({ia_by_br.get(router) for router in routers}) > 1:
            bridges.append(network)
    if not bridges:
        raise ConfigError("no inter-AS Docker bridges were found in gen/scion-dc.yml")
    return sorted(bridges)


def patch_compose(compose: dict[str, Any], bridges: list[str], tc: dict[str, str]) -> None:
    """Add or replace the profiled host-networked tc setup service in generated Compose."""
    services = compose["services"]
    depends = sorted({router for bridge in bridges for router, entry in services.items()
                      if router.startswith("br") and isinstance(entry, dict) and bridge in entry.get("networks", {})})
    # Keep the privileged, one-shot tc helper behind a profile so normal `scion.sh start` does
    # not leave an exited setup service in the topology status output.
    services["hummbwtester_tc_setup"] = {
        "profiles": ["hummbwtester-setup"],
        "image": "scion/tester:latest",
        "user": "0:0",
        "cap_add": ["NET_ADMIN"],
        "network_mode": "host",
        "depends_on": depends,
        "volumes": [{
            "type": "bind", "source": str(TC_SCRIPT), "target": "/share/hummbwtester_tc_setup.sh", "read_only": True,
        }],
        "entrypoint": ["/bin/bash", "/share/hummbwtester_tc_setup.sh"],
        "command": [tc["rate"], tc["burst"], tc["latency"], *bridges],
    }
    COMPOSE.write_text(yaml.safe_dump(compose, sort_keys=False))


def run(command: list[str], *, check: bool = True, **kwargs: Any) -> subprocess.CompletedProcess[str]:
    """Print and run a command, forwarding subprocess keyword arguments to subprocess.run."""
    # Print shell-escaped commands so setup failures can be reproduced manually.
    print("+", shlex.join(command))
    return subprocess.run(command, check=check, text=True, **kwargs)


def dc_args(*args: str) -> list[str]:
    """Build a Docker Compose command targeting the generated SCION Compose file."""
    return ["docker", "compose", "-f", str(COMPOSE), *args]


def require_built_binary() -> None:
    """Ensure `make build-dev` has produced the standard hummbwtester artifact."""
    if not BIN.is_file():
        raise RuntimeError(
            f"{BIN} does not exist; run `make build-dev` before setting up hummbwtester",
        )


def wait_for_reachability(server: Endpoint, client: Client, timeout: float = 60) -> None:
    """Poll SCION ping from client until it can reach server or timeout expires."""
    # scion ping takes a SCION host address, not the UDP endpoint used by hummbwtester.
    destination = f"{server.isd_as},{server.host}"
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        result = run(dc_args("exec", "-T", tester_service(client.endpoint.isd_as),
                             "scion", "ping", "-c", "1", destination), check=False,
                     stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        if result.returncode == 0:
            return
        time.sleep(1)
    raise RuntimeError(f"timed out waiting for {client.client_id} to reach {destination}")


def write_targets(compose: dict[str, Any], clients: list[Client]) -> None:
    """Write Prometheus file-SD target files for client and border-router metrics."""
    TARGET_DIR.mkdir(parents=True, exist_ok=True)
    # client_id is the only custom Prometheus label for client metrics.
    client_targets = [{
        "targets": [join_host_port(client.endpoint.host, client.metrics_port)],
        "labels": {"client_id": client.client_id},
    } for client in clients]
    (TARGET_DIR / "clients.json").write_text(json.dumps(client_targets, indent=2) + "\n")

    ia_by_br = br_ias()
    targets = []
    for service, ia in sorted(ia_by_br.items()):
        # The BR's internal address is its second generated network address. Read topology instead
        # of relying on Compose network ordering.
        for topology in GEN.glob("AS*/topology.json"):
            data = json.loads(topology.read_text())
            if service in data.get("border_routers", {}):
                internal = data["border_routers"][service]["internal_addr"]
                host = internal.rsplit(":", 1)[0].strip("[]")
                targets.append({"targets": [join_host_port(host, 30442)],
                                "labels": {"as": ia.split("-", 1)[1].replace(":", "_"), "br": service}})
                break
    (TARGET_DIR / "border_routers.json").write_text(json.dumps(targets, indent=2) + "\n")


def setup(config_path: Path) -> int:
    """Build, start, shape, populate, and publish targets for one configured experiment."""
    server, clients, tc = load_config(config_path)
    compose = compose_data()
    validate_endpoints(compose, server, clients)
    _ = [endpoint_sciond(endpoint, sciond_map()) for endpoint in [server, *(c.endpoint for c in clients)]]
    bridges = inter_as_bridges(compose)
    # `make build-dev` builds this Bazel target as a static binary and extracts it into bin/.
    # Reusing that artifact keeps this tool consistent with the other tester-container binaries.
    require_built_binary()
    patch_compose(compose, bridges, tc)
    # This is safe after `scion.sh stop`: Compose recreates the removed bridges before tc runs.
    run([str(ROOT / "scion.sh"), "start"], cwd=ROOT)
    wait_for_reachability(server, clients[0])
    run(dc_args("run", "--rm", "--no-deps", "hummbwtester_tc_setup"), cwd=ROOT)
    for client in clients:
        print(f"client_id={client.client_id} metrics_port={client.metrics_port}")
    for service in sorted({tester_service(server.isd_as), *(tester_service(c.endpoint.isd_as) for c in clients)}):
        run(dc_args("cp", str(BIN), f"{service}:/share/bin/hummbwtester"), cwd=ROOT)
        run(dc_args("exec", "-T", service, "test", "-x", "/share/bin/hummbwtester"), cwd=ROOT)
    write_targets(compose, clients)
    return 0


def verify_binaries(server: Endpoint, clients: list[Client]) -> None:
    """Check that setup copied a runnable hummbwtester binary to every needed tester."""
    # The runner deliberately does not rebuild; setup is responsible for placing this binary.
    services = {tester_service(server.isd_as)}
    services.update(tester_service(client.endpoint.isd_as) for client in clients)
    for service in sorted(services):
        run(dc_args("exec", "-T", service, "test", "-x", "/share/bin/hummbwtester"), cwd=ROOT)


def launch(service: str, pidfile: str, args: list[str], logfile: Path) -> subprocess.Popen[str]:
    """Start one tester process through Compose and record its in-container PID and output."""
    logfile.parent.mkdir(parents=True, exist_ok=True)
    # The shell PID becomes the tester PID after exec. Saving it lets cleanup target exactly this
    # experiment process instead of broadly killing every hummbwtester in the shared container.
    command = "echo $$ > " + shlex.quote(pidfile) + "; exec " + shlex.join(args)
    print(f"logging {service} to {logfile}")
    return subprocess.Popen(dc_args("exec", "-T", service, "/bin/bash", "-c", command), cwd=ROOT,
                            stdout=logfile.open("w"), stderr=subprocess.STDOUT, text=True)


def stop_remote(service: str, pidfile: str) -> None:
    """Send SIGTERM to the process identified by pidfile in a tester container, if present."""
    # A missing pidfile means the process never launched or has already completed; that is harmless.
    run(dc_args("exec", "-T", service, "/bin/bash", "-c",
                f"test ! -f {shlex.quote(pidfile)} || kill -TERM $(cat {shlex.quote(pidfile)})"),
        cwd=ROOT, check=False, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)


def run_experiment(config_path: Path) -> int:
    """Launch the server and all clients, then return their aggregate experiment status."""
    server, clients, _ = load_config(config_path)
    compose = compose_data()
    validate_endpoints(compose, server, clients)
    daemons = sciond_map()
    verify_binaries(server, clients)
    # Regenerate targets here too, so editing client IDs does not require a separate monitoring step.
    write_targets(compose, clients)
    server_service = tester_service(server.isd_as)
    log_dir = ROOT / "logs" / "hummbwtester"
    server_pidfile = "/tmp/hummbwtester-server.pid"
    server_args = ["/share/bin/hummbwtester", "-mode", "server", "-local", server.local(),
                   "-sciond", endpoint_sciond(server, daemons)]
    processes: list[tuple[Client, str, subprocess.Popen[str]]] = []
    server_process = launch(server_service, server_pidfile, server_args, log_dir / "server.log")
    try:
        # Give the server a predictable head start before clients begin selecting paths and dialing.
        time.sleep(2)
        for client in clients:
            suffix = client.client_id
            args = ["/share/bin/hummbwtester", "-mode", "client", "-local", client.endpoint.local(),
                    "-remote", server.local(), "-sciond", endpoint_sciond(client.endpoint, daemons),
                    "-bandwidth", "1Mbps", "-duration", "600s", "-metrics-addr", f":{client.metrics_port}"]
            if client.hummingbird:
                args.extend(["-hummingbird", "1000,10s,1000", "-hummKeysDir", "/share/gen"])
            pidfile = f"/tmp/hummbwtester-{suffix}.pid"
            processes.append((client, pidfile, launch(tester_service(client.endpoint.isd_as), pidfile, args,
                                                       log_dir / f"{suffix}.log")))
        failure = False
        # A prematurely exited server invalidates the experiment even if clients are still alive.
        while any(process.poll() is None for _, _, process in processes):
            if server_process.poll() is not None:
                failure = True
                break
            time.sleep(0.25)
        failure = failure or any(process.wait() != 0 for _, _, process in processes)
        return 1 if failure else 0
    except KeyboardInterrupt:
        return 130
    finally:
        # Always remove the server and any remaining clients on failure or Ctrl-C.
        for client, pidfile, process in processes:
            if process.poll() is None:
                stop_remote(tester_service(client.endpoint.isd_as), pidfile)
                process.terminate()
        stop_remote(server_service, server_pidfile)
        if server_process.poll() is None:
            server_process.terminate()
        for _, _, process in processes:
            process.wait(timeout=10)
        server_process.wait(timeout=10)


def main(default_mode: str | None = None) -> int:
    """Parse CLI arguments and dispatch to setup or run, optionally forcing the mode."""
    # The two small entrypoint scripts pass "setup" or "run" directly. Running this module
    # itself leaves the mode as None, so argparse requires the user to choose one.
    parser = argparse.ArgumentParser()
    if default_mode is None:
        parser.add_argument("mode", choices=("setup", "run"))
    parser.add_argument("--config", type=Path, default=CONFIG_DEFAULT)
    args = parser.parse_args()
    try:
        mode = default_mode if default_mode is not None else args.mode
        return setup(args.config) if mode == "setup" else run_experiment(args.config)
    except (ConfigError, RuntimeError, subprocess.SubprocessError) as err:
        print(f"error: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
