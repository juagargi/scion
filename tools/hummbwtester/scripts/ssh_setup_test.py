from dataclasses import replace
from pathlib import Path
import subprocess
import tempfile
from types import SimpleNamespace
import unittest
from unittest import mock

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
        )

    def clients(self):
        return [SimpleNamespace(client_id="client-1", metrics_port=9090)]

    def test_targets_and_tunnels_use_prometheus_loopback_ports(self):
        inventory = self.inventory()
        documents = ssh_setup.target_documents(inventory, self.clients())
        self.assertIn('"127.0.0.1:19090"', documents["targets/clients.json"])
        self.assertIn('"127.0.0.1:19091"', documents["targets/border_routers.json"])

        specs = ssh_setup.tunnel_specs(inventory, self.clients())
        self.assertEqual(specs[0], ssh_setup.TunnelSpec(
            "source-alias", "monitor-alias", 19090, "127.0.0.1:9090",
        ))
        self.assertEqual(specs[1], ssh_setup.TunnelSpec(
            "monitor-alias", "monitor-alias", 19091, "127.0.0.1:30442",
        ))

    def test_prometheus_is_unchanged_when_files_and_container_are_current(self):
        inventory = self.inventory()
        targets = ssh_setup.target_documents(inventory, self.clients())
        with mock.patch.object(ssh_setup.remote, "ssh_command"), \
             mock.patch.object(
                 ssh_setup, "_remote_prometheus_files_current", return_value=True,
             ), mock.patch.object(
                 ssh_setup, "_prometheus_container_current", return_value=True,
             ), mock.patch.object(ssh_setup.remote, "scp_to") as scp, \
             mock.patch.object(ssh_setup, "stop_prometheus") as stop:
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
             ), mock.patch.object(ssh_setup.remote, "scp_to") as scp, \
             mock.patch.object(ssh_setup, "stop_prometheus") as stop:
            ssh_setup.ensure_prometheus(inventory, targets)
        stop.assert_called_once_with(inventory.hosts["monitor"])
        self.assertEqual(scp.call_count, 4)
        self.assertTrue(any("up -d --force-recreate" in call.args[1]
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


if __name__ == "__main__":
    unittest.main()
