#!/usr/bin/env python3
"""Shape one host-local UDP flow through a veth hairpin.

The selected locally generated flow is marked in netfilter OUTPUT. Policy routing sends it through
one end of a veth pair, whose root qdisc is a TBF. The other end reinjects the packet into the host,
where ordinary forwarding sends it through the original interface. Return traffic is unchanged.

The border router remains in the host network namespace and keeps its original underlay endpoint.
Only ``up`` and ``down`` mutate the host; ``diagnose`` and ``status`` are read-only.
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
import shlex
import shutil
import subprocess
import sys
import uuid


STATE_DIRECTORY = Path("/run/hummbwtester-veth")
LOCK_FILE = Path("/run/lock/hummbwtester-veth.lock")
NAME_RE = re.compile(r"[A-Za-z0-9_.-]{1,32}")
TC_RE = re.compile(r"[0-9]+(?:\.[0-9]+)?(?:bit|kbit|mbit|gbit|b|kb|mb|gb)")


class Error(RuntimeError):
    pass


@dataclass(frozen=True)
class Endpoint:
    address: str
    port: int

    @classmethod
    def parse(cls, value: str) -> Endpoint:
        host, separator, port = value.rpartition(":")
        if not separator or not port.isdecimal() or not 1 <= int(port) <= 65535:
            raise argparse.ArgumentTypeError(f"invalid IPv4 UDP endpoint: {value}")
        try:
            address = ipaddress.ip_address(host)
        except ValueError as err:
            raise argparse.ArgumentTypeError(f"invalid IPv4 UDP endpoint: {value}") from err
        if address.version != 4 or address.is_unspecified or address.is_multicast:
            raise argparse.ArgumentTypeError(
                f"endpoint must contain a unicast IPv4 address: {value}",
            )
        return cls(str(address), int(port))

    def __str__(self) -> str:
        return f"{self.address}:{self.port}"


@dataclass(frozen=True)
class Config:
    name: str
    device: str
    out_device: str
    in_device: str
    local: Endpoint
    remote: Endpoint
    veth_network: str
    out_address: str
    in_address: str
    rate: str
    burst: str
    limit: str
    mark: int
    table: int
    rule_priority: int
    token: str

    @property
    def comment(self) -> str:
        return f"hummbwtester-veth:{self.name}"

    @property
    def mark_spec(self) -> str:
        return f"{self.mark:#x}/0xffffffff"


def run(
    arguments: list[str], *, check: bool = True, capture: bool = False,
) -> subprocess.CompletedProcess[str]:
    return subprocess.run(arguments, check=check, text=True,
                          capture_output=capture)


def output(arguments: list[str]) -> str:
    return run(arguments, capture=True).stdout.strip()


def require_commands() -> None:
    missing = [command for command in ("ip", "iptables", "sysctl", "tc")
               if shutil.which(command) is None]
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
    return Endpoint(str(value["address"]), int(value["port"]))


def load_state(name: str) -> Config:
    try:
        value = json.loads(state_path(name).read_text())
    except FileNotFoundError as err:
        raise Error(f"no active veth shaper named {name}") from err
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


def lock():
    LOCK_FILE.parent.mkdir(mode=0o755, parents=True, exist_ok=True)
    file = LOCK_FILE.open("a+")
    fcntl.flock(file, fcntl.LOCK_EX)
    return file


def interface_details(device: str) -> dict[str, object]:
    result = run(["ip", "-j", "-details", "link", "show", "dev", device],
                 check=False, capture=True)
    if result.returncode:
        raise Error(f"network interface {device} does not exist")
    return json.loads(result.stdout)[0]


def interface_alias(device: str) -> str | None:
    return interface_details(device).get("ifalias")  # type: ignore[return-value]


def sysctl_value(name: str) -> int:
    return int(output(["sysctl", "-n", name]))


def validate_tc(value: str) -> str:
    if not TC_RE.fullmatch(value):
        raise argparse.ArgumentTypeError(f"unsafe or unsupported tc value: {value}")
    return value


def build_config(args: argparse.Namespace) -> Config:
    if not NAME_RE.fullmatch(args.name):
        raise Error("name must contain 1-32 letters, digits, dots, underscores, or hyphens")
    if not args.device or len(args.device) > 15 or any(char.isspace() for char in args.device):
        raise Error(f"invalid Linux interface name: {args.device!r}")
    for device in (args.out_device, args.in_device):
        if not re.fullmatch(r"[A-Za-z0-9_-]{1,15}", device):
            raise Error(
                "veth name must contain 1-15 letters, digits, underscores, or hyphens: "
                f"{device!r}",
            )
    network = ipaddress.ip_network(args.veth_network, strict=True)
    if network.version != 4 or network.prefixlen != 30:
        raise Error("veth-network must be an IPv4 /30")
    hosts = list(network.hosts())
    if args.local.address in {str(address) for address in network} or \
            args.remote.address in {str(address) for address in network}:
        raise Error("veth-network must not contain either underlay endpoint")
    if not 1 <= args.table <= 2**31 - 1 or not 1 <= args.rule_priority <= 32765:
        raise Error("table must be 1..2147483647 and rule-priority must be 1..32765")
    if not 1 <= args.mark <= 0xffffffff:
        raise Error("mark must be 1..0xffffffff")
    return Config(
        name=args.name,
        device=args.device,
        out_device=args.out_device,
        in_device=args.in_device,
        local=args.local,
        remote=args.remote,
        veth_network=str(network),
        out_address=f"{hosts[0]}/{network.prefixlen}",
        in_address=f"{hosts[1]}/{network.prefixlen}",
        rate=args.rate,
        burst=args.burst,
        limit=args.limit,
        mark=args.mark,
        table=args.table,
        rule_priority=args.rule_priority,
        token=uuid.uuid4().hex,
    )


def route_get(config: Config) -> dict[str, object]:
    result = output([
        "ip", "-j", "route", "get", config.remote.address,
        "from", config.local.address,
        "ipproto", "udp", "sport", str(config.local.port), "dport", str(config.remote.port),
    ])
    return json.loads(result)[0]


def local_addresses(device: str) -> set[str]:
    entries = json.loads(output(["ip", "-j", "address", "show", "dev", device]))
    return {info["local"] for entry in entries for info in entry.get("addr_info", [])}


def configured_ipv4_networks() -> set[ipaddress.IPv4Network]:
    entries = json.loads(output(["ip", "-j", "address", "show"]))
    return {
        ipaddress.ip_network(f"{info['local']}/{info['prefixlen']}", strict=False)
        for entry in entries
        for info in entry.get("addr_info", [])
        if info.get("family") == "inet"
    }


def diagnose(config: Config) -> int:
    require_commands()
    details = interface_details(config.device)
    addresses = local_addresses(config.device)
    route = route_get(config)
    qdiscs = json.loads(output(["tc", "-j", "qdisc", "show", "dev", config.device]) or "[]")
    forward_policy = output(["iptables", "-w", "-S", "FORWARD"])
    warnings = []
    if config.local.address not in addresses:
        warnings.append(f"{config.local.address} is not assigned to {config.device}")
    if route.get("dev") != config.device:
        warnings.append(f"the current route uses {route.get('dev')}, not {config.device}")
    try:
        ensure_unused(config)
    except Error as err:
        warnings.append(str(err))
    report = {
        "result": "ready" if not warnings else "warnings",
        "flow": f"{config.local} -> {config.remote}",
        "original_interface": {
            "name": config.device,
            "mtu": details.get("mtu"),
            "qdiscs": [entry.get("kind") for entry in qdiscs],
        },
        "current_route": route,
        "forward_policy": forward_policy.splitlines()[0] if forward_policy else "unknown",
        "global_ipv4_forwarding": sysctl_value("net.ipv4.ip_forward"),
        "planned_veth": {
            "out": config.out_device,
            "in": config.in_device,
            "network": config.veth_network,
            "table": config.table,
            "mark": f"{config.mark:#x}",
        },
        "warnings": warnings,
    }
    print(json.dumps(report, indent=2))
    return 0 if not warnings else 2


def iptables_rule(table: str, chain: str, rule: list[str], operation: str) -> None:
    run(["iptables", "-w", "-t", table, operation, chain, *rule], check=operation != "-D")


def output_mark_rule(config: Config) -> list[str]:
    return [
        "-o", config.device, "-p", "udp",
        "-s", config.local.address, "--sport", str(config.local.port),
        "-d", config.remote.address, "--dport", str(config.remote.port),
        "-m", "comment", "--comment", config.comment,
        "-j", "MARK", "--set-xmark", config.mark_spec,
    ]


def clear_mark_rule(config: Config) -> list[str]:
    return [
        "-i", config.in_device, "-m", "mark", "--mark", config.mark_spec,
        "-m", "comment", "--comment", config.comment,
        "-j", "MARK", "--set-xmark", "0/0xffffffff",
    ]


def forward_rule(config: Config) -> list[str]:
    return [
        "-i", config.in_device, "-o", config.device, "-p", "udp",
        "-s", config.local.address, "--sport", str(config.local.port),
        "-d", config.remote.address, "--dport", str(config.remote.port),
        "-m", "comment", "--comment", config.comment,
        "-j", "ACCEPT",
    ]


def delete_rule(config: Config, table: str, chain: str, rule: list[str]) -> None:
    result = run(["iptables", "-w", "-t", table, "-C", chain, *rule],
                 check=False, capture=True)
    if result.returncode == 0:
        run(["iptables", "-w", "-t", table, "-D", chain, *rule])


def ensure_unused(config: Config) -> None:
    if state_path(config.name).exists():
        raise Error(f"veth shaper {config.name} is already active")
    active = list(STATE_DIRECTORY.glob("*.json")) if STATE_DIRECTORY.exists() else []
    if active:
        raise Error(f"another veth shaper is active ({active[0].stem}); only one is supported")
    for device in (config.out_device, config.in_device):
        if run(["ip", "link", "show", "dev", device], check=False,
               capture=True).returncode == 0:
            raise Error(f"network interface {device} already exists")
    planned_network = ipaddress.ip_network(config.veth_network)
    for network in configured_ipv4_networks():
        if planned_network.overlaps(network):
            raise Error(f"veth network {planned_network} overlaps configured network {network}")
    rules = output(["ip", "-4", "rule", "show"])
    if re.search(rf"^{config.rule_priority}:", rules, re.MULTILINE):
        raise Error(f"IP rule priority {config.rule_priority} is already in use")
    if f"fwmark {config.mark:#x}" in rules:
        raise Error(f"packet mark {config.mark:#x} is already used by an IP rule")
    routes = run(["ip", "-j", "-4", "route", "show", "table", str(config.table)],
                 check=False, capture=True)
    if routes.returncode == 0 and json.loads(routes.stdout):
        raise Error(f"routing table {config.table} is already in use")


def create(config: Config) -> None:
    original = interface_details(config.device)
    if config.local.address not in local_addresses(config.device):
        raise Error(f"{config.local.address} is not assigned to {config.device}")
    route = route_get(config)
    if route.get("dev") != config.device:
        raise Error(
            f"the selected flow currently routes through {route.get('dev')}, not {config.device}",
        )
    ensure_unused(config)
    save_state(config)
    alias = f"hummbwtester:{config.name}:{config.token}"
    mtu = str(original["mtu"])
    try:
        run([
            "ip", "link", "add", config.out_device,
            "type", "veth", "peer", "name", config.in_device,
        ])
        for device, address in ((config.out_device, config.out_address),
                                (config.in_device, config.in_address)):
            run(["ip", "link", "set", "dev", device, "alias", alias])
            run(["ip", "link", "set", "dev", device, "mtu", mtu])
            run(["ip", "address", "add", address, "dev", device])
            run(["ip", "link", "set", "dev", device, "up"])
        run(["tc", "qdisc", "add", "dev", config.out_device, "root", "tbf",
             "rate", config.rate, "burst", config.burst, "limit", config.limit])
        run(["sysctl", "-q", "-w", f"net.ipv4.conf.{config.in_device}.accept_local=1"])
        run(["sysctl", "-q", "-w", f"net.ipv4.conf.{config.in_device}.rp_filter=0"])
        # Forward only packets reinjected through this private veth. Global IP forwarding and all
        # pre-existing interfaces retain their settings.
        run(["sysctl", "-q", "-w", f"net.ipv4.conf.{config.in_device}.forwarding=1"])
        run(["ip", "-4", "route", "add", "table", str(config.table),
             f"{config.remote.address}/32", "via", config.in_address.split("/")[0],
             "dev", config.out_device])
        run(["ip", "-4", "rule", "add", "pref", str(config.rule_priority),
             "fwmark", config.mark_spec, "lookup", str(config.table)])
        iptables_rule("mangle", "PREROUTING", clear_mark_rule(config), "-I")
        iptables_rule("filter", "FORWARD", forward_rule(config), "-I")
        # Install this last: it is the operation that starts diverting live traffic.
        iptables_rule("mangle", "OUTPUT", output_mark_rule(config), "-I")
    except BaseException:
        destroy(config, verify_alias=False)
        raise


def destroy(config: Config, *, verify_alias: bool = True) -> None:
    errors = []

    def attempt(arguments: list[str], *, absent_ok: bool = False) -> None:
        result = run(arguments, check=False, capture=True)
        if result.returncode and not absent_ok:
            errors.append(f"{shlex.join(arguments)}: {result.stderr.strip()}")

    try:
        delete_rule(config, "mangle", "OUTPUT", output_mark_rule(config))
        delete_rule(config, "filter", "FORWARD", forward_rule(config))
        delete_rule(config, "mangle", "PREROUTING", clear_mark_rule(config))
    except subprocess.SubprocessError as err:
        errors.append(str(err))
    attempt(["ip", "-4", "rule", "del", "pref", str(config.rule_priority),
             "fwmark", config.mark_spec, "lookup", str(config.table)], absent_ok=True)
    attempt(["ip", "-4", "route", "flush", "table", str(config.table)], absent_ok=True)
    if run(["ip", "link", "show", "dev", config.out_device], check=False,
           capture=True).returncode == 0:
        expected = f"hummbwtester:{config.name}:{config.token}"
        if verify_alias and interface_alias(config.out_device) != expected:
            errors.append(f"refusing to delete {config.out_device}: ownership alias does not match")
        else:
            attempt(["ip", "link", "delete", "dev", config.out_device])
    if not errors:
        state_path(config.name).unlink(missing_ok=True)
        try:
            STATE_DIRECTORY.rmdir()
        except OSError:
            pass
    else:
        raise Error("cleanup was incomplete:\n" + "\n".join(errors))


def status(config: Config) -> int:
    report: dict[str, object] = {"name": config.name, "flow": f"{config.local} -> {config.remote}"}
    links = {}
    for device in (config.out_device, config.in_device):
        result = run(["ip", "-j", "-details", "link", "show", "dev", device],
                     check=False, capture=True)
        links[device] = json.loads(result.stdout)[0] if result.returncode == 0 else None
    report["links"] = links
    report["rule"] = output(["ip", "-4", "rule", "show"])
    result = run(["tc", "-j", "-s", "qdisc", "show", "dev", config.out_device],
                 check=False, capture=True)
    report["qdisc"] = json.loads(result.stdout) if result.returncode == 0 else None
    print(json.dumps(report, indent=2))
    return 0


def add_common(parser: argparse.ArgumentParser) -> None:
    parser.add_argument("--name", default="humm-br2")
    parser.add_argument("--device", required=True, help="original egress interface")
    parser.add_argument("--out-device", default="hummbr2o", help="TBF side of the veth pair")
    parser.add_argument("--in-device", default="hummbr2i", help="reinjection side of the veth pair")
    parser.add_argument("--local", required=True, type=Endpoint.parse, metavar="IP:PORT")
    parser.add_argument("--remote", required=True, type=Endpoint.parse, metavar="IP:PORT")
    parser.add_argument("--veth-network", default="169.254.254.0/30")
    parser.add_argument("--rate", type=validate_tc, default="10mbit")
    parser.add_argument("--burst", type=validate_tc, default="50kb")
    parser.add_argument("--limit", type=validate_tc, default="256kb")
    parser.add_argument("--mark", type=lambda value: int(value, 0), default=0x48554232)
    parser.add_argument("--table", type=int, default=50001)
    parser.add_argument("--rule-priority", type=int, default=10001)


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
            config = load_state(args.name)
            destroy(config)
            return 0
    except (Error, OSError, ValueError, subprocess.SubprocessError) as err:
        print(f"error: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
