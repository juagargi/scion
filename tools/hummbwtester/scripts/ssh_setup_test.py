from dataclasses import replace
import json
from pathlib import Path
import shlex
import socket
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

from tools.hummbwtester.scripts import orchestration
from tools.hummbwtester.scripts import ssh_orchestration as remote
from tools.hummbwtester.scripts import ssh_setup


class SSHSetupTest(unittest.TestCase):
    def inventory(self):
        hosts = {
            "source": remote.SSHHost(
                "source", "source-alias", "127.0.0.1:30255", "/var/tmp/humm", None,
            ),
            "monitor": remote.SSHHost(
                "monitor", "monitor-alias", "127.0.0.1:30255", "/var/tmp/humm", None,
            ),
        }
        return remote.Inventory(
            hosts=hosts,
            server_host="monitor",
            client_hosts={"client-1": "source"},
            prometheus_host="monitor",
            local_port_base=19090,
            routers=(remote.RouterMetrics(
                "monitor", "127.0.0.1:30442", {"br": "br-1"},
            ),),
            shaping=(),
            marketplace=self.marketplace(),
        )

    def clients(self):
        return [SimpleNamespace(client_id="client-1", metrics_port=9090)]

    def test_remote_sources_are_relayed_and_prometheus_host_sources_scraped_directly(self):
        inventory = self.inventory()  # Prometheus and the router run on monitor.
        documents = ssh_setup.target_documents(inventory, self.clients())
        self.assertIn('"127.0.0.1:19090"', documents["targets/clients.json"])
        self.assertIn('"127.0.0.1:30442"', documents["targets/border_routers.json"])
        self.assertEqual(ssh_setup.tunnel_specs(inventory, self.clients()), [ssh_setup.TunnelSpec(
            "source-alias", "monitor-alias", 19090, "127.0.0.1:9090",
        )])

    def test_remote_prometheus_listens_on_loopback_only(self):
        documents, _ = ssh_setup.prometheus_documents({})
        self.assertIn("--web.listen-address=127.0.0.1:8090", documents["docker-compose.yml"])

    def test_prometheus_forward_is_a_further_ssh_control_master(self):
        specs = [ssh_setup.TunnelSpec("source-alias", "monitor-alias", 19090, "127.0.0.1:9090")]
        forward = ssh_setup.PrometheusForward("monitor-alias", 18090, ("127.0.0.1", "10.200.0.1"))
        completed = subprocess.CompletedProcess([], 0, "", "")
        with tempfile.TemporaryDirectory() as directory, \
             mock.patch.object(ssh_setup, "TUNNEL_STATE_DIR", Path(directory)), \
             mock.patch.object(ssh_setup, "TUNNEL_MANIFEST", Path(directory) / "manifest.json"), \
             mock.patch.object(ssh_setup, "_check_local_port_free"), \
             mock.patch.object(ssh_setup.subprocess, "run", return_value=completed) as run, \
             mock.patch("builtins.print"):
            ssh_setup.ensure_tunnels(specs, forward)
            manifest = json.loads((Path(directory) / "manifest.json").read_text())
        last = run.call_args_list[-1].args[0]
        self.assertEqual(len(manifest["ssh_control_masters"]), 3)
        self.assertEqual(last[last.index("--") + 1], "monitor-alias")
        self.assertEqual([last[i + 1] for i, value in enumerate(last) if value == "-L"],
                         ["127.0.0.1:18090:127.0.0.1:8090", "10.200.0.1:18090:127.0.0.1:8090"])

    def test_prometheus_forward_reports_a_busy_controller_port(self):
        with socket.socket() as busy:
            busy.bind(("127.0.0.1", 0))
            busy.listen()
            forward = ssh_setup.PrometheusForward("a", busy.getsockname()[1], ("127.0.0.1",))
            with self.assertRaisesRegex(remote.SSHError, "local_prometheus_port"):
                ssh_setup._check_local_port_free(forward)

    def test_prometheus_is_unchanged_when_files_and_container_are_current(self):
        inventory = self.inventory()
        targets = ssh_setup.target_documents(inventory, self.clients())
        with mock.patch.object(ssh_setup.remote, "ssh_command"), \
             mock.patch.object(
                 ssh_setup, "_remote_prometheus_files_current", return_value=True,
             ), mock.patch.object(
                 ssh_setup, "_prometheus_container_current", return_value=True,
             ), mock.patch.object(ssh_setup.remote, "scp_to") as scp, mock.patch.object(
                 ssh_setup, "stop_prometheus",
             ) as stop:
            ssh_setup.ensure_prometheus(inventory, targets)
        scp.assert_not_called()
        stop.assert_not_called()

    def test_stale_prometheus_files_restart_the_container(self):
        inventory = self.inventory()
        targets = ssh_setup.target_documents(inventory, self.clients())
        completed = subprocess.CompletedProcess([], 0, "", "")
        with mock.patch.object(ssh_setup.remote, "ssh_command", return_value=completed) as run, \
             mock.patch.object(
                 ssh_setup, "_remote_prometheus_files_current", return_value=False,
             ), mock.patch.object(
                 ssh_setup, "_prometheus_container_current", return_value=True,
             ), mock.patch.object(ssh_setup.remote, "scp_to") as scp, mock.patch.object(
                 ssh_setup, "stop_prometheus",
             ) as stop:
            ssh_setup.ensure_prometheus(inventory, targets)
        stop.assert_called_once_with(inventory.hosts["monitor"])
        self.assertEqual(scp.call_count, 4)
        self.assertTrue(any("docker compose --progress quiet" in call.args[1]
                            and "up -d --force-recreate" in call.args[1]
                            for call in run.call_args_list))

    def test_stop_prometheus_refuses_foreign_container(self):
        owner = subprocess.CompletedProcess([], 0, "other/prometheus", "")
        with mock.patch.object(ssh_setup.remote, "ssh_command", return_value=owner) as run:
            with self.assertRaisesRegex(remote.SSHError, "refusing to stop"):
                ssh_setup.stop_prometheus(self.inventory().hosts["monitor"])
        run.assert_called_once()

    def test_tunnel_setup_is_idempotent_and_uses_control_sockets(self):
        specs = [ssh_setup.TunnelSpec(
            "source-alias", "monitor-alias", 19090, "127.0.0.1:9090",
        )]
        completed = subprocess.CompletedProcess([], 0, "", "")
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory)
            manifest = state / "manifest.json"
            with mock.patch.object(ssh_setup, "TUNNEL_STATE_DIR", state), \
                 mock.patch.object(ssh_setup, "TUNNEL_MANIFEST", manifest), \
                 mock.patch.object(
                     ssh_setup.subprocess, "run", return_value=completed,
                 ) as run:
                ssh_setup.ensure_tunnels(specs)
                first_calls = list(run.call_args_list)
                ssh_setup.ensure_tunnels(specs)
                second_calls = run.call_args_list[len(first_calls):]
                ssh_setup.stop_tunnels()
        self.assertEqual(len(first_calls), 2)
        self.assertIn("-L", first_calls[0].args[0])
        self.assertIn("-R", first_calls[1].args[0])
        self.assertEqual(len(second_calls), 2)
        self.assertTrue(all("check" in call.args[0] for call in second_calls))

    def marketplace(self, **changes):
        return replace(orchestration.MarketplaceConfig(
            "https://127.0.0.1:8888/", "alice", "MARKETPLACE_PASSWORD", None, "monitor",
            "[1-ff00:0:110,127.0.0.1]:31888",
        ), **changes)

    def test_marketplace_password_is_checked_before_opening_the_tunnel(self):
        with mock.patch.dict(ssh_setup.os.environ, {}, clear=True), \
             mock.patch.object(ssh_setup.subprocess, "run") as run:
            with self.assertRaisesRegex(orchestration.ConfigError, "MARKETPLACE_PASSWORD"):
                ssh_setup.obtain_marketplace_jwt(self.inventory())
        run.assert_not_called()

    @mock.patch.dict(ssh_setup.os.environ, {"MARKETPLACE_PASSWORD": "secret"})
    def test_marketplace_on_host_is_reached_through_a_temporary_tunnel(self):
        inventory = self.inventory()
        completed = subprocess.CompletedProcess([], 0, "", "")
        with mock.patch.object(ssh_setup, "_free_local_port", return_value=18888), \
             mock.patch.object(ssh_setup.workload, "obtain_marketplace_jwt",
                               return_value="jwt") as obtain, \
             mock.patch.object(ssh_setup.subprocess, "run", return_value=completed) as run:
            jwt = ssh_setup.obtain_marketplace_jwt(inventory)
        self.assertEqual(jwt, "jwt")
        self.assertEqual(obtain.call_args.args[0].url, "https://127.0.0.1:18888/")
        start, stop = (call.args[0] for call in run.call_args_list)
        self.assertEqual(start[start.index("-L") + 1], "127.0.0.1:18888:127.0.0.1:8888")
        self.assertEqual(start[-1], "monitor-alias")
        self.assertEqual(stop[stop.index("-O") + 1], "exit")

    @mock.patch.dict(ssh_setup.os.environ, {"MARKETPLACE_PASSWORD": "secret"})
    def test_marketplace_tunnel_is_closed_when_login_fails(self):
        inventory = self.inventory()
        completed = subprocess.CompletedProcess([], 0, "", "")
        with mock.patch.object(ssh_setup, "_free_local_port", return_value=18888), \
             mock.patch.object(ssh_setup.workload, "obtain_marketplace_jwt",
                               side_effect=orchestration.ConfigError("login failed")), \
             mock.patch.object(ssh_setup.subprocess, "run", return_value=completed) as run:
            with self.assertRaisesRegex(orchestration.ConfigError, "on monitor"):
                ssh_setup.obtain_marketplace_jwt(inventory)
        self.assertIn("exit", run.call_args_list[-1].args[0])

    def test_marketplace_note_is_advertised_on_participant_and_marketplace_hosts(self):
        hosts = {
            **self.inventory().hosts,
            "market": remote.SSHHost("market", "market-alias", "127.0.0.1:30255",
                                     "/var/tmp/humm", None, "/etc/scion/staticInfoConfig.json",
                                     "scion-control@cs-1.service"),
        }
        inventory = replace(self.inventory(), hosts=hosts, marketplace=self.marketplace(
            host="market", scion_address="[1-ff00:0:110,127.0.0.1]:31888",
        ))
        changed = subprocess.CompletedProcess([], 0, '{"changed": true}\n', "")
        current = subprocess.CompletedProcess([], 0, '{"changed": false}\n', "")
        # Hosts are visited in sorted order: market changes, monitor and source are current.
        with mock.patch.object(ssh_setup.remote, "ssh_command",
                               side_effect=[changed, current, current]) as run:
            manual = ssh_setup.ensure_marketplace_notes(inventory)
        helper = run.call_args_list[0]
        # Only the helper runs; no service is restarted on any host.
        self.assertEqual([call.args[0].name for call in run.call_args_list],
                         ["market", "monitor", "source"])
        self.assertFalse(any("systemctl" in call.args[1] for call in run.call_args_list))
        argv = shlex.split(helper.args[1])
        self.assertEqual(argv[:5], ["sudo", "-n", "python3", "-", "ensure"])
        self.assertEqual(json.loads(argv[argv.index("--entry") + 1]), {
            "name": "hummbwtester",
            "api_protocol": "connectrpc/TLS/QUIC/SCION",
            "api_address": "[1-ff00:0:110,127.0.0.1]:31888",
            "client_registration_website": "https://127.0.0.1:8888/",
        })
        self.assertEqual(helper.kwargs["input"], ssh_setup.STATIC_INFO_HELPER.read_text())
        self.assertEqual(len(manual), 1)
        self.assertIn("on market", manual[0])
        self.assertIn("ssh -t market-alias sudo systemctl restart scion-control@cs-1.service",
                      manual[0])

    def test_manual_steps_are_numbered_or_reported_as_none(self):
        with mock.patch("builtins.print") as output:
            ssh_setup.print_manual_steps("setup", ["restart a", "restart b"])
            ssh_setup.print_manual_steps("setup", [])
        lines = [call.args[0] for call in output.call_args_list]
        self.assertEqual(lines, [
            "setup: the following steps must be done manually:",
            "  1. restart a",
            "  2. restart b",
            "setup: no manual steps are required",
        ])

    def test_marketplace_note_is_skipped_without_scion_address(self):
        with mock.patch.object(ssh_setup.remote, "ssh_command") as run:
            ssh_setup.ensure_marketplace_notes(replace(
                self.inventory(), marketplace=self.marketplace(scion_address=None),
            ))
        run.assert_not_called()

    def test_static_info_helper_failure_is_reported(self):
        failed = subprocess.CompletedProcess([], 1, "", "error: the Note field is not JSON")
        inventory = replace(self.inventory(), marketplace=self.marketplace(
            scion_address="[1-ff00:0:110,127.0.0.1]:31888",
        ))
        with mock.patch.object(ssh_setup.remote, "ssh_command", return_value=failed):
            with self.assertRaisesRegex(remote.SSHError, "not JSON"):
                ssh_setup.ensure_marketplace_notes(inventory)

    def fake_hosts(self, commands, diagnose='{"result": "ready", "warnings": []}'):
        """An SSH stand-in that records commands and answers as unconfigured hosts would."""
        topology = json.dumps({
            "isd_as": "1-ff00:0:110", "dispatched_ports": "30000-32767",
            "border_routers": {"br1": {"interfaces": {"1": {}, "2": {}}}},
        })

        def run(host, command, check=True, **kwargs):
            commands.append((host.name, command))
            if command.startswith("command -v"):
                return subprocess.CompletedProcess([], 0, "", "")
            if command.startswith("cat /etc/scion/topology.json"):
                return subprocess.CompletedProcess([], 0, topology, "")
            if command.startswith("printf"):
                return subprocess.CompletedProcess([], 0, "sciera:sciera", "")
            if "master0.key" in command:
                return subprocess.CompletedProcess([], 0, "11" * 16 + "\n", "")
            if "sha256sum" in command or command.startswith("test") \
                    or command.startswith("docker inspect") or "is-active" in command:
                return subprocess.CompletedProcess([], 1, "", "")
            if "hummbwtester-qdisc" in command:
                return subprocess.CompletedProcess([], 44, "", "")
            if " diagnose " in command:
                return subprocess.CompletedProcess([], 0, diagnose, "")
            if " ensure " in command:
                return subprocess.CompletedProcess([], 0, '{"changed": true}', "")
            return subprocess.CompletedProcess([], 0, "", "")
        return run

    def test_dry_run_setup_checks_everything_and_changes_nothing(self):
        shape = remote.ShapedFlow("source", "source-peer", "ens192", "10.6.7.1:50001",
                                  "10.6.7.2:50001", "mq")
        inventory = replace(self.inventory(), shaping=(shape,), marketplace=self.marketplace(
            scion_address="[1-ff00:0:110,127.0.0.1]:31888",
        ))
        clients = [SimpleNamespace(client_id="client-1", metrics_port=9090, hummingbird=True)]
        config = (None, clients, {}, {"rate": "10mbit", "burst": "50kb", "limit": "1mb"})
        commands = []
        with tempfile.TemporaryDirectory() as directory, \
             mock.patch.object(ssh_setup, "TUNNEL_MANIFEST", Path(directory) / "manifest.json"), \
             mock.patch.object(ssh_setup.workload, "require_built_binary"), \
             mock.patch.object(ssh_setup, "MARKETPLACE_BIN", Path(__file__)), \
             mock.patch.object(ssh_setup.remote, "sha256", return_value="digest"), \
             mock.patch.object(ssh_setup.remote, "ssh_command",
                               side_effect=self.fake_hosts(commands)), \
             mock.patch.object(ssh_setup, "obtain_marketplace_jwt", return_value="jwt"), \
             mock.patch.object(ssh_setup.remote, "scp_to") as scp, \
             mock.patch.object(ssh_setup, "write_local_targets") as write_targets, \
             mock.patch.object(ssh_setup, "docker_bridge_gateway", return_value="10.200.0.1"), \
             mock.patch.object(ssh_setup, "_check_local_port_free"), \
             mock.patch.object(ssh_setup.workload, "local_container_owner", return_value=None), \
             mock.patch.object(ssh_setup.subprocess, "run") as local_run, \
             mock.patch("builtins.print") as output:
            self.assertEqual(
                ssh_setup.setup(Path("config.json"), config, inventory, dry_run=True), 0)
        scp.assert_not_called()
        write_targets.assert_not_called()
        local_run.assert_not_called()  # no ssh-control-master is started or stopped
        mutating = ("install", "mv -f", "chmod", "rm -", "--force-recreate", "docker rm",
                    "systemctl start", "systemctl stop", "daemon-reload", "ln -s", "mktemp",
                    " up ", " down ")
        for host, command in commands:
            self.assertFalse(any(word in command for word in mutating), (host, command))
        self.assertTrue(any(" diagnose " in command for _, command in commands))
        notes = [command for _, command in commands if " ensure " in command]
        self.assertTrue(notes and all(command.endswith("--dry-run") for command in notes))
        lines = [str(call.args[0]) for call in output.call_args_list if call.args]
        for expected in ("would create /var/tmp/humm on monitor",
                         "would deploy hummbwtester to source",
                         "would upload marketplace JWT to source",
                         "would install selective qdisc source-peer on source",
                         "would advertise marketplace",
                         "would create hummbwtester-prometheus",
                         "would start 1 Prometheus metric relays (2 ssh-control-masters) and "
                         "forward controller port 8090 to Prometheus",
                         "would start Grafana http://127.0.0.1:3000 with Prometheus at "
                         "controller port 8090",
                         "would install marketplace binary /usr/local/bin/hummingbird-marketplace",
                         "would start hummingbird-marketplace.service on monitor with a rebuilt",
                         "dry-run: setup would require these manual steps:"):
            self.assertTrue(any(expected in line for line in lines), expected)
        self.assertEqual(lines[-1], orchestration.DRY_RUN_NOTICE)

    def test_dry_run_continues_after_a_failed_step_and_returns_failure(self):
        shape = remote.ShapedFlow("source", "source-peer", "ens192", "10.6.7.1:50001",
                                  "10.6.7.2:50001", "mq")
        inventory = replace(self.inventory(), shaping=(shape,))
        clients = [SimpleNamespace(client_id="client-1", metrics_port=9090, hummingbird=True)]
        config = (None, clients, {}, {"rate": "10mbit", "burst": "50kb", "limit": "1mb"})
        commands = []
        with tempfile.TemporaryDirectory() as directory, \
             mock.patch.object(ssh_setup, "TUNNEL_MANIFEST", Path(directory) / "manifest.json"), \
             mock.patch.object(ssh_setup.workload, "require_built_binary"), \
             mock.patch.object(ssh_setup, "MARKETPLACE_BIN", Path(__file__)), \
             mock.patch.object(ssh_setup.remote, "sha256", return_value="digest"), \
             mock.patch.object(ssh_setup.remote, "ssh_command",
                               side_effect=self.fake_hosts(commands)), \
             mock.patch.object(ssh_setup, "obtain_marketplace_jwt",
                               side_effect=orchestration.ConfigError("cannot reach marketplace")), \
             mock.patch("builtins.print") as output:
            self.assertEqual(
                ssh_setup.setup(Path("config.json"), config, inventory, dry_run=True), 1)
        # The qdisc and Prometheus checks still ran after the marketplace login failed.
        self.assertTrue(any(" diagnose " in command for _, command in commands))
        self.assertTrue(any("docker info" in command for _, command in commands))
        lines = [str(call.args[0]) for call in output.call_args_list if call.args]
        self.assertIn("  - 3. marketplace JWT: cannot reach marketplace", lines)
        self.assertEqual(lines[-1], orchestration.DRY_RUN_NOTICE)

    def test_real_setup_stops_at_the_first_failed_step(self):
        clients = [SimpleNamespace(client_id="client-1", metrics_port=9090, hummingbird=True)]
        config = (None, clients, {}, {"rate": "10mbit", "burst": "50kb", "limit": "1mb"})
        with mock.patch.object(ssh_setup.workload, "require_built_binary"), \
             mock.patch.object(ssh_setup.remote, "preflight"), \
             mock.patch.object(ssh_setup, "deploy_binary"), \
             mock.patch.object(ssh_setup, "ensure_marketplace"), \
             mock.patch.object(ssh_setup, "obtain_marketplace_jwt",
                               side_effect=orchestration.ConfigError("cannot reach marketplace")), \
             mock.patch.object(ssh_setup, "ensure_selective_qdiscs") as qdiscs:
            with self.assertRaisesRegex(orchestration.ConfigError, "cannot reach"):
                ssh_setup.setup(Path("config.json"), config, self.inventory())
        qdiscs.assert_not_called()

    def test_dry_run_reports_a_qdisc_that_could_not_be_installed(self):
        shape = remote.ShapedFlow("source", "source-peer", "ens192", "10.6.7.1:50001",
                                  "10.6.7.2:50001", "mq")
        inventory = replace(self.inventory(), shaping=(shape,))
        commands = []
        diagnose = '{"result": "warnings", "warnings": ["root qdisc is noqueue, not expected mq"]}'
        with mock.patch.object(ssh_setup.remote, "ssh_command",
                               side_effect=self.fake_hosts(commands, diagnose)):
            with self.assertRaisesRegex(remote.SSHError, "could not be installed: root qdisc"):
                ssh_setup.ensure_selective_qdiscs(
                    inventory, {"rate": "10mbit", "burst": "50kb", "limit": "1mb"}, dry_run=True,
                )

    def test_tunneled_url_keeps_path_and_brackets_ipv6(self):
        self.assertEqual(
            ssh_setup._tunneled_url("https://[fd00::1]/market?x=1", 18888),
            ("[fd00::1]:443", "https://127.0.0.1:18888/market?x=1"),
        )

    def test_matching_active_selective_qdisc_is_left_untouched(self):
        shape = remote.ShapedFlow(
            "source", "source-peer", "ens192", "10.6.7.1:50001",
            "10.6.7.2:50001", "mq",
        )
        inventory = replace(self.inventory(), shaping=(shape,))
        config = ssh_setup.selective_config(
            shape, {"rate": "10mbit", "burst": "50kb", "limit": "1mb"},
        )
        with mock.patch.object(ssh_setup, "copy_if_changed"), \
             mock.patch.object(ssh_setup, "_qdisc_state", return_value=ssh_setup.asdict(config)), \
             mock.patch.object(ssh_setup, "_qdisc_is_active", return_value=True), \
             mock.patch.object(ssh_setup.remote, "ssh_command") as run:
            ssh_setup.ensure_selective_qdiscs(
                inventory, {"rate": "10mbit", "burst": "50kb", "limit": "1mb"},
            )
        run.assert_not_called()

    def test_changed_selective_qdisc_is_replaced(self):
        shape = remote.ShapedFlow(
            "source", "source-peer", "ens192", "10.6.7.1:50001",
            "10.6.7.2:50001", "mq",
        )
        inventory = replace(self.inventory(), shaping=(shape,))
        completed = subprocess.CompletedProcess([], 0, "", "")
        with mock.patch.object(ssh_setup, "copy_if_changed"), \
             mock.patch.object(ssh_setup, "_qdisc_state", return_value={"rate": "1mbit"}), \
             mock.patch.object(ssh_setup.remote, "ssh_command", return_value=completed) as run:
            ssh_setup.ensure_selective_qdiscs(
                inventory, {"rate": "10mbit", "burst": "50kb", "limit": "1mb"},
            )
        self.assertEqual(run.call_count, 2)
        self.assertIn(" down ", run.call_args_list[0].args[1])
        self.assertIn(" up ", run.call_args_list[1].args[1])
        self.assertIs(run.call_args_list[1].kwargs["stdout"], subprocess.DEVNULL)


class MarketplaceServiceTest(unittest.TestCase):
    def inventory(self):
        return SSHSetupTest.inventory(SSHSetupTest())

    def ensure(self, *, changed=False, active=True, reachable=(True,), started=True):
        """Run ensure_marketplace with mocked host state; return the mocks of interest."""
        mocks = {}
        with mock.patch.object(ssh_setup, "MARKETPLACE_BIN", Path(__file__)), \
             mock.patch.object(ssh_setup, "_check_marketplace_host"), \
             mock.patch.object(ssh_setup.remote, "ssh_command",
                               return_value=subprocess.CompletedProcess(
                                   [], 0, "sciera:sciera", "")), \
             mock.patch.object(ssh_setup, "install_root_file",
                               side_effect=[changed, False]) as install, \
             mock.patch.object(ssh_setup, "_ensure_directory", return_value=False), \
             mock.patch.object(ssh_setup, "copy_if_changed", return_value=False), \
             mock.patch.object(ssh_setup, "_ensure_symlink", return_value=False), \
             mock.patch.object(ssh_setup, "_service_active", return_value=active), \
             mock.patch.object(ssh_setup, "_remote_test", side_effect=reachable), \
             mock.patch.object(ssh_setup, "marketplace_entries",
                               return_value={"assets": [], "ases": []}) as entries, \
             mock.patch.object(ssh_setup, "_reconfigure_marketplace_db") as database, \
             mock.patch.object(ssh_setup, "_systemctl") as systemctl, \
             mock.patch.object(ssh_setup, "_wait_for", return_value=started) as wait, \
             mock.patch("builtins.print"):
            mocks.update(install=install, entries=entries, database=database,
                         systemctl=systemctl, wait=wait)
            try:
                ssh_setup.ensure_marketplace(self.inventory())
            except remote.SSHError as err:
                mocks["error"] = err
        return mocks

    def test_running_reachable_unchanged_marketplace_is_left_alone(self):
        mocks = self.ensure()
        mocks["systemctl"].assert_not_called()
        mocks["entries"].assert_not_called()
        mocks["database"].assert_not_called()

    def test_replaced_binary_restarts_with_a_rebuilt_database(self):
        mocks = self.ensure(changed=True)
        self.assertEqual([call.args[1] for call in mocks["systemctl"].call_args_list],
                         ["stop", "start"])
        mocks["database"].assert_called_once()
        mocks["wait"].assert_called_once()

    def test_stopped_marketplace_is_started_without_a_stop(self):
        mocks = self.ensure(active=False)
        self.assertEqual([call.args[1] for call in mocks["systemctl"].call_args_list], ["start"])
        mocks["database"].assert_called_once()

    def test_running_but_unreachable_marketplace_is_restarted(self):
        mocks = self.ensure(reachable=(False,))
        self.assertEqual([call.args[1] for call in mocks["systemctl"].call_args_list],
                         ["stop", "start"])

    def test_marketplace_that_never_becomes_reachable_is_an_error(self):
        mocks = self.ensure(active=False, started=False)
        self.assertIn("not reachable after", str(mocks["error"]))
        self.assertIn("journalctl -u hummingbird-marketplace.service", str(mocks["error"]))

    def test_teardown_stops_and_waits_until_unreachable(self):
        with mock.patch.object(ssh_setup, "_service_active", return_value=True), \
             mock.patch.object(ssh_setup, "_systemctl") as systemctl, \
             mock.patch.object(ssh_setup, "_wait_for", return_value=True) as wait, \
             mock.patch("builtins.print"):
            ssh_setup.stop_marketplace(self.inventory())
        systemctl.assert_called_once_with(self.inventory().hosts["monitor"], "stop")
        self.assertIs(wait.call_args.args[2], False)
        with mock.patch.object(ssh_setup, "_service_active", return_value=False), \
             mock.patch.object(ssh_setup, "_wait_for", return_value=False):
            with self.assertRaisesRegex(remote.SSHError, "still reachable"):
                ssh_setup.stop_marketplace(self.inventory())

    def test_unit_runs_as_the_ssh_user_and_can_be_neither_enabled_nor_restarted(self):
        unit = ssh_setup.marketplace_unit("sciera", "sciera")
        self.assertIn("User=sciera\nGroup=sciera\n", unit)
        self.assertIn("Restart=no\n", unit)
        self.assertNotIn("[Install]", unit)
        self.assertIn("ExecStart=/usr/local/bin/hummingbird-marketplace "
                      "--config /etc/scion/marketplace/marketplace.toml", unit)

    def test_config_binds_both_apis_to_the_configured_loopback_addresses(self):
        config = ssh_setup.marketplace_toml(SSHSetupTest.marketplace(SSHSetupTest()))
        self.assertIn('config_dir = "/etc/scion/marketplace"', config)
        self.assertIn('api_addr = "127.0.0.1:8888"', config)
        self.assertIn('scion_api_addr = "127.0.0.1:31888"', config)
        self.assertIn('connection = "/var/lib/scion/marketplace/marketplace.db"', config)

    def test_database_entries_use_secret_values_derived_on_the_hosts(self):
        commands = []
        fake = SSHSetupTest.fake_hosts(SSHSetupTest(), commands)
        with mock.patch.object(ssh_setup.remote, "ssh_command", side_effect=fake):
            entries = ssh_setup.marketplace_entries(self.inventory())
        self.assertEqual({entry["key"] for entry in entries["delegations"]}, {"11" * 16})
        self.assertEqual([user["name"] for user in entries["users"]], ["alice", "bob"])
        self.assertTrue(entries["assets"])
        key_reads = [command for _, command in commands if "master0.key" in command]
        self.assertTrue(key_reads and all(command.startswith("sudo -n python3 -c")
                                          for command in key_reads))


class GrafanaTest(unittest.TestCase):
    forward = ssh_setup.PrometheusForward("monitor-alias", 18090, ("127.0.0.1", "10.200.0.1"))

    def ensure(self, owner, state):
        with mock.patch.object(ssh_setup.workload, "local_container_owner", return_value=owner), \
             mock.patch.object(ssh_setup, "_grafana_state", return_value=state), \
             mock.patch.object(ssh_setup.subprocess, "run") as run, \
             mock.patch.object(ssh_setup, "_wait_http", return_value=True) as wait, \
             mock.patch.dict(ssh_setup.os.environ, {}, clear=True), \
             mock.patch("builtins.print"):
            ssh_setup.ensure_grafana(self.forward)
        return run, wait

    def test_running_grafana_for_this_prometheus_is_left_alone(self):
        run, wait = self.ensure("monitoring/grafana", (True, "18090"))
        run.assert_not_called()
        self.assertEqual([call.args[0] for call in wait.call_args_list], [
            "http://127.0.0.1:3000/api/health", "http://10.200.0.1:18090/-/ready",
        ])

    def test_missing_or_differently_configured_grafana_is_started_without_prometheus(self):
        for owner, state in ((None, (False, None)), ("monitoring/grafana", (True, "8090"))):
            with self.subTest(owner=owner, state=state):
                run, _ = self.ensure(owner, state)
                self.assertEqual(run.call_args.args[0][-4:], ["up", "-d", "--no-deps", "grafana"])
                self.assertEqual(run.call_args.args[0][1:4], ["compose", "--progress", "quiet"])
                self.assertEqual(run.call_args.kwargs["env"]["PROMETHEUS_PORT"], "18090")

    def test_foreign_grafana_container_is_refused(self):
        with mock.patch.object(ssh_setup.workload, "local_container_owner",
                               return_value="other/grafana"):
            with self.assertRaisesRegex(remote.SSHError, "refusing to replace"):
                ssh_setup.ensure_grafana(self.forward)


if __name__ == "__main__":
    unittest.main()
