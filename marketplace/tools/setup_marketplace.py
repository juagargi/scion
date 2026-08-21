#!/usr/bin/env python3

"""Adds a marketplace instance to an already generated SCION topology.

`topogen -m ISD-AS` (i.e. `./scion.sh topology -m ISD-AS`) does this as part of
generating the topology, and prepopulates the database on top. This script is for
the topologies that are already there: it adds the marketplace to one of their
ASes without regenerating anything. The database is left alone, so
populate_marketplace.py is still needed to fill it.

Both backends are supported, and the one to use is detected the same way scion.sh
does it: gen/scion-dc.yml means the topology runs on docker, and the marketplace
becomes a compose service; otherwise it is a supervisord program.

The script is idempotent: running it again for the same IA reports what is
already in place instead of adding a second marketplace.
"""

import argparse
import configparser
import os
import re
import sys
from io import StringIO
from pathlib import Path

import yaml

# The marketplace is described once by the topology generator,
# so that a marketplace added here is the same one that a generated topology would have.
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "tools"))

# Linters like flake8 would complain about the import not being top-level. Avoid with noqa: e402
from topology.common import TopoID  # noqa: E402
from topology.marketplace import (  # noqa: E402
    CONTAINER_CONFIG_DIR,
    CONTAINER_DB,
    CONTAINER_TOML,
    LOCAL_CACHE_DIR,
    LOCAL_HOST,
    MARKETPLACE_CONFIG_NAME,
    MARKETPLACE_DB_NAME,
    MARKETPLACE_PORT,
    PROGRAM_NAME,
    STATIC_INFO_CONFIG_NAME,
    Endpoints,
    MarketplaceError,
    advertiseMarketplace,
    checkDispatchedPorts,
    dockerService,
    dockerServiceName,
    hostPort,
    marketplaceToml,
    supervisordProgram,
)

DOCKER_CONF = "scion-dc.yml"
SUPERVISOR_CONF = "supervisord.conf"
DEFAULT_IMAGE = "scion/marketplace:latest"

# The [program:marketplace] section, i.e. up to the next section or EOF.
PROGRAM_SECTION_RE = re.compile(
    r"^\[program:%s\]\s*$.*?(?=^\[|\Z)" % PROGRAM_NAME,
    re.MULTILINE | re.DOTALL,
)
PROGRAM_CONFIG_RE = re.compile(r"^command\s*=.*?--config\s+(?P<config>\S+)", re.MULTILINE)

# The marketplace service of an AS, e.g. marketplace1-ff00_0_111.
SERVICE_RE = re.compile(r"marketplace(?P<isd>\d+)-(?P<as>[0-9a-fA-F_]+)")


def parseIA(ia_string: str) -> TopoID:
    try:
        return TopoID(ia_string.strip())
    except ValueError:
        raise MarketplaceError(
            "invalid ISD-AS %r; expected e.g. 1-ff00_0_111, 1-ff00:0:111 or 1-64512"
            % ia_string)


def programSection(config_path: str) -> str:
    """The [program:marketplace] section of a supervisord config, as text."""
    config = configparser.ConfigParser(interpolation=None)
    config["program:%s" % PROGRAM_NAME] = {
        key: str(value) for key, value in supervisordProgram(config_path).items()
    }
    text = StringIO()
    config.write(text)
    return text.getvalue()


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
        raise MarketplaceError(
            f"no [group:{group_name}] section in the supervisord config "
            f"(known groups: {known})"
        )

    programs = [p.strip() for p in match.group(2).split(",") if p.strip()]
    if PROGRAM_NAME in programs:
        return text, False

    programs.append(PROGRAM_NAME)
    return text[:match.start()] + match.group(1) + ",".join(programs) + text[match.end():], True


def planSupervisord(gen_dir: Path, topo_id: TopoID, api_port: int, scion_port: int):
    """Plans the marketplace of a supervisord topology.

    Returns the endpoints it will bind, the contents of its marketplace.toml, and
    a function applying the changes to gen/supervisord.conf.
    """
    supervisord_conf = gen_dir / SUPERVISOR_CONF
    group_name = "as%s" % topo_id.file_fmt()  # e.g. as1-ff00_0_111
    as_base = str(gen_dir / topo_id.AS_file())
    config_path = os.path.join(as_base, MARKETPLACE_CONFIG_NAME)

    try:
        config = supervisord_conf.read_text(encoding="utf-8")
    except OSError as e:
        raise MarketplaceError(f"cannot read {supervisord_conf}: {e}")

    # There is a single [program:marketplace]: if it exists, it must be ours.
    section = PROGRAM_SECTION_RE.search(config)
    if section is not None:
        command = PROGRAM_CONFIG_RE.search(section.group(0))
        if command is None:
            raise MarketplaceError(
                f"{supervisord_conf} already has a [program:{PROGRAM_NAME}] section with an "
                "unrecognized command; remove it before running this script"
            )
        if Path(command.group("config")) != Path(config_path):
            raise MarketplaceError(
                f"{supervisord_conf} already runs a marketplace with "
                f"{command.group('config')}; only one marketplace is supported"
            )

    # Appending to [group:as<isd>-<as_id>] is resolved here, before any change is
    # applied, so that a missing group is not a partial setup.
    config, group_added = addToGroup(config, group_name)

    endpoints = Endpoints(LOCAL_HOST, api_port, scion_port)
    db_path = os.path.join(LOCAL_CACHE_DIR, MARKETPLACE_DB_NAME)
    toml_content = marketplaceToml(as_base, endpoints, db_path)

    def apply(changes):
        text = config
        if section is None:
            text = programSection(config_path) + text
            changes.append(f"added [program:{PROGRAM_NAME}] to {supervisord_conf}")
        if group_added:
            changes.append(f"added {PROGRAM_NAME} to [group:{group_name}]")
        if section is None or group_added:
            try:
                supervisord_conf.write_text(text, encoding="utf-8")
            except OSError as e:
                raise MarketplaceError(f"cannot write {supervisord_conf}: {e}")

    return endpoints, toml_content, apply


def loadCompose(path: Path):
    """Reads the docker compose file of a generated topology."""
    try:
        compose = yaml.safe_load(path.read_text(encoding="utf-8"))
    except (OSError, yaml.YAMLError) as e:
        raise MarketplaceError(f"cannot read {path}: {e}")
    if not isinstance(compose, dict) or not isinstance(compose.get("services"), dict):
        raise MarketplaceError(f"{path} has no services, it is not a compose file")
    return compose


def controlService(compose, path: Path, topo_id: TopoID):
    """The control service of an AS, and the address its containers share.

    The marketplace joins the network namespace of the control service, so it ends
    up on the very same address, and only its ports set it apart. The address is
    pinned on the dispatcher, which is the container that owns the namespace.
    """
    services = compose["services"]
    pattern = re.compile(r"cs%s-\d+" % re.escape(topo_id.file_fmt()))
    names = sorted(n for n in services if pattern.fullmatch(n))
    if not names:
        raise MarketplaceError(
            f"{path} has no control service of {topo_id}; is that AS part of it?")
    control = names[0]

    dispatcher = f"disp_{control}"
    if dispatcher not in services:
        raise MarketplaceError(f"{path} has no {dispatcher}; regenerate the topology")
    networks = services[dispatcher].get("networks")
    if not isinstance(networks, dict) or len(networks) != 1:
        raise MarketplaceError(f"{path}: expected exactly one network on {dispatcher}")
    (_, addresses), = networks.items()
    for key in ("ipv4_address", "ipv6_address"):
        if key in addresses:
            return control, dispatcher, addresses[key]
    raise MarketplaceError(f"{path}: {dispatcher} has no address of its own")


def marketplaceImage(control):
    """The marketplace image, named like the images of the generated topology."""
    image = control.get("image")
    if not isinstance(image, str) or "/" not in image:
        return DEFAULT_IMAGE
    registry, _, name = image.rpartition("/")
    _, colon, tag = name.partition(":")
    return f"{registry}/marketplace{colon}{tag}"


def marketplaceVolumes(path: Path, as_dir: str, control_name: str, control):
    """The volumes of the marketplace, i.e. the ones of its control service.

    Unlike the other services, the marketplace writes into its config dir:
    its TLS certificate and its JWT signature keys are created on first run.
    """
    volumes = []
    for volume in control.get("volumes") or []:
        if not isinstance(volume, str):
            raise MarketplaceError(f"{path}: expected string volumes on {control_name}")
        volumes.append(volume[:-2] + "rw" if volume.endswith(":ro") else volume)
    if not any(v.endswith(f"/{as_dir}:{CONTAINER_CONFIG_DIR}:rw") for v in volumes):
        raise MarketplaceError(
            f"{path}: {control_name} does not mount {as_dir} at {CONTAINER_CONFIG_DIR}; "
            "regenerate the topology"
        )
    return volumes


def planDocker(gen_dir: Path, topo_id: TopoID, api_port: int, scion_port: int):
    """Plans the marketplace of a docker topology.

    Returns the endpoints it will bind, the contents of its marketplace.toml, and
    a function applying the changes to gen/scion-dc.yml.
    """
    dc_file = gen_dir / DOCKER_CONF
    compose = loadCompose(dc_file)
    services = compose["services"]
    name = dockerServiceName(topo_id)

    # There is a single marketplace: if one exists, it must be ours.
    for other, service in services.items():
        if other == name:
            continue
        match = SERVICE_RE.fullmatch(other)
        if match is not None:
            raise MarketplaceError(
                f"{dc_file} already runs a marketplace for "
                f"{match.group('isd')}-{match.group('as').replace('_', ':')}; "
                "only one marketplace is supported"
            )
        if CONTAINER_TOML in (service.get("command") or []):
            raise MarketplaceError(
                f"{dc_file} already has a marketplace service named {other}; "
                "remove it before running this script"
            )

    control_name, dispatcher, address = controlService(compose, dc_file, topo_id)
    control = services[control_name]
    endpoints = Endpoints(address, api_port, scion_port)
    toml_content = marketplaceToml(CONTAINER_CONFIG_DIR, endpoints, CONTAINER_DB)
    entry = dockerService(
        marketplaceImage(control),
        control_name,
        dispatcher,
        marketplaceVolumes(dc_file, topo_id.AS_file(), control_name, control),
        control.get("user"),
    )

    def apply(changes):
        if services.get(name) == entry:
            return
        verb = "updated" if name in services else "added"
        services[name] = entry
        try:
            dc_file.write_text(yaml.dump(compose, default_flow_style=False), encoding="utf-8")
        except OSError as e:
            raise MarketplaceError(f"cannot write {dc_file}: {e}")
        changes.append(f"{verb} the {name} service of {dc_file}")

    return endpoints, toml_content, apply


def main(args) -> None:
    topo_id = parseIA(args.ia)

    # Paths
    gen_dir = Path(args.gen_dir)
    marketplace_dir = gen_dir / topo_id.AS_file()
    certs_dir = marketplace_dir / "certs"
    marketplace_toml = marketplace_dir / MARKETPLACE_CONFIG_NAME
    static_info_json = marketplace_dir / STATIC_INFO_CONFIG_NAME

    # 0. Validate the topology before touching anything, so that a typo in the
    # IA does not leave a half configured AS behind.
    if not gen_dir.is_dir():
        raise MarketplaceError(f"{gen_dir} does not exist; generate the topology first")
    if not marketplace_dir.is_dir():
        raise MarketplaceError(
            f"{marketplace_dir} does not exist; {topo_id} is not part of {gen_dir}")

    # The backend is the one the topology was generated for, detected the way
    # scion.sh does it.
    if (gen_dir / DOCKER_CONF).is_file():
        plan = planDocker
    elif (gen_dir / SUPERVISOR_CONF).is_file():
        plan = planSupervisord
    else:
        raise MarketplaceError(
            f"neither {gen_dir / DOCKER_CONF} nor {gen_dir / SUPERVISOR_CONF} exists; "
            "generate the topology first"
        )

    checkDispatchedPorts(marketplace_dir, args.scion_api_port)
    endpoints, toml_content, apply = plan(
        gen_dir, topo_id, args.api_port, args.scion_api_port)

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
    advertised = advertiseMarketplace(static_info_json, topo_id, endpoints)
    if advertised:
        changes.append(f"advertised the marketplace in {static_info_json}")

    if not changes:
        print(f"Marketplace already configured for {topo_id}, nothing to do")
        return
    for change in changes:
        print(change)
    print(f"Marketplace config added for {topo_id}")
    if advertised:
        print("The control service reads staticInfoConfig.json on startup: restart the "
              "topology for other ASes to discover the marketplace")
    print("Its database is not touched here: run populate_marketplace.py to fill it")


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
    except MarketplaceError as e:
        print(f"error: {e}", file=sys.stderr)
        sys.exit(1)
