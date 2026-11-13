#!/usr/bin/env python3
"""Single configuration and command surface for Docker and SSH experiments."""

from __future__ import annotations

import argparse
from dataclasses import dataclass
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
LOCAL_GRAFANA_CONTAINER = "hummbwtester-grafana"


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
            planner.check_docker_marketplace_location(clients)
        return ExperimentPlan(path, config, kind, None)
    inventory = ssh.load_inventory(path, server, clients)
    return ExperimentPlan(path, config, kind, inventory)


_compose = planner.monitoring_compose


def _check_local_prometheus_owner() -> bool:
    return planner.check_local_owner(LOCAL_PROMETHEUS_CONTAINER, LOCAL_PROMETHEUS_PROJECT,
                                     "prometheus")


def _check_local_grafana_owner() -> bool:
    return planner.check_local_owner(LOCAL_GRAFANA_CONTAINER, LOCAL_PROMETHEUS_PROJECT, "grafana")


def _wait_monitoring() -> None:
    prometheus = f"http://127.0.0.1:{os.environ.get('PROMETHEUS_PORT', '8090')}/-/ready"
    grafana = f"http://127.0.0.1:{os.environ.get('GRAFANA_PORT', '3000')}/api/health"
    planner.wait_service(LOCAL_PROMETHEUS_CONTAINER, prometheus)
    planner.wait_service(LOCAL_GRAFANA_CONTAINER, grafana)
    print(f"Prometheus {prometheus.rsplit('/-/', 1)[0]} and Grafana "
          f"{grafana.rsplit('/api/', 1)[0]} are running")


def setup_local(plan: ExperimentPlan, dry_run: bool = False) -> int:
    exists = _check_local_prometheus_owner()
    _check_local_grafana_owner()
    if dry_run:
        steps = planner.describe_setup(plan.path, plan.config)
        steps.append(
            f"would run docker compose --project-name {LOCAL_PROMETHEUS_PROJECT} up -d "
            "prometheus grafana, which "
            + ("keeps the running monitoring containers that are up to date" if exists
               else "starts the local hummbwtester-prometheus and hummbwtester-grafana containers"),
        )
        steps.append("would check that both containers keep running and answer on "
                     "Prometheus /-/ready and Grafana /api/health")
        print("dry-run: docker setup would perform these steps:")
        for index, step in enumerate(steps, start=1):
            print(f"  {index}. {step}")
        print(planner.DRY_RUN_NOTICE)
        return 0
    result = planner.setup(plan.path, plan.config)
    # up -d keeps running containers, starts stopped ones, and recreates a Grafana configured
    # for another Prometheus port.
    subprocess.run(_compose("up", "-d", "prometheus", "grafana"), check=True, text=True)
    _wait_monitoring()
    return result


def teardown_local() -> int:
    services = [service for service, present in (
        ("prometheus", _check_local_prometheus_owner()), ("grafana", _check_local_grafana_owner()),
    ) if present]
    if services:
        subprocess.run(_compose("rm", "--stop", "--force", *services), check=True, text=True)
    planner.remove_docker_qdiscs()
    return 0


def run_local(plan: ExperimentPlan) -> int:
    return planner.run_experiment(plan.path, plan.config)


def show_informative(config_path: Path) -> None:
    """Print the configuration's informative lines, framed so they stand out from the log."""
    lines = planner.informative_lines(planner.read_json(config_path))
    if lines:
        print("\n".join(lines))
        print()


def execute(action: str, config_path: Path, dry_run: bool = False) -> int:
    plan = load_plan(config_path, action)
    if action == "setup":
        show_informative(config_path)
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
        status = execute(args.action, args.config, args.dry_run)
    except (planner.ConfigError, ssh.SSHError, ssh_setup.selective_qdisc.Error,
            argparse.ArgumentTypeError, subprocess.SubprocessError,
            RuntimeError, OSError, ValueError) as err:
        print(f"error: {err}", file=sys.stderr)
        status = 1
    if args.action == "setup":
        # Setup prints a lot; the last line says how it ended.
        if status == 0:
            print("setup: done.")
        else:
            print("setup: FAILED.", file=sys.stderr)
    return status


if __name__ == "__main__":
    raise SystemExit(main())
