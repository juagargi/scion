import json
from pathlib import Path
import shlex
import subprocess
import tempfile
import unittest
from unittest import mock

from tools.hummbwtester.scripts import orchestration
from tools.hummbwtester.scripts import ssh_orchestration as ssh


class InventoryTest(unittest.TestCase):
    def write_json(self, directory, name, value):
        path = Path(directory) / name
        path.write_text(json.dumps(value))
        return path

    def workload(self, marketplace_host="b"):
        return {
            "hummingbird": {
                "reservation_source": "marketplace",
                "marketplace": {
                    "host": marketplace_host, "url": "https://127.0.0.1:8888", "username": "alice",
                    "password_env": "MARKETPLACE_PASSWORD",
                    "scion_address": "[1-ff00:0:111,127.0.0.1]:31888",
                },
            },
            "server": {"node": "a", "isd_as": "1-ff00:0:112", "host": "fd00::1", "port": 12345,
                       "receive_buffer_size": 4096},
            "hummingbird_clients": [{
                "client_id": "hummingbird-1", "node": "b", "isd_as": "1-ff00:0:111",
                "host": "fd00::2", "port": 0, "bandwidth": "1Mbps", "maxburst": "2Mbps",
                "duration": "1m",
                "hummingbird_reservation": {
                    "bandwidth": "1mbps", "duration": "1m", "reverse_bandwidth": "0kbps",
                },
            }],
            "best_effort_clients": [{
                "client_id": "best-effort-1", "node": "a", "isd_as": "1-ff00:0:110",
                "host": "fd00::3", "port": 0, "bandwidth": "1Mbps", "maxburst": "2Mbps",
                "duration": "1m",
            }],
            "deployment": {"kind": "ssh", **self.inventory()},
            "tc": {"rate": "10mbit", "burst": "50kb", "limit": "256kb"},
        }

    def inventory(self):
        return {
            "hosts": {
                "a": {"ssh": "sciera-rnp", "sciond": "127.0.0.1:30255",
                      "run_dir": "/var/tmp/hummbwtester"},
                "b": {"ssh": "sciera-ufes", "sciond": "127.0.0.1:30255",
                      "run_dir": "/var/tmp/hummbwtester"},
            },
            "metrics": {"prometheus": "b", "local_port_base": 19090, "routers": [{
                "host": "a", "address": "127.0.0.1:30442", "labels": {"br": "br-1"},
            }]},
            "shaping": [{
                "host": "a", "name": "a-to-b", "device": "eno4.140",
                "local": "[fe80::1%eno4.140]:50000", "remote": "[fe80::2]:50000",
                "expected_root": "noqueue",
            }],
        }

    def test_inventory_matches_clients_and_accepts_proxyjump_aliases(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.write_json(directory, "experiment.json", self.workload())
            server, clients, _, _ = orchestration.load_config(path)
            inventory = ssh.load_inventory(path, server, clients)
        self.assertEqual(inventory.hosts["a"].alias, "sciera-rnp")
        self.assertEqual(inventory.client_hosts["hummingbird-1"], "b")
        self.assertEqual(inventory.prometheus_host, "b")
        self.assertEqual(inventory.shaping[0].expected_root, "noqueue")

    def test_rejects_key_derived_reservations(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.workload()
            config["hummingbird"] = {"reservation_source": "keys"}
            config["hummingbird_clients"][0]["hummingbird_reservation"] = {
                "bandwidth": 1, "duration": "1m", "reverse_bandwidth": 0,
            }
            with self.assertRaisesRegex(orchestration.ConfigError, "require .*marketplace"):
                orchestration.load_config(self.write_json(directory, "experiment.json", config))

    def test_marketplace_host_must_be_declared(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.write_json(directory, "experiment.json", self.workload())
            server, clients, _, _ = orchestration.load_config(path)
            self.assertEqual(ssh.load_inventory(path, server, clients).marketplace_host, "b")

            path = self.write_json(directory, "unknown.json", self.workload("unknown"))
            server, clients, _, _ = orchestration.load_config(path)
            with self.assertRaisesRegex(orchestration.ConfigError, "marketplace.host"):
                ssh.load_inventory(path, server, clients)

    def test_marketplace_host_is_required(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.workload()
            del config["hummingbird"]["marketplace"]["host"]
            path = self.write_json(directory, "experiment.json", config)
            with self.assertRaisesRegex(orchestration.ConfigError,
                                        "marketplace.host is required for SSH"):
                orchestration.load_config(path)

    def test_static_info_path_and_control_service_are_per_host(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.workload()
            config["deployment"]["hosts"]["a"]["control_service"] = "scion-control@cs-1.service"
            path = self.write_json(directory, "experiment.json", config)
            server, clients, _, _ = orchestration.load_config(path)
            inventory = ssh.load_inventory(path, server, clients)
            self.assertEqual(inventory.hosts["a"].control_service, "scion-control@cs-1.service")
            self.assertEqual(inventory.hosts["b"].static_info, "/etc/scion/staticInfoConfig.json")
            self.assertIsNone(inventory.hosts["b"].control_service)

            for key, value in (("control_service", "cs; reboot"), ("static_info", "relative.json")):
                config = self.workload()
                config["deployment"]["hosts"]["a"][key] = value
                path = self.write_json(directory, "invalid.json", config)
                server, clients, _, _ = orchestration.load_config(path)
                with self.subTest(key=key), self.assertRaisesRegex(orchestration.ConfigError, key):
                    ssh.load_inventory(path, server, clients)

    def test_local_prometheus_port_defaults_and_must_not_overlap_relays(self):
        with tempfile.TemporaryDirectory() as directory:
            path = self.write_json(directory, "experiment.json", self.workload())
            server, clients, _, _ = orchestration.load_config(path)
            self.assertEqual(ssh.load_inventory(path, server, clients).local_prometheus_port, 8090)
            for port in (19091, 80, "18090"):
                config = self.workload()
                config["deployment"]["metrics"]["local_prometheus_port"] = port
                path = self.write_json(directory, "invalid.json", config)
                server, clients, _, _ = orchestration.load_config(path)
                with self.subTest(port=port), \
                        self.assertRaisesRegex(orchestration.ConfigError, "local_prometheus_port"):
                    ssh.load_inventory(path, server, clients)

    def test_rejects_missing_client_placement(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.workload()
            del config["best_effort_clients"][0]["node"]
            with self.assertRaisesRegex(orchestration.ConfigError, "node"):
                orchestration.load_config(self.write_json(directory, "experiment.json", config))

    def test_rejects_unknown_prometheus_host(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.workload()
            config["deployment"]["metrics"]["prometheus"] = "unknown"
            path = self.write_json(directory, "experiment.json", config)
            server, clients, _, _ = orchestration.load_config(path)
            with self.assertRaisesRegex(orchestration.ConfigError, "metrics.prometheus"):
                ssh.load_inventory(path, server, clients)

    def test_marketplace_interfaces_are_optional_and_validated(self):
        with tempfile.TemporaryDirectory() as directory:
            def interfaces(config):
                _, clients, _, _ = orchestration.load_config(
                    self.write_json(directory, "experiment.json", config))
                return next(client for client in clients if client.hummingbird) \
                    .marketplace.interfaces

            config = self.workload()
            self.assertIsNone(interfaces(config))
            marketplace = config["hummingbird"]["marketplace"]
            marketplace["interfaces"] = {"1-ff00:0:110": [104, 0], "71-1916": [0, 103, 106]}
            self.assertEqual(interfaces(config),
                             {"1-ff00:0:110": (0, 104), "71-1916": (0, 103, 106)})
            for interfaces, message in (
                ({}, "non-empty object"),
                ({"71_1916": [0]}, "invalid ISD-AS"),
                ({"71-1916": []}, "non-empty array"),
                ({"71-1916": [-1]}, "0 through 65535"),
                ({"71-1916": [65536]}, "0 through 65535"),
                ({"71-1916": [True]}, "0 through 65535"),
                ({"71-1916": ["103"]}, "0 through 65535"),
                ({"71-1916": [103, 103]}, "duplicate"),
            ):
                marketplace["interfaces"] = interfaces
                path = self.write_json(directory, "experiment.json", config)
                with self.subTest(interfaces=interfaces), \
                        self.assertRaisesRegex(orchestration.ConfigError, message):
                    orchestration.load_config(path)

    def test_router_labels_must_differ_in_as_or_br(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.workload()
            routers = config["deployment"]["metrics"]["routers"]
            routers[0]["labels"] = {"as": "71_1916", "br": "br-2"}
            routers.append({"host": "b", "address": "127.0.0.1:30442",
                            "labels": {"as": "71_2_0_152", "br": "br-2"}})
            path = self.write_json(directory, "experiment.json", config)
            server, clients, _, _ = orchestration.load_config(path)
            self.assertEqual(len(ssh.load_inventory(path, server, clients).routers), 2)

            routers[1]["labels"]["as"] = "71_1916"
            path = self.write_json(directory, "experiment.json", config)
            with self.assertRaisesRegex(orchestration.ConfigError, r"routers\[1\].*as and br"):
                ssh.load_inventory(path, server, clients)

    def test_ssh_command_quotes_script_for_remote_shell(self):
        host = ssh.SSHHost("a", "sciera-rnp", "127.0.0.1:30255", "/var/tmp/humm", None)
        script = "test -f /etc/hosts && printf '%s' 'quoted value'"
        with mock.patch.object(ssh.subprocess, "run") as run:
            ssh.ssh_command(host, script)
        argv = run.call_args.args[0]
        self.assertEqual(argv[:5], ["ssh", "-o", "LogLevel=ERROR", "--", "sciera-rnp"])
        self.assertEqual(shlex.split(argv[5]), ["sh", "-c", script])
        # SSH hands the remote command line to a shell; emulate that parsing locally.
        result = subprocess.run(["sh", "-c", argv[5]], check=True,
                                capture_output=True, text=True)
        self.assertEqual(result.stdout, "quoted value")

    def test_scp_hides_its_progress_meter(self):
        host = ssh.SSHHost("a", "sciera-rnp", "127.0.0.1:30255", "/var/tmp/humm", None)
        with mock.patch.object(ssh.subprocess, "run") as run:
            ssh.scp_to(host, Path("local"), "/remote")
        self.assertEqual(run.call_args.args[0][:2], ["scp", "-q"])

    def test_launch_never_places_jwt_in_ssh_arguments(self):
        host = ssh.SSHHost("a", "sciera-rnp", "127.0.0.1:30255", "/var/tmp/humm", None)
        with tempfile.TemporaryDirectory() as directory, \
             mock.patch.object(ssh.subprocess, "Popen") as popen:
            ssh.launch(host, "run-1", "client", ["ignored", "-mode", "client"],
                       Path(directory) / "client.log", "/var/tmp/humm/run-1/marketplace.jwt")
        command = popen.call_args.args[0]
        self.assertNotIn("jwt-value", command)
        self.assertIn("marketplace.jwt", command[-1])
        self.assertEqual(shlex.split(command[-1])[:2], ["sh", "-c"])
        self.assertIn("; exec ", shlex.split(command[-1])[2])

    CLEANUP_MESSAGES = [
        "stopping server on a",
        "stopping best-effort-1 on a",
        "stopping hummingbird-1 on b",
        "waiting for server on a to exit",
        "waiting for best-effort-1 on a to exit",
        "waiting for hummingbird-1 on b to exit",
        "removing run directory on a",
        "removing run directory on b",
        "experiment stopped",
    ]

    def run_with(self, processes, sleep):
        """Run the experiment with the given server and client processes; return the status and
        the printed messages."""
        with tempfile.TemporaryDirectory() as directory:
            path = self.write_json(directory, "experiment.json", self.workload())
            config = orchestration.load_config(path)
            inventory = ssh.load_inventory(path, config[0], config[1])
        with mock.patch.object(ssh.workload, "require_built_binary"), \
             mock.patch.object(ssh, "sha256", return_value="digest"), \
             mock.patch.object(ssh, "preflight"), \
             mock.patch.object(ssh, "verify_setup"), \
             mock.patch.object(ssh, "ssh_command"), \
             mock.patch.object(ssh, "stop_stray_testers") as strays, \
             mock.patch.object(ssh, "launch", side_effect=processes), \
             mock.patch.object(ssh.time, "sleep", side_effect=sleep), \
             mock.patch.object(ssh, "stop") as stop, \
             mock.patch.object(ssh, "cleanup_host") as cleanup, \
             mock.patch("builtins.print") as output:
            status = ssh.run_experiment(path, config, inventory)
        self.assertEqual([call.args[0].name for call in strays.call_args_list], ["a", "b"])
        self.assertEqual(stop.call_count, 3)
        self.assertEqual(cleanup.call_count, 2)
        return status, [call.args[0] for call in output.call_args_list]

    @staticmethod
    def processes(*exits):
        """Server and client processes, each exiting with (status, after this many sleeps), or
        never for None; and the sleep stand-in that advances their clock."""
        clock = [0]

        def process(exit):
            fake = mock.Mock()
            fake.poll.side_effect = lambda: (
                exit[0] if exit is not None and clock[0] >= exit[1] else None)
            fake.wait.side_effect = lambda timeout=None: fake.poll()
            return fake

        def sleep(_):
            clock[0] += 1
        return [process(exit) for exit in exits], sleep

    def test_interrupted_run_reports_each_cleanup_step(self):
        processes, _ = self.processes(None, None, None)
        status, messages = self.run_with(processes, [None, KeyboardInterrupt])
        self.assertEqual(status, 130)
        self.assertEqual(messages, ["interrupted, stopping the experiment",
                                    *self.CLEANUP_MESSAGES])

    def test_run_reports_each_client_exit_and_stops_the_server(self):
        # The server never exits by itself; hummingbird-1 fails before best-effort-1 finishes.
        processes, sleep = self.processes(None, (0, 4), (1, 2))
        status, messages = self.run_with(processes, sleep)
        self.assertEqual(status, 1)
        self.assertEqual(messages, [
            "hummingbird-1 on b stopped with exit status 1; "
            "see logs/hummbwtester/ssh-hummingbird-1.log",
            "best-effort-1 on a finished; see logs/hummbwtester/ssh-best-effort-1.log",
            "all clients exited, stopping the experiment",
            *self.CLEANUP_MESSAGES,
        ])

    def test_run_reports_a_server_exit_and_stops(self):
        processes, sleep = self.processes((-15, 3), None, (255, 2))
        status, messages = self.run_with(processes, sleep)
        self.assertEqual(status, 1)
        self.assertEqual(messages, [
            "hummingbird-1 on b stopped: ssh failed (status 255); "
            "see logs/hummbwtester/ssh-hummingbird-1.log",
            "server on a stopped: its ssh client was killed by signal 15; "
            "see logs/hummbwtester/ssh-server.log",
            "the server stopped, stopping the experiment",
            *self.CLEANUP_MESSAGES,
        ])

    def test_cleanup_kills_an_ssh_session_that_does_not_exit(self):
        processes, sleep = self.processes(None, (0, 2), (0, 2))
        stuck = processes[0]
        stuck.wait.side_effect = [subprocess.TimeoutExpired("ssh", 10), None]
        status, messages = self.run_with(processes, sleep)
        self.assertEqual(status, 0)
        stuck.kill.assert_called_once()
        self.assertIn("warning: the ssh session of server on a did not exit; killing it",
                      messages)
        self.assertEqual(messages[-3:], ["removing run directory on a",
                                         "removing run directory on b", "experiment stopped"])

    def test_stray_testers_are_reported_and_killed(self):
        host = ssh.SSHHost("a", "sciera-rnp", "127.0.0.1:30255", "/var/tmp/humm", None)

        def strays(*results, dry_run=False):
            with mock.patch.object(ssh, "ssh_command", side_effect=[
                    subprocess.CompletedProcess([], code, out, err) for code, out, err in results
                    ]) as run, mock.patch("builtins.print") as output:
                ssh.stop_stray_testers(host, dry_run)
            return ([call.args[1] for call in run.call_args_list],
                    [call.args[0] for call in output.call_args_list])

        self.assertEqual(strays((1, "", "")), (["pgrep -a -x hummbwtester"], []))
        listing = (0, "42 /var/tmp/humm/setup/hummbwtester -mode server\n", "")
        commands, messages = strays(listing, (0, "", ""))
        self.assertTrue(commands[1].startswith("pkill -TERM -x hummbwtester\n"))
        self.assertIn("pkill -KILL -x hummbwtester", commands[1])
        self.assertEqual(messages, [
            "warning: 1 hummbwtester process(es) already running on a:",
            "  42 /var/tmp/humm/setup/hummbwtester -mode server",
            "killed them on a",
        ])
        commands, messages = strays(listing, dry_run=True)
        self.assertEqual(len(commands), 1)
        self.assertEqual(messages[-1], "dry-run: would kill them on a")
        with self.assertRaisesRegex(ssh.SSHError, "cannot kill"):
            strays(listing, (1, "", ""))
        with self.assertRaisesRegex(ssh.SSHError, "cannot list .* on a: denied"):
            strays((3, "", "denied"))

    def test_ssh_never_reads_the_controller_terminal(self):
        host = ssh.SSHHost("a", "sciera-rnp", "127.0.0.1:30255", "/var/tmp/humm", None)
        with mock.patch.object(ssh.subprocess, "run") as run:
            ssh.ssh_command(host, "true")
            ssh.ssh_command(host, "cat", input="data")
        self.assertIs(run.call_args_list[0].kwargs["stdin"], subprocess.DEVNULL)
        self.assertNotIn("stdin", run.call_args_list[1].kwargs)
        with tempfile.TemporaryDirectory() as directory, \
             mock.patch.object(ssh.subprocess, "Popen") as popen:
            ssh.launch(host, "run-1", "server", ["ignored"], Path(directory) / "server.log")
        self.assertIs(popen.call_args.kwargs["stdin"], subprocess.DEVNULL)

    def test_rejects_duplicate_shaping_device(self):
        with tempfile.TemporaryDirectory() as directory:
            config = self.workload()
            shaping = config["deployment"]["shaping"]
            shaping.append({**shaping[0], "name": "second"})
            path = self.write_json(directory, "experiment.json", config)
            server, clients, _, _ = orchestration.load_config(path)
            with self.assertRaisesRegex(orchestration.ConfigError, "duplicates a host device"):
                ssh.load_inventory(path, server, clients)


if __name__ == "__main__":
    unittest.main()
