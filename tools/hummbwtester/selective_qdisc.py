#!/usr/bin/env python3
"""Rate-limit one locally generated UDP flow with a classful egress qdisc.

The helper replaces an explicitly acknowledged automatic root qdisc with a two-band PRIO qdisc.
An exact IPv4 or IPv6 UDP flow is classified into the first band, which contains a TBF.
All other traffic uses the second, unshaped band. Only ``up`` and ``down`` mutate the host;
``diagnose`` and ``status`` are read-only.
"""

from __future__ import annotations

import argparse
from dataclasses import asdict, dataclass
import fcntl
import ipaddress
import json
import os
from pathlib import Path
import re
import shutil
import subprocess
import sys


STATE_DIRECTORY = Path("/run/hummbwtester-qdisc")
LOCK_FILE = Path("/run/lock/hummbwtester-qdisc.lock")
NAME_RE = re.compile(r"[A-Za-z0-9_.-]{1,32}")
DEVICE_RE = re.compile(r"[A-Za-z0-9_.-]{1,15}")
TC_RE = re.compile(r"[0-9]+(?:\.[0-9]+)?(?:bit|kbit|mbit|gbit|b|kb|mb|gb)")
SUPPORTED_ROOTS = ("noqueue", "mq")
ROOT_HANDLE = "1:"
SHAPED_CLASS = "1:1"
UNSHAPED_CLASS = "1:2"
TBF_HANDLE = "10:"
FILTER_PRIORITY = "10"


class Error(RuntimeError):
    pass


@dataclass(frozen=True)
class Endpoint:
    address: str
    port: int
    zone: str | None = None

    @classmethod
    def parse(cls, value: str) -> Endpoint:
        if value.startswith("["):
            closing = value.find("]")
            if closing < 0 or value[closing + 1:closing + 2] != ":":
                raise argparse.ArgumentTypeError(f"invalid bracketed UDP endpoint: {value}")
            host = value[1:closing]
            port = value[closing + 2:]
        else:
            if value.count(":") != 1:
                raise argparse.ArgumentTypeError(
                    f"IPv6 UDP endpoints must use [address%zone]:port: {value}",
                )
            host, port = value.rsplit(":", 1)
        if not port.isdecimal() or not 1 <= int(port) <= 65535:
            raise argparse.ArgumentTypeError(f"invalid UDP port in endpoint: {value}")
        address_text, separator, zone = host.partition("%")
        if separator and (not zone or not DEVICE_RE.fullmatch(zone)):
            raise argparse.ArgumentTypeError(f"invalid IPv6 zone in endpoint: {value}")
        try:
            address = ipaddress.ip_address(address_text)
        except ValueError as err:
            raise argparse.ArgumentTypeError(f"invalid IP address in endpoint: {value}") from err
        if address.is_unspecified or address.is_multicast:
            raise argparse.ArgumentTypeError(f"endpoint must contain a unicast address: {value}")
        if address.version == 4 and separator:
            raise argparse.ArgumentTypeError(f"IPv4 endpoints cannot contain a zone: {value}")
        return cls(str(address), int(port), zone or None)

    @property
    def family(self) -> int:
        return ipaddress.ip_address(self.address).version

    def __str__(self) -> str:
        if self.family == 6:
            zone = f"%{self.zone}" if self.zone else ""
            return f"[{self.address}{zone}]:{self.port}"
        return f"{self.address}:{self.port}"


@dataclass(frozen=True)
class Config:
    name: str
    device: str
    local: Endpoint
    remote: Endpoint
    rate: str
    burst: str
    limit: str
    expected_root: str


def run(
    arguments: list[str], *, check: bool = True, capture: bool = False,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(arguments, check=check, text=True, capture_output=capture)


def output(arguments: list[str]) -> str:
    return run(arguments, capture=True).stdout.strip()


def require_commands() -> None:
    missing = [command for command in ("ip", "tc") if shutil.which(command) is None]
    if missing:
        raise Error("missing required commands: " + ", ".join(missing))


def require_root() -> None:
    if os.geteuid() != 0:
        raise Error("this action must run as root")


def state_path(name: str) -> Path:
    if not NAME_RE.fullmatch(name):
        raise Error("name must contain 1-32 letters, digits, dots, underscores, or hyphens")
    return STATE_DIRECTORY / f"{name}.json"


def endpoint_from_json(value: dict[str, object]) -> Endpoint:
    zone = value.get("zone")
    return Endpoint(str(value["address"]), int(value["port"]), str(zone) if zone else None)


def load_state(name: str) -> Config:
    try:
        value = json.loads(state_path(name).read_text())
    except FileNotFoundError as err:
        raise Error(f"no active selective qdisc named {name}") from err
    value["local"] = endpoint_from_json(value["local"])
    value["remote"] = endpoint_from_json(value["remote"])
    return Config(**value)


def save_state(config: Config) -> None:
    STATE_DIRECTORY.mkdir(mode=0o700, parents=True, exist_ok=True)
    path = state_path(config.name)
    temporary = path.with_suffix(".tmp")
    temporary.write_text(json.dumps(asdict(config), indent=2) + "\n")
    temporary.chmod(0o600)
    temporary.replace(path)


def remove_state(config: Config) -> None:
    state_path(config.name).unlink(missing_ok=True)
    try:
        STATE_DIRECTORY.rmdir()
    except OSError:
        pass


def lock():
    LOCK_FILE.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    file = LOCK_FILE.open("a+")
    fcntl.flock(file, fcntl.LOCK_EX)
    return file


def validate_tc(value: str) -> str:
    if not TC_RE.fullmatch(value):
        raise argparse.ArgumentTypeError(f"unsafe or unsupported tc value: {value}")
    return value


def build_config(args: argparse.Namespace) -> Config:
    if not NAME_RE.fullmatch(args.name):
        raise Error("name must contain 1-32 letters, digits, dots, underscores, or hyphens")
    if not DEVICE_RE.fullmatch(args.device):
        raise Error(f"invalid Linux interface name: {args.device!r}")
    if args.local.family != args.remote.family:
        raise Error("local and remote endpoints must use the same address family")
    for endpoint in (args.local, args.remote):
        if endpoint.zone and endpoint.zone != args.device:
            raise Error(
                f"endpoint zone {endpoint.zone!r} does not match device {args.device!r}",
            )
    return Config(
        name=args.name,
        device=args.device,
        local=args.local,
        remote=args.remote,
        rate=args.rate,
        burst=args.burst,
        limit=args.limit,
        expected_root=args.expected_root,
    )


def interface_details(device: str) -> dict[str, object]:
    result = run(["ip", "-j", "-details", "link", "show", "dev", device],
                 check=False, capture=True)
    if result.returncode:
        raise Error(f"network interface {device} does not exist")
    return json.loads(result.stdout)[0]


def local_addresses(device: str) -> set[str]:
    entries = json.loads(output(["ip", "-j", "address", "show", "dev", device]))
    return {info["local"] for entry in entries for info in entry.get("addr_info", [])}


def route_get(config: Config) -> dict[str, object]:
    family = "-4" if config.local.family == 4 else "-6"
    arguments = [
        "ip", "-j", family, "route", "get", config.remote.address,
        "from", config.local.address,
    ]
    if config.local.family == 6:
        arguments.extend(["oif", config.device])
    arguments.extend([
        "ipproto", "udp", "sport", str(config.local.port), "dport", str(config.remote.port),
    ])
    return json.loads(output(arguments))[0]


def qdiscs(device: str) -> list[dict[str, object]]:
    result = output(["tc", "-j", "-d", "qdisc", "show", "dev", device])
    return json.loads(result or "[]")


def root_qdisc(device: str) -> dict[str, object]:
    roots = [entry for entry in qdiscs(device) if entry.get("root") is True]
    if len(roots) != 1:
        raise Error(f"expected exactly one root qdisc on {device}, found {len(roots)}")
    return roots[0]


def root_matches(device: str, kind: str, handle: str | None = None) -> bool:
    root = root_qdisc(device)
    return root.get("kind") == kind and (handle is None or root.get("handle") == handle)


def managed_root_present(config: Config) -> bool:
    return root_matches(config.device, "prio", ROOT_HANDLE)


def active_configs() -> list[Config]:
    if not STATE_DIRECTORY.exists():
        return []
    return [load_state(path.stem) for path in STATE_DIRECTORY.glob("*.json")]


def ensure_unused(config: Config) -> None:
    if state_path(config.name).exists():
        raise Error(f"selective qdisc {config.name} is already active")
    for active in active_configs():
        if active.device == config.device:
            raise Error(
                f"interface {config.device} is already managed by selective qdisc {active.name}",
            )


def validate_host(config: Config) -> None:
    interface_details(config.device)
    if config.local.address not in local_addresses(config.device):
        raise Error(f"{config.local.address} is not assigned to {config.device}")
    route = route_get(config)
    if route.get("dev") != config.device:
        raise Error(
            f"the selected flow currently routes through {route.get('dev')}, not {config.device}",
        )
    root = root_qdisc(config.device)
    if root.get("kind") != config.expected_root:
        raise Error(
            f"root qdisc is {root.get('kind')}, expected {config.expected_root}; "
            "refusing replacement",
        )


def prio_command(config: Config) -> list[str]:
    return [
        "tc", "qdisc", "replace", "dev", config.device, "root", "handle", ROOT_HANDLE,
        "prio", "bands", "2", "priomap", *(["1"] * 16),
    ]


def tbf_command(config: Config) -> list[str]:
    return [
        "tc", "qdisc", "replace", "dev", config.device,
        "parent", SHAPED_CLASS, "handle", TBF_HANDLE,
        "tbf", "rate", config.rate, "burst", config.burst, "limit", config.limit,
    ]


def filter_command(config: Config) -> list[str]:
    protocol = "ip" if config.local.family == 4 else "ipv6"
    return [
        "tc", "filter", "add", "dev", config.device, "parent", ROOT_HANDLE,
        "protocol", protocol, "pref", FILTER_PRIORITY, "flower",
        "ip_proto", "udp",
        "src_ip", config.local.address,
        "dst_ip", config.remote.address,
        "src_port", str(config.local.port),
        "dst_port", str(config.remote.port),
        "classid", SHAPED_CLASS,
    ]


def restore_baseline(config: Config) -> None:
    if root_matches(config.device, config.expected_root):
        return
    if not managed_root_present(config):
        root = root_qdisc(config.device)
        raise Error(
            f"refusing cleanup: root qdisc is {root.get('kind')} {root.get('handle')}, "
            f"not the managed prio {ROOT_HANDLE}",
        )
    run(["tc", "qdisc", "del", "dev", config.device, "root"])
    if config.expected_root == "mq" and not root_matches(config.device, "mq"):
        run(["tc", "qdisc", "replace", "dev", config.device, "root", "mq"])
    if not root_matches(config.device, config.expected_root):
        root = root_qdisc(config.device)
        raise Error(
            f"cleanup did not restore {config.expected_root}; current root is {root.get('kind')}",
        )


def create(config: Config) -> None:
    validate_host(config)
    ensure_unused(config)
    save_state(config)
    try:
        run(prio_command(config))
        run(tbf_command(config))
        run(filter_command(config))
    except BaseException as original:
        try:
            restore_baseline(config)
            remove_state(config)
        except BaseException as cleanup:
            raise Error(
                f"setup failed ({original}); rollback also failed ({cleanup})",
            ) from original
        raise


def destroy(config: Config) -> None:
    restore_baseline(config)
    remove_state(config)


def tc_json(arguments: list[str]) -> object:
    result = run(arguments, check=False, capture=True)
    if result.returncode:
        return {"error": result.stderr.strip()}
    return json.loads(result.stdout or "[]")


def status(config: Config) -> int:
    report = {
        "name": config.name,
        "flow": f"{config.local} -> {config.remote}",
        "device": config.device,
        "expected_root": config.expected_root,
        "managed_root_present": managed_root_present(config),
        "qdiscs": tc_json(["tc", "-j", "-s", "qdisc", "show", "dev", config.device]),
        "classes": tc_json(["tc", "-j", "-s", "class", "show", "dev", config.device]),
        "filters": tc_json([
            "tc", "-j", "-s", "filter", "show", "dev", config.device,
            "parent", ROOT_HANDLE,
        ]),
    }
    print(json.dumps(report, indent=2))
    return 0


def read_integer(path: Path) -> int | None:
    try:
        return int(path.read_text().strip())
    except (OSError, ValueError):
        return None


def diagnose(config: Config) -> int:
    require_commands()
    details = interface_details(config.device)
    addresses = local_addresses(config.device)
    route = route_get(config)
    current_qdiscs = qdiscs(config.device)
    root = root_qdisc(config.device)
    warnings = []
    if config.local.address not in addresses:
        warnings.append(f"{config.local.address} is not assigned to {config.device}")
    if route.get("dev") != config.device:
        warnings.append(f"the current route uses {route.get('dev')}, not {config.device}")
    if root.get("kind") != config.expected_root:
        warnings.append(
            f"root qdisc is {root.get('kind')}, not expected {config.expected_root}",
        )
    try:
        ensure_unused(config)
    except Error as err:
        warnings.append(str(err))
    report = {
        "result": "ready" if not warnings else "warnings",
        "flow": f"{config.local} -> {config.remote}",
        "device": {
            "name": config.device,
            "mtu": details.get("mtu"),
            "tx_queues": details.get("num_tx_queues"),
        },
        "current_route": route,
        "current_qdiscs": current_qdiscs,
        "planned": {
            "root": "prio",
            "shaped_class": SHAPED_CLASS,
            "unshaped_class": UNSHAPED_CLASS,
            "tbf": {"rate": config.rate, "burst": config.burst, "limit": config.limit},
            "protocol": "ip" if config.local.family == 4 else "ipv6",
            "expected_restored_root": config.expected_root,
        },
        "socket_send_buffer_defaults": {
            "wmem_default": read_integer(Path("/proc/sys/net/core/wmem_default")),
            "wmem_max": read_integer(Path("/proc/sys/net/core/wmem_max")),
        },
        "warnings": warnings,
    }
    print(json.dumps(report, indent=2))
    return 0 if not warnings else 2


def add_common(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--name", default="humm-br2")
    parser.add_argument("--device", required=True, help="egress interface whose root is replaced")
    parser.add_argument("--local", required=True, type=Endpoint.parse, metavar="IP:PORT")
    parser.add_argument("--remote", required=True, type=Endpoint.parse, metavar="IP:PORT")
    parser.add_argument("--rate", type=validate_tc, default="10mbit")
    parser.add_argument("--burst", type=validate_tc, default="50kb")
    parser.add_argument("--limit", type=validate_tc, default="1mb")
    parser.add_argument(
        "--expected-root", required=True, choices=SUPPORTED_ROOTS,
        help="automatic root qdisc that down must restore",
    )


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="action", required=True)
    for action in ("diagnose", "up"):
        add_common(subparsers.add_parser(action))
    for action in ("status", "down"):
        child = subparsers.add_parser(action)
        child.add_argument("--name", default="humm-br2")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    try:
        if args.action == "diagnose":
            return diagnose(build_config(args))
        if args.action == "status":
            return status(load_state(args.name))
        require_root()
        require_commands()
        with lock():
            if args.action == "up":
                config = build_config(args)
                create(config)
                return status(config)
            # Down requested:
            config = load_state(args.name)
            destroy(config)
            return 0
    except (Error, OSError, ValueError, subprocess.SubprocessError) as err:
        print(f"error: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
