"""Contract test for the functions other tools import; see README.md, "Reuse from other tools".

A rename or a changed parameter list fails here, not in the other tool's live experiment.
"""

import dataclasses
import inspect
import unittest

from tools.hummbwtester.scripts import orchestration
from tools.hummbwtester.scripts import ssh_orchestration as remote
from tools.hummbwtester.scripts import ssh_setup

# module, function, its parameter names in order
FUNCTIONS = [
    (orchestration, "patch_router_configs", ["router"]),
    (orchestration, "compose_data", []),
    (orchestration, "inter_as_router_peers", ["compose"]),
    (orchestration, "patch_compose", ["compose", "peers", "tc"]),
    (orchestration, "monitoring_compose", ["args", "quiet", "compose", "project"]),
    (orchestration, "ensure_docker_qdiscs", ["compose", "tc", "peers"]),
    (orchestration, "remove_docker_qdiscs", []),
    (orchestration, "border_router_targets", []),
    (orchestration, "wait_for_scion", ["source_ia", "destination", "timeout"]),
    (orchestration, "parse_marketplace_config", ["marketplace", "kind"]),
    (remote, "load_deployment", ["root", "participants", "relay_ports"]),
    (remote, "load_marketplace", ["root"]),
    (remote, "preflight", ["host", "dry_run", "required_tools"]),
    (remote, "ssh_command", ["host", "command", "check", "kwargs"]),
    (remote, "scp_to", ["host", "local", "remote"]),
    (ssh_setup, "ensure_marketplace", ["inventory", "dry_run"]),
    (ssh_setup, "obtain_marketplace_jwt", ["inventory"]),
    (ssh_setup, "ensure_marketplace_notes", ["inventory", "dry_run"]),
    (ssh_setup, "stop_marketplace", ["inventory"]),
    (ssh_setup, "ensure_selective_qdiscs", ["inventory", "tc", "dry_run"]),
    (ssh_setup, "remove_selective_qdiscs", ["inventory", "tc"]),
    (ssh_setup, "copy_if_changed", ["host", "local", "target", "mode", "dry_run"]),
    (ssh_setup, "print_manual_steps", ["action", "steps", "dry_run"]),
    (ssh_setup, "prometheus_documents", ["targets", "stack"]),
    (ssh_setup, "ensure_prometheus", ["inventory", "targets", "dry_run", "stack"]),
    (ssh_setup, "stop_prometheus", ["host", "stack"]),
    (ssh_setup, "ensure_tunnels", ["specs", "forward", "dry_run", "stack"]),
    (ssh_setup, "stop_tunnels", ["stack"]),
    (ssh_setup, "ensure_grafana", ["forward", "dry_run", "stack"]),
    (ssh_setup, "client_metric_sources", ["inventory", "clients"]),
    (ssh_setup, "router_metric_sources", ["inventory", "first_port"]),
    (ssh_setup, "target_documents_for", ["inventory", "sources", "target_files"]),
    (ssh_setup, "tunnel_specs_for", ["inventory", "sources"]),
]

# module, dataclass, its field names
DATACLASSES = [
    (remote, "SSHHost", ["name", "alias", "sciond", "run_dir", "readiness_command",
                         "static_info", "control_service"]),
    (remote, "Inventory", ["hosts", "server_host", "client_hosts", "prometheus_host",
                           "local_port_base", "routers", "shaping", "marketplace",
                           "local_prometheus_port", "participant_hosts"]),
    (ssh_setup, "MonitoringStack", [
        "prometheus_config", "remote_dir", "prometheus_container", "prometheus_project",
        "remote_prometheus_port", "config_hash_label", "grafana_container", "grafana_compose",
        "grafana_project", "tunnel_state_dir", "tunnel_manifest"]),
    (ssh_setup, "MetricSource", ["host", "address", "port", "labels", "target_file"]),
    (ssh_setup, "TunnelSpec", ["source_alias", "prometheus_alias", "port", "source_address"]),
]


class ReuseAPITest(unittest.TestCase):
    def test_function_signatures(self):
        for module, name, parameters in FUNCTIONS:
            with self.subTest(function=f"{module.__name__}.{name}"):
                function = getattr(module, name)
                self.assertEqual(list(inspect.signature(function).parameters), parameters)

    def test_dataclass_fields(self):
        for module, name, fields in DATACLASSES:
            with self.subTest(dataclass=f"{module.__name__}.{name}"):
                cls = getattr(module, name)
                self.assertEqual([field.name for field in dataclasses.fields(cls)], fields)

    def test_default_monitoring_stack_is_hummbwtesters(self):
        stack = ssh_setup.HUMMBWTESTER_STACK
        self.assertEqual(stack.prometheus_container, ssh_setup.PROMETHEUS_CONTAINER)
        self.assertEqual(stack.remote_prometheus_port, ssh_setup.PROMETHEUS_PORT)
        self.assertEqual(stack.manifest, ssh_setup.TUNNEL_MANIFEST)


if __name__ == "__main__":
    unittest.main()
