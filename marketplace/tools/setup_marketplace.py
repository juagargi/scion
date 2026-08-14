#!/usr/bin/env python3

"""Adds a marketplace instance to an already generated SCION topology.

Both backends are supported, and the one to use is detected the same way
scion.sh does it: gen/scion-dc.yml means the topology runs on docker, and the
marketplace becomes a compose service; otherwise it is a supervisord program.

The script is idempotent: running it again for the same IA reports what is
already in place instead of adding a second marketplace.
"""

import argparse
import json
import re
import sys
from collections import namedtuple
from pathlib import Path

import yaml

# <ISD>-<AS>, with the AS either as a hex triple (underscores or colons, the
# form used by gen/AS<as_id>) or as a plain decimal (BGP-style) AS number.
IA_RE = re.compile(
    r"^(?P<isd>\d{1,5})-"
    r"(?P<as>[0-9a-fA-F]{1,4}[_:][0-9a-fA-F]{1,4}[_:][0-9a-fA-F]{1,4}|\d{1,10})$"
)

MAX_ISD = (1 << 16) - 1
MAX_BGP_AS = (1 << 32) - 1

# The port of both APIs, the TCP one serving the ConnectRPC API and the web app
# from a single mux, and the SCION/QUIC one, which is UDP. They do not collide.
# It has to be inside the dispatched_ports range of the AS, otherwise only the
# shim dispatcher can deliver SCION packets to it.
MARKETPLACE_PORT = 31888

# Where the marketplace of a docker topology finds its files inside the container.
CONTAINER_CONFIG_DIR = "/etc/scion"
CONTAINER_DB = "/share/cache/marketplace.db"
CONTAINER_TOML = f"{CONTAINER_CONFIG_DIR}/marketplace.toml"

DOCKER_CONF = "scion-dc.yml"
DEFAULT_IMAGE = "scion/marketplace:latest"

# What the marketplace binds, and what staticInfoConfig.json advertises for it.
Endpoints = namedtuple("Endpoints", "host api_port scion_port")

MARKETPLACE_TOML = """[general]
id = "marketplace"
config_dir = "{config_dir}"

[log.console]
level = "debug"

[marketplace]
api_addr = "{api_addr}"
scion_api_addr = "{scion_api_addr}"
currency = "CHF"
currency_exponent = 2
supports_redemption_delegation = true
delegation_hourly_fee = 10000
transaction_fee_relative = 0.01
transaction_fee_absolute = 10
split_combine_fee_absolute = 10

[marketplace_db]
connection = "{db_path}"
"""

MARKETPLACE_PROGRAM = """[program:marketplace]
autostart = false
autorestart = false
environment = TZ=UTC,GODEBUG="cgocheck=0"
stdout_logfile = logs/marketplace.log
redirect_stderr = True
startretries = 0
startsecs = 5
priority = 100
command = bin/marketplace --config {config}

"""

# The [program:marketplace] section, i.e. up to the next section or EOF.
PROGRAM_SECTION_RE = re.compile(
    r"^\[program:marketplace\]\s*$.*?(?=^\[|\Z)",
    re.MULTILINE | re.DOTALL,
)
PROGRAM_CONFIG_RE = re.compile(r"^command\s*=.*?--config\s+(?P<config>\S+)", re.MULTILINE)

# The marketplace service of an AS, e.g. marketplace1-ff00_0_111.
SERVICE_RE = re.compile(r"marketplace(?P<isd>\d+)-(?P<as>[0-9a-fA-F_]+)")


class SetupError(Exception):
    """An error that aborts the setup with a message, but without a traceback."""


def parseIA(ia_string: str):
    """Splits an IA into (isd, as_id), with the AS id in the gen/ underscore form."""
    match = IA_RE.match(ia_string.strip())
    if match is None:
        raise SetupError(
            f"invalid ISD-AS {ia_string!r}; expected e.g. 1-ff00_0_111, "
            "1-ff00:0:111 or 1-64512"
        )
    isd = int(match.group("isd"))
    if isd < 1 or isd > MAX_ISD:
        raise SetupError(f"invalid ISD in {ia_string!r}: must be in [1, {MAX_ISD}]")
    as_id = match.group("as")
    if as_id.isdigit() and int(as_id) > MAX_BGP_AS:
        raise SetupError(f"invalid AS number in {ia_string!r}: must be <= {MAX_BGP_AS}")
    return isd, as_id.replace(":", "_")


def hostPort(host: str, port: int) -> str:
    """host:port, with an IPv6 host in brackets."""
    if ":" in host:
        return f"[{host}]:{port}"
    return f"{host}:{port}"


def marketplaceEntries(ia_colons: str, endpoints: Endpoints):
    """The staticInfoConfig entries advertising this marketplace.

    They describe what the marketplace really binds: the web app is served by the
    same mux as the TCP API, so both live on the API port.
    """
    website = f"https://{hostPort(endpoints.host, endpoints.api_port)}"
    return [
        {
            "name": "Test Market",
            "protocol": "connectrpc/TLS/QUIC/SCION",
            "api": f"[{ia_colons},{endpoints.host}]:{endpoints.scion_port}",
            "website": website,
        },
        {
            "name": "Test Market",
            "protocol": "connectrpc/TLS/TCP",
            "api": website,
            "website": website,
        },
    ]


def marketplaceToml(config_dir: str, endpoints: Endpoints, db_path: str) -> str:
    return MARKETPLACE_TOML.format(
        config_dir=config_dir,
        api_addr=hostPort(endpoints.host, endpoints.api_port),
        scion_api_addr=hostPort(endpoints.host, endpoints.scion_port),
        db_path=db_path,
    )


def checkDispatchedPorts(marketplace_dir: Path, port: int) -> None:
    """Checks that the SCION API port is one the border router delivers directly."""
    topology = marketplace_dir / "topology.json"
    if not topology.is_file():
        return
    try:
        ports = json.loads(topology.read_text(encoding="utf-8")).get("dispatched_ports")
    except (OSError, json.JSONDecodeError) as e:
        raise SetupError(f"cannot read {topology}: {e}")
    if not ports or ports == "all":
        return
    match = re.fullmatch(r"(\d+)-(\d+)", str(ports).strip())
    if match is None:
        return
    start, end = int(match.group(1)), int(match.group(2))
    if not start <= port <= end:
        print("Warning: "
        f"the SCION API port {port} is outside the dispatched_ports range "
            f"{ports} of {topology}; the marketplace will only receive SCION "
            "packets through a shim dispatcher")


def loadStaticInfo(path: Path):
    """Reads staticInfoConfig.json and its embedded note object.

    Returns (static_info, note), both dicts. Missing files yield empty ones.
    """
    if not path.exists():
        return {}, {}

    try:
        static_info = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as e:
        raise SetupError(f"cannot read {path}: {e}")
    if not isinstance(static_info, dict):
        raise SetupError(f"{path}: expected a JSON object, got {type(static_info).__name__}")

    note = static_info.get("note", "")
    if note == "":
        return static_info, {}
    try:
        note = json.loads(note)
    except (TypeError, json.JSONDecodeError):
        raise SetupError(
            f"{path}: the 'note' field is not a JSON object; refusing to overwrite it"
        )
    if not isinstance(note, dict):
        raise SetupError(
            f"{path}: the 'note' field is not a JSON object; refusing to overwrite it"
        )
    return static_info, note


def mergeEntries(existing, entries):
    """Merges the marketplace entries into the existing ones, keyed by name+protocol."""
    merged = [e for e in existing if isinstance(e, dict)]
    changed = len(merged) != len(existing)
    for entry in entries:
        key = (entry["name"], entry["protocol"])
        for i, old in enumerate(merged):
            if (old.get("name"), old.get("protocol")) == key:
                if old != entry:
                    merged[i] = entry
                    changed = True
                break
        else:
            merged.append(entry)
            changed = True
    return merged, changed


def addToGroup(text: str, group_name: str):
    """Adds the marketplace program to the [group:as<isd>-<as_id>] section."""
    pattern = re.compile(
        rf"(\[group:{re.escape(group_name)}\]\nprograms\s*=\s*)([^\n]+)",
        re.MULTILINE,
    )

    match = pattern.search(text)
    if match is None:
        groups = re.findall(r"^\[group:(.+)\]$", text, re.MULTILINE)
        known = ", ".join(groups) if groups else "none"
        raise SetupError(
            f"no [group:{group_name}] section in the supervisord config "
            f"(known groups: {known})"
        )

    programs = [p.strip() for p in match.group(2).split(",") if p.strip()]
    if "marketplace" in programs:
        return text, False

    programs.append("marketplace")
    return text[:match.start()] + match.group(1) + ",".join(programs) + text[match.end():], True


def planSupervisord(gen_dir: Path, isd: int, as_id: str, as_dir: str,
                    api_port: int, scion_port: int):
    """Plans the marketplace of a supervisord topology.

    Returns the endpoints it will bind, the contents of its marketplace.toml, and
    a function applying the changes to gen/supervisord.conf.
    """
    supervisord_conf = gen_dir / "supervisord.conf"
    group_name = f"as{isd}-{as_id}"  # e.g. as1-ff00_0_111
    config_path = f"{gen_dir / as_dir}/marketplace.toml"

    try:
        config = supervisord_conf.read_text(encoding="utf-8")
    except OSError as e:
        raise SetupError(f"cannot read {supervisord_conf}: {e}")

    # There is a single [program:marketplace]: if it exists, it must be ours.
    section = PROGRAM_SECTION_RE.search(config)
    if section is not None:
        command = PROGRAM_CONFIG_RE.search(section.group(0))
        if command is None:
            raise SetupError(
                f"{supervisord_conf} already has a [program:marketplace] section with an "
                "unrecognized command; remove it before running this script"
            )
        if Path(command.group("config")) != Path(config_path):
            raise SetupError(
                f"{supervisord_conf} already runs a marketplace with "
                f"{command.group('config')}; only one marketplace is supported"
            )

    # Appending to [group:as<isd>-<as_id>] is resolved here, before any change is
    # applied, so that a missing group is not a partial setup.
    config, group_added = addToGroup(config, group_name)

    endpoints = Endpoints("127.0.0.1", api_port, scion_port)
    toml_content = marketplaceToml(str(gen_dir / as_dir), endpoints, "gen-cache/marketplace.db")

    def apply(changes):
        text = config
        if section is None:
            text = MARKETPLACE_PROGRAM.format(config=config_path) + text
            changes.append(f"added [program:marketplace] to {supervisord_conf}")
        if group_added:
            changes.append(f"added marketplace to [group:{group_name}]")
        if section is None or group_added:
            try:
                supervisord_conf.write_text(text, encoding="utf-8")
            except OSError as e:
                raise SetupError(f"cannot write {supervisord_conf}: {e}")

    return endpoints, toml_content, apply


def loadCompose(path: Path):
    """Reads the docker compose file of a generated topology."""
    try:
        compose = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as e:
        raise SetupError(f"cannot read {path}: {e}")
    if not isinstance(compose, dict) or not isinstance(compose.get("services"), dict):
        raise SetupError(f"{path} has no services, it is not a compose file")
    return compose


def controlService(compose, path: Path, isd: int, as_id: str):
    """The control service of an AS, and the address its containers share.

    The marketplace joins the network namespace of the control service, so it ends
    up on the very same address, and only its ports set it apart. The address is
    pinned on the dispatcher, which is the container that owns the namespace.
    """
    services = compose["services"]
    pattern = re.compile(rf"cs{isd}-{re.escape(as_id)}-\d+")
    names = sorted(n for n in services if pattern.fullmatch(n))
    if not names:
        raise SetupError(
            f"{path} has no control service of {isd}-{as_id.replace('_', ':')}; "
            "is that AS part of it?"
        )
    control = names[0]

    dispatcher = f"disp_{control}"
    if dispatcher not in services:
        raise SetupError(f"{path} has no {dispatcher}; regenerate the topology")
    networks = services[dispatcher].get("networks")
    if not isinstance(networks, dict) or len(networks) != 1:
        raise SetupError(f"{path}: expected exactly one network on {dispatcher}")
    (_, addresses), = networks.items()
    for key in ("ipv4_address", "ipv6_address"):
        if key in addresses:
            return control, dispatcher, addresses[key]
    raise SetupError(f"{path}: {dispatcher} has no address of its own")


def marketplaceImage(control):
    """The marketplace image, named like the images of the generated topology."""
    image = control.get("image")
    if not isinstance(image, str) or "/" not in image:
        return DEFAULT_IMAGE
    registry, _, name = image.rpartition("/")
    _, colon, tag = name.partition(":")
    return f"{registry}/marketplace{colon}{tag}"


def composeService(path: Path, as_dir: str, control_name: str, control, dispatcher: str):
    """The compose service running the marketplace of an AS.

    It shares the network namespace of the control service, the way the control
    service and the hummingbird service already share the one of their dispatcher.
    It therefore carries no address of its own, and needs none.
    """
    volumes = []
    for volume in control.get("volumes") or []:
        if not isinstance(volume, str):
            raise SetupError(f"{path}: expected string volumes on {control_name}")
        # Unlike the other services, the marketplace writes into its config dir:
        # its TLS certificate and its JWT signature keys are created on first run.
        volumes.append(volume[:-2] + "rw" if volume.endswith(":ro") else volume)
    if not any(v.endswith(f"/{as_dir}:{CONTAINER_CONFIG_DIR}:rw") for v in volumes):
        raise SetupError(
            f"{path}: {control_name} does not mount {as_dir} at {CONTAINER_CONFIG_DIR}; "
            "regenerate the topology"
        )

    entry = {
        "command": ["--config", CONTAINER_TOML],
        # The marketplace asks the control service for trust material on startup.
        "depends_on": {control_name: {"condition": "service_healthy"}},
        "image": marketplaceImage(control),
        "network_mode": f"service:{dispatcher}",
        "volumes": volumes,
    }
    if "user" in control:
        entry["user"] = control["user"]
    return entry


def planDocker(gen_dir: Path, isd: int, as_id: str, as_dir: str,
               api_port: int, scion_port: int):
    """Plans the marketplace of a docker topology.

    Returns the endpoints it will bind, the contents of its marketplace.toml, and
    a function applying the changes to gen/scion-dc.yml.
    """
    dc_file = gen_dir / DOCKER_CONF
    compose = loadCompose(dc_file)
    services = compose["services"]
    name = f"marketplace{isd}-{as_id}"

    # There is a single marketplace: if one exists, it must be ours.
    for other, service in services.items():
        if other == name:
            continue
        match = SERVICE_RE.fullmatch(other)
        if match is not None:
            raise SetupError(
                f"{dc_file} already runs a marketplace for "
                f"{match.group('isd')}-{match.group('as').replace('_', ':')}; "
                "only one marketplace is supported"
            )
        if CONTAINER_TOML in (service.get("command") or []):
            raise SetupError(
                f"{dc_file} already has a marketplace service named {other}; "
                "remove it before running this script"
            )

    control_name, dispatcher, address = controlService(compose, dc_file, isd, as_id)
    endpoints = Endpoints(address, api_port, scion_port)
    toml_content = marketplaceToml(CONTAINER_CONFIG_DIR, endpoints, CONTAINER_DB)
    entry = composeService(dc_file, as_dir, control_name, services[control_name], dispatcher)

    def apply(changes):
        if services.get(name) == entry:
            return
        verb = "updated" if name in services else "added"
        services[name] = entry
        try:
            dc_file.write_text(yaml.dump(compose, default_flow_style=False), encoding="utf-8")
        except OSError as e:
            raise SetupError(f"cannot write {dc_file}: {e}")
        changes.append(f"{verb} the {name} service of {dc_file}")

    return endpoints, toml_content, apply


def main(args) -> None:
    isd, as_id = parseIA(args.ia)
    ia_colons = f"{isd}-{as_id.replace('_', ':')}"
    as_dir = f"AS{as_id}"            # e.g. ASff00_0_111

    # Paths
    gen_dir = Path(args.gen_dir)
    marketplace_dir = gen_dir / as_dir
    certs_dir = marketplace_dir / "certs"
    marketplace_toml = marketplace_dir / "marketplace.toml"
    static_info_json = marketplace_dir / "staticInfoConfig.json"

    # 0. Validate the topology before touching anything, so that a typo in the
    # IA does not leave a half configured AS behind.
    if not gen_dir.is_dir():
        raise SetupError(f"{gen_dir} does not exist; generate the topology first")
    if not marketplace_dir.is_dir():
        raise SetupError(f"{marketplace_dir} does not exist; {ia_colons} is not part of {gen_dir}")

    # The backend is the one the topology was generated for, detected the way
    # scion.sh does it.
    if (gen_dir / DOCKER_CONF).is_file():
        plan = planDocker
    elif (gen_dir / "supervisord.conf").is_file():
        plan = planSupervisord
    else:
        raise SetupError(
            f"neither {gen_dir / DOCKER_CONF} nor {gen_dir / 'supervisord.conf'} exists; "
            "generate the topology first"
        )

    checkDispatchedPorts(marketplace_dir, args.scion_api_port)
    endpoints, toml_content, apply = plan(
        gen_dir, isd, as_id, as_dir, args.api_port, args.scion_api_port)

    static_info, note = loadStaticInfo(static_info_json)
    hummingbird = note.get("hummingbird", [])
    if not isinstance(hummingbird, list):
        raise SetupError(
            f"{static_info_json}: 'note'.hummingbird is not a list; refusing to overwrite it"
        )

    changes = []

    # 1 & 2. Create directories
    certs_dir.mkdir(parents=True, exist_ok=True)

    # 3. Write marketplace.toml, keeping any local edits to an existing one.
    if not marketplace_toml.exists():
        marketplace_toml.write_text(toml_content, encoding="utf-8")
        changes.append(f"wrote {marketplace_toml}")
    else:
        existing = marketplace_toml.read_text(encoding="utf-8")
        if existing != toml_content:
            print(f"{marketplace_toml} exists and differs from the default, left unchanged")
            bound = re.search(r'^scion_api_addr\s*=\s*"([^"]*)"', existing, re.MULTILINE)
            advertised = hostPort(endpoints.host, endpoints.scion_port)
            if bound is not None and bound.group(1) != advertised:
                print(f"  warning: it binds {bound.group(1)}, but {static_info_json} "
                      f"advertises {advertised}")

    # 4 & 5. Add the marketplace to the topology.
    apply(changes)

    # 6. Advertise the marketplace in staticInfoConfig.json, without dropping
    # whatever else is already configured there.
    hummingbird, changed = mergeEntries(hummingbird, marketplaceEntries(ia_colons, endpoints))
    if changed:
        note["hummingbird"] = hummingbird
        static_info["note"] = json.dumps(note)
        try:
            static_info_json.write_text(json.dumps(static_info, indent=2), encoding="utf-8")
        except OSError as e:
            raise SetupError(f"cannot write {static_info_json}: {e}")
        changes.append(f"advertised the marketplace in {static_info_json}")

    if not changes:
        print(f"Marketplace already configured for {ia_colons}, nothing to do")
        return
    for change in changes:
        print(change)
    print(f"Marketplace config added for {ia_colons}")
    if changed:
        print("The control service reads staticInfoConfig.json on startup: restart the "
              "topology for other ASes to discover the marketplace")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Adds a marketplace to a generated SCION topology."
    )
    parser.add_argument(
        "ia",
        metavar="IA",
        help="ISD-AS hosting the marketplace, e.g. 1-ff00_0_111 or 1-ff00:0:111"
    )
    parser.add_argument(
        "--gen-dir",
        default="gen",
        help="Path to the generated topology directory (default: gen)."
    )
    parser.add_argument(
        "--api-port",
        type=int,
        default=MARKETPLACE_PORT,
        help=f"TCP port of the API and the web app (default: {MARKETPLACE_PORT})."
    )
    parser.add_argument(
        "--scion-api-port",
        type=int,
        default=MARKETPLACE_PORT,
        help=f"UDP port of the SCION API (default: {MARKETPLACE_PORT}). It has to be "
             "inside the dispatched_ports range of the AS."
    )

    try:
        main(parser.parse_args())
    except SetupError as e:
        print(f"error: {e}", file=sys.stderr)
        sys.exit(1)
