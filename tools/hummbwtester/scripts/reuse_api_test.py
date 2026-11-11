"""Contract test for the functions other tools import; see README.md, "Reuse from other tools".

A rename or a changed parameter list fails here, not in the other tool's live experiment.
ReuseBehaviorTest covers what those tools rely on beyond signatures, with another tool's stack.
"""

import dataclasses
import inspect
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

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
    (orchestration, "obtain_marketplace_jwt", ["marketplace", "url"]),
    (orchestration, "write_private_jwt", ["token"]),
    (orchestration, "install_docker_jwt", ["token", "services"]),
    (orchestration, "remove_docker_jwt", ["services", "remote"]),
    (orchestration, "marketplace_registration_website", []),
    (orchestration, "local_container_owner", ["container"]),
    (orchestration, "tester_service", ["ia"]),
    (orchestration, "join_host_port", ["host", "port"]),
    (orchestration, "dc_args", ["args"]),
    (orchestration, "run", ["command", "check", "kwargs"]),
    (remote, "load_deployment", ["root", "participants", "relay_ports"]),
    (remote, "load_marketplace", ["root"]),
    (remote, "preflight", ["host", "dry_run", "required_tools"]),
    (remote, "ssh_command", ["host", "command", "check", "kwargs"]),
    (remote, "scp_to", ["host", "local", "remote"]),
    (remote, "setup_dir", ["host"]),
    (remote, "jwt_path", ["host"]),
    (remote, "check_clocks", ["hosts"]),
    (ssh_setup, "setup_hosts", ["inventory"]),
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
    (ssh_setup, "prometheus_forward", ["inventory"]),
    (ssh_setup, "ensure_tunnels", ["specs", "forward", "dry_run", "stack"]),
    (ssh_setup, "stop_tunnels", ["stack"]),
    (ssh_setup, "ensure_grafana", ["forward", "dry_run", "stack"]),
    (ssh_setup, "client_metric_sources", ["inventory", "clients"]),
    (ssh_setup, "router_metric_sources", ["inventory", "first_port"]),
    (ssh_setup, "target_documents_for", ["inventory", "sources", "target_files"]),
    (ssh_setup, "tunnel_specs_for", ["inventory", "sources"]),
    (ssh_setup, "agent_specs_for", ["inventory", "sources"]),
    (ssh_setup, "central_sources", ["inventory", "sources"]),
    (ssh_setup, "agent_tunnel_specs", ["inventory", "agents", "forward_port"]),
    (ssh_setup, "ensure_agents", ["inventory", "agents", "dry_run", "stack"]),
    (ssh_setup, "check_agents_delivering", ["inventory", "agents", "stack"]),
    (ssh_setup, "stop_agents", ["inventory", "agents", "stack"]),
    (ssh_setup, "agent_delivery", ["host", "stack"]),
]

# module, dataclass, its field names
DATACLASSES = [
    (orchestration, "MarketplaceConfig", ["username", "password_env", "sub_account", "host",
                                          "scion_address", "interfaces", "location_override"]),
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
    (ssh_setup, "PrometheusForward", ["alias", "port", "binds"]),
    (ssh_setup, "AgentSpec", ["host", "sources", "write_port"]),
    (ssh_setup, "AgentDelivery", ["newest_scraped", "newest_sent", "failed"]),
]

# module, class, its properties (not dataclass fields, so not covered by DATACLASSES)
PROPERTIES = [
    (orchestration, "MarketplaceConfig", ["url"]),
    (ssh_setup, "MonitoringStack", ["manifest"]),
    (ssh_setup, "AgentDelivery", ["backlog"]),
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

    def test_properties(self):
        for module, name, properties in PROPERTIES:
            cls = getattr(module, name)
            for prop in properties:
                with self.subTest(property=f"{module.__name__}.{name}.{prop}"):
                    self.assertIsInstance(inspect.getattr_static(cls, prop), property)

    def test_constants(self):
        self.assertIsInstance(orchestration.ROOT, Path)
        self.assertIsInstance(orchestration.GEN, Path)
        self.assertEqual(orchestration.COMPOSE, orchestration.GEN / "scion-dc.yml")
        self.assertIsInstance(remote.DEFAULT_REQUIRED_TOOLS, tuple)
        self.assertTrue(all(isinstance(tool, str) for tool in remote.DEFAULT_REQUIRED_TOOLS))
        self.assertIsInstance(remote.SSH_OPTIONS, list)
        self.assertTrue(all(isinstance(option, str) for option in remote.SSH_OPTIONS))
        self.assertEqual(ssh_setup.SCION_CONFIG_DIR, "/etc/scion")
        # Dashboards (hummbwtester's and other tools') select routers with display!="false".
        self.assertEqual(remote.DISPLAY_LABEL, "display")

    def test_default_monitoring_stack_is_hummbwtesters(self):
        stack = ssh_setup.HUMMBWTESTER_STACK
        self.assertEqual(stack.prometheus_container, ssh_setup.PROMETHEUS_CONTAINER)
        self.assertEqual(stack.remote_prometheus_port, ssh_setup.PROMETHEUS_PORT)
        self.assertEqual(stack.manifest, ssh_setup.TUNNEL_MANIFEST)


# Another tool's own Prometheus configuration: its job names and file-SD files, not
# hummbwtester's.
OTHER_PROMETHEUS_CONFIG = """\
global:
  scrape_interval: 1s
scrape_configs:
  - job_name: other-runner
    file_sd_configs:
      - files: [/etc/prometheus/targets/runner.json]
  - job_name: other-hosts
    file_sd_configs:
      - files: [/etc/prometheus/targets/hosts.json]
"""

COMPLETED = subprocess.CompletedProcess([], 0, "", "")


class ReuseBehaviorTest(unittest.TestCase):
    """What other tools rely on beyond signatures, with a stack that is not hummbwtester's."""

    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        config = Path(directory.name) / "prometheus.yml"
        config.write_text(OTHER_PROMETHEUS_CONFIG)
        self.stack = ssh_setup.MonitoringStack(
            config, "/var/tmp/other-prometheus", "other-prometheus", "other-ssh", 8091,
            "org.other.config-sha256", "other-grafana", Path(directory.name) / "compose.yml",
            "other-monitoring", Path(directory.name) / "tunnels",
        )
        hosts = {name: remote.SSHHost(name, f"{name}-alias", "127.0.0.1:30255", "/var/tmp/x", None)
                 for name in ("monitor", "source")}
        self.inventory = remote.Inventory(
            hosts=hosts, server_host="", client_hosts={}, prometheus_host="monitor",
            local_port_base=19090, routers=(), shaping=(), marketplace=None,
        )
        self.controller = ssh_setup.MetricSource(
            None, "127.0.0.1:9000", 19090, {"job": "runner"}, "targets/runner.json")
        self.on_monitor = ssh_setup.MetricSource(
            "monitor", "127.0.0.1:9001", 19091, {}, "targets/hosts.json")
        self.on_source = [
            ssh_setup.MetricSource("source", "127.0.0.1:9002", 19092, {}, "targets/hosts.json"),
            ssh_setup.MetricSource("source", "127.0.0.1:9003", 19093, {}, "targets/hosts.json"),
        ]
        self.sources = [self.controller, self.on_monitor, *self.on_source]

    def test_controller_sources_stay_central_and_relayed(self):
        self.assertEqual(ssh_setup.central_sources(self.inventory, self.sources),
                         [self.controller, self.on_monitor])
        self.assertIn(ssh_setup.TunnelSpec(None, "monitor-alias", 19090, "127.0.0.1:9000"),
                      ssh_setup.tunnel_specs_for(self.inventory, self.sources))

    def test_agent_writes_on_the_relay_port_of_its_hosts_first_source(self):
        self.assertEqual(ssh_setup.agent_specs_for(self.inventory, self.sources),
                         [ssh_setup.AgentSpec("source", tuple(self.on_source), 19092)])

    def test_agent_scrapes_with_the_stacks_own_configuration(self):
        agent, = ssh_setup.agent_specs_for(self.inventory, self.sources)
        documents, _ = ssh_setup.agent_documents(agent, self.stack)
        config = ssh_setup.yaml.safe_load(documents["prometheus.yml"])
        other = ssh_setup.yaml.safe_load(OTHER_PROMETHEUS_CONFIG)
        self.assertEqual(config["scrape_configs"], other["scrape_configs"])
        self.assertEqual(json.loads(documents["targets/runner.json"]), [])
        self.assertEqual([entry["targets"] for entry in
                          json.loads(documents["targets/hosts.json"])],
                         [["127.0.0.1:9002"], ["127.0.0.1:9003"]])
        compose = documents["docker-compose.yml"]
        for expected in ("container_name: other-prometheus-agent",
                         "--web.listen-address=127.0.0.1:8091",
                         "source: /var/tmp/other-prometheus/prometheus.yml\n",
                         "source: /var/tmp/other-prometheus/targets\n"):
            self.assertIn(expected, compose)
        self.assertNotIn("hummbwtester", compose)

    def test_agent_lifecycle_uses_the_stacks_names(self):
        agents = ssh_setup.agent_specs_for(self.inventory, self.sources)
        commands, uploads, owners, deliveries = [], [], [], []

        def owned(host, docker, container, expected):
            owners.append((container, expected))
            return True

        def delivery(host, stack=None):
            deliveries.append(stack)
            return ssh_setup.AgentDelivery(100, 100, 0)

        with mock.patch.object(ssh_setup, "_docker_command", return_value="docker"), \
             mock.patch.object(ssh_setup.remote, "ssh_command",
                               side_effect=lambda host, command, **kw: commands.append(command)
                               or COMPLETED), \
             mock.patch.object(ssh_setup.remote, "scp_to",
                               side_effect=lambda host, local, target: uploads.append(target)), \
             mock.patch.object(ssh_setup, "_remote_documents_current", return_value=False), \
             mock.patch.object(ssh_setup, "_container_current", return_value=True), \
             mock.patch.object(ssh_setup, "_owned_container_exists", side_effect=owned), \
             mock.patch.object(ssh_setup, "agent_delivery", side_effect=delivery), \
             mock.patch("builtins.print"):
            ssh_setup.ensure_agents(self.inventory, agents, stack=self.stack)
            ssh_setup.check_agents_delivering(self.inventory, agents, stack=self.stack)
            ssh_setup.stop_agents(self.inventory, agents, stack=self.stack)

        self.assertTrue(uploads)
        self.assertTrue(all(target.startswith("/var/tmp/other-prometheus/") for target in uploads))
        self.assertIn("docker compose --progress quiet --project-name other-ssh "
                      "-f /var/tmp/other-prometheus/docker-compose.yml up -d --force-recreate",
                      commands)
        self.assertIn("docker rm -f other-prometheus-agent >/dev/null", commands)
        self.assertIn("docker volume rm -f other-ssh_agent-data >/dev/null", commands)
        self.assertIn("rm -rf /var/tmp/other-prometheus", commands)
        self.assertEqual(set(owners), {("other-prometheus-agent", "other-ssh/agent")})
        self.assertTrue(deliveries)
        self.assertTrue(all(stack is self.stack for stack in deliveries))
        self.assertFalse(any("hummbwtester" in command for command in commands))

    def test_agent_delivery_asks_the_stacks_port_and_is_none_without_answer(self):
        host = self.inventory.hosts["source"]
        unreachable = subprocess.CompletedProcess([], 1, "", "connection refused")
        with mock.patch.object(ssh_setup.remote, "ssh_command",
                               return_value=unreachable) as ssh:
            self.assertIsNone(ssh_setup.agent_delivery(host, self.stack))
        self.assertIn("http://127.0.0.1:8091/metrics", ssh.call_args.args[1])
        self.assertIs(ssh.call_args.kwargs["check"], False)  # no SSHError for a silent agent

    def test_central_prometheus_always_receives_remote_write(self):
        for stack in (None, self.stack):
            with self.subTest(stack=stack and stack.prometheus_container):
                documents, _ = ssh_setup.prometheus_documents({}, stack)
                self.assertIn("--web.enable-remote-write-receiver",
                              documents["docker-compose.yml"])

    def test_hidden_routers_are_labeled_display_false(self):
        inventory = dataclasses.replace(self.inventory, routers=(
            remote.RouterMetrics("monitor", "127.0.0.1:30442", {"br": "br-1"}),
            remote.RouterMetrics("monitor", "127.0.0.1:30443", {"br": "br-2"}, display=False),
        ))
        self.assertEqual([source.labels for source in
                          ssh_setup.router_metric_sources(inventory, 19100)],
                         [{"br": "br-1"}, {"br": "br-2", "display": "false"}])

    def test_check_clocks_names_every_failing_host(self):
        hosts = [remote.SSHHost(name, f"{name}-alias", "127.0.0.1:30255", "/var/tmp/x", None)
                 for name in ("good", "unsynced", "drifted", "silent")]
        offsets = [(0.0, 0.1), (0.0, 0.1), (7.3, 0.1), remote.SSHError("no answer")]
        with mock.patch.object(remote, "_ntp_synchronized",
                               side_effect=[True, True, False, True, True]), \
             mock.patch.object(remote, "clock_offset", side_effect=offsets), \
             mock.patch("builtins.print"):
            with self.assertRaises(remote.SSHError) as raised:
                remote.check_clocks(hosts)
        message = str(raised.exception)
        for name in ("unsynced", "drifted", "silent"):
            self.assertIn(f"{name}: ", message)
        self.assertNotIn("good: ", message)


if __name__ == "__main__":
    unittest.main()
