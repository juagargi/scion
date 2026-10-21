#!/usr/bin/env python3
"""Single configuration and command surface for Docker and SSH experiments."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
import json
import os
from pathlib import Path
import subprocess
import sys

try:
    from . import orchestration as planner
    from . import ssh_orchestration as ssh
    from . import ssh_setup
except ImportError:
    import orchestration as planner
    import ssh_orchestration as ssh
    import ssh_setup


LOCAL_PROMETHEUS_PROJECT = planner.MONITORING_PROJECT
LOCAL_PROMETHEUS_CONTAINER = "hummbwtester-prometheus"


@dataclass(frozen=True)
class ExperimentPlan:
    path: Path
    config: tuple[planner.Endpoint, list[planner.Client], dict[str, int], dict[str, str]]
    kind: str
    inventory: ssh.Inventory | None


def load_plan(path: Path, action: str) -> ExperimentPlan:
    config = planner.load_config(path)
    server, clients, _, _ = config
    kind = planner.read_json(path)["deployment"]["kind"]
    if kind == "docker":
        if action != "teardown":
            planner.validate_endpoints(planner.compose_data(), server, clients)
        return ExperimentPlan(path, config, kind, None)
    inventory = ssh.load_inventory(path, server, clients)
    return ExperimentPlan(path, config, kind, inventory)


_compose = planner.monitoring_compose


def _local_prometheus_owner() -> str | None:
    return planner.local_container_owner(LOCAL_PROMETHEUS_CONTAINER)


def _check_local_prometheus_owner() -> bool:
    owner = _local_prometheus_owner()
    expected = f"{LOCAL_PROMETHEUS_PROJECT}/prometheus"
    if owner is not None and owner != expected:
        raise RuntimeError(
            f"{LOCAL_PROMETHEUS_CONTAINER} exists but belongs to {owner}, not {expected}",
        )
    return owner is not None


def setup_local(plan: ExperimentPlan, dry_run: bool = False) -> int:
    exists = _check_local_prometheus_owner()
    if dry_run:
        steps = planner.describe_setup(plan.path, plan.config)
        steps.append(
            f"would run docker compose --project-name {LOCAL_PROMETHEUS_PROJECT} up -d prometheus, "
            + (f"which keeps the running {LOCAL_PROMETHEUS_CONTAINER} container" if exists
               else f"which starts the local {LOCAL_PROMETHEUS_CONTAINER} container"),
        )
        print("dry-run: docker setup would perform these steps:")
        for index, step in enumerate(steps, start=1):
            print(f"  {index}. {step}")
        print(planner.DRY_RUN_NOTICE)
        return 0
    result = planner.setup(plan.path, plan.config)
    subprocess.run(_compose("up", "-d", "prometheus"), check=True, text=True)
    return result


def teardown_local() -> int:
    if _check_local_prometheus_owner():
        subprocess.run(_compose("rm", "--stop", "--force", "prometheus"), check=True,
                       text=True)
    try:
        peers = json.loads(planner.DOCKER_QDISC_STATE.read_text())
    except FileNotFoundError:
        return 0
    if isinstance(peers, dict) and "peers" in peers:
        peers = peers["peers"]
    if not isinstance(peers, dict) or not all(
        isinstance(router, str) and isinstance(addresses, list)
        and all(isinstance(address, str) for address in addresses)
        for router, addresses in peers.items()
    ):
        raise RuntimeError(f"invalid qdisc manifest: {planner.DOCKER_QDISC_STATE}")
    for router, addresses in peers.items():
        running = subprocess.run(planner.dc_args("ps", "-q", router), cwd=planner.ROOT,
                                 check=True, capture_output=True, text=True)
        if not running.stdout.strip():
            continue  # A stopped container has no network namespace or qdisc to remove.
        subprocess.run(planner.dc_args(
            "run", "--rm", "--no-deps", planner.tc_helper_name(router), "cleanup", *addresses,
        ), cwd=planner.ROOT, check=True, text=True)
    planner.DOCKER_QDISC_STATE.unlink()
    return 0


def run_local(plan: ExperimentPlan) -> int:
    # The generated local marketplace account uses this development-only password.
    hummingbird_clients = [client for client in plan.config[1] if client.hummingbird]
    marketplace = hummingbird_clients[0].marketplace if hummingbird_clients else None
    if marketplace is not None and marketplace.password_env == "HUMMBWTESTER_MARKETPLACE_PASSWORD":
        os.environ.setdefault("HUMMBWTESTER_MARKETPLACE_PASSWORD", "1234")
    return planner.run_experiment(plan.path, plan.config)


def execute(action: str, config_path: Path, dry_run: bool = False) -> int:
    plan = load_plan(config_path, action)
    if plan.kind == "docker":
        if action == "setup":
            return setup_local(plan, dry_run)
        if action == "run":
            return run_local(plan)
        return teardown_local()
    # plan.kind is ssh:
    assert plan.inventory is not None
    if action == "setup":
        return ssh_setup.setup(plan.path, plan.config, plan.inventory, dry_run)
    if action == "run":
        return ssh.run_experiment(plan.path, plan.config, plan.inventory)
    return ssh_setup.teardown(plan.path, plan.config, plan.inventory)


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("action", choices=("setup", "run", "teardown"))
    parser.add_argument("--config", required=True, type=Path)
    parser.add_argument(
        "--dry-run", action="store_true",
        help="setup only: run every check and read, report what would change, change nothing",
    )
    args = parser.parse_args()
    if args.dry_run and args.action != "setup":
        parser.error("--dry-run is only supported with setup")
    try:
        return execute(args.action, args.config, args.dry_run)
    except (planner.ConfigError, ssh.SSHError, ssh_setup.selective_qdisc.Error,
            argparse.ArgumentTypeError, subprocess.SubprocessError,
            RuntimeError, OSError, ValueError) as err:
        print(f"error: {err}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
