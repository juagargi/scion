import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

from tools.hummbwtester.scripts import experiment


class ExperimentTest(unittest.TestCase):
    def test_dispatches_docker_setup_and_ssh_run_from_one_config(self):
        with mock.patch.object(experiment.planner, "load_config", return_value=(
            mock.sentinel.server, [], {}, {},
        )), mock.patch.object(experiment.planner, "compose_data", return_value={}), \
             mock.patch.object(experiment.planner, "validate_endpoints"), \
             mock.patch.object(experiment, "setup_local", return_value=0) as local, \
             mock.patch.object(experiment.ssh, "load_inventory"), \
             mock.patch.object(experiment.ssh, "run_experiment", return_value=0) as remote:
            with mock.patch.object(experiment.planner, "read_json", return_value={
                "deployment": {"kind": "docker"},
            }):
                self.assertEqual(experiment.execute("setup", Path("local.json")), 0)
            with mock.patch.object(experiment.planner, "read_json", return_value={
                "deployment": {"kind": "ssh"},
            }):
                self.assertEqual(experiment.execute("run", Path("remote.json")), 0)
        self.assertEqual(local.call_args.args[0].path, Path("local.json"))
        self.assertEqual(remote.call_args.args[0], Path("remote.json"))

    def test_dry_run_is_forwarded_in_both_modes(self):
        with mock.patch.object(experiment.planner, "load_config", return_value=(
            mock.sentinel.server, [], {}, {},
        )), mock.patch.object(experiment.planner, "compose_data", return_value={}), \
             mock.patch.object(experiment.planner, "validate_endpoints"), \
             mock.patch.object(experiment, "setup_local", return_value=0) as local, \
             mock.patch.object(experiment.ssh, "load_inventory"), \
             mock.patch.object(experiment.ssh_setup, "setup", return_value=0) as remote:
            with mock.patch.object(experiment.planner, "read_json", return_value={
                "deployment": {"kind": "docker"},
            }):
                self.assertEqual(experiment.execute("setup", Path("local.json"), dry_run=True), 0)
            with mock.patch.object(experiment.planner, "read_json", return_value={
                "deployment": {"kind": "ssh"},
            }):
                self.assertEqual(experiment.execute("setup", Path("remote.json"), dry_run=True), 0)
        self.assertTrue(local.call_args.args[1])
        self.assertTrue(remote.call_args.args[3])

    def test_docker_dry_run_lists_steps_and_starts_nothing(self):
        plan = experiment.ExperimentPlan(Path("local.json"), mock.sentinel.config, "docker", None)
        with mock.patch.object(experiment, "_check_local_prometheus_owner", return_value=False), \
             mock.patch.object(experiment, "_check_local_grafana_owner", return_value=False), \
             mock.patch.object(experiment.planner, "describe_setup",
                               return_value=["would run ./scion.sh start"]), \
             mock.patch.object(experiment.planner, "setup") as setup, \
             mock.patch.object(experiment.subprocess, "run") as run, \
             mock.patch("builtins.print") as output:
            self.assertEqual(experiment.setup_local(plan, dry_run=True), 0)
        setup.assert_not_called()
        run.assert_not_called()
        lines = [call.args[0] for call in output.call_args_list]
        self.assertEqual(lines[:4], [
            "dry-run: docker setup would perform these steps:",
            "  1. would run ./scion.sh start",
            "  2. would run docker compose --project-name monitoring up -d prometheus grafana, "
            "which starts the local hummbwtester-prometheus and hummbwtester-grafana containers",
            "  3. would check that both containers keep running and answer on "
            "Prometheus /-/ready and Grafana /api/health",
        ])
        self.assertEqual(lines[-1], experiment.planner.DRY_RUN_NOTICE)

    def test_dry_run_is_only_accepted_for_setup(self):
        with mock.patch.object(experiment.sys, "argv", [
            "experiment.py", "teardown", "--config", "remote.json", "--dry-run",
        ]), mock.patch.object(experiment, "execute") as execute, \
             mock.patch("sys.stderr"), self.assertRaises(SystemExit):
            experiment.main()
        execute.assert_not_called()

    def test_local_prometheus_refuses_foreign_container(self):
        with mock.patch.object(experiment.planner, "local_container_owner",
                               return_value="other/prometheus"):
            with self.assertRaisesRegex(RuntimeError, "belongs to other/prometheus"):
                experiment._check_local_prometheus_owner()

    def test_local_grafana_refuses_foreign_container(self):
        with mock.patch.object(experiment.planner, "local_container_owner",
                               return_value="other/grafana"):
            with self.assertRaisesRegex(RuntimeError, "belongs to other/grafana"):
                experiment._check_local_grafana_owner()

    def test_local_setup_starts_both_services_and_waits(self):
        plan = experiment.ExperimentPlan(Path("local.json"), mock.sentinel.config, "docker", None)
        with mock.patch.object(experiment, "_check_local_prometheus_owner", return_value=False), \
             mock.patch.object(experiment, "_check_local_grafana_owner", return_value=False), \
             mock.patch.object(experiment.planner, "setup", return_value=0), \
             mock.patch.object(experiment, "_wait_monitoring") as wait, \
             mock.patch.object(experiment.subprocess, "run") as run:
            self.assertEqual(experiment.setup_local(plan), 0)
        self.assertEqual(run.call_args.args[0][-4:], ["up", "-d", "prometheus", "grafana"])
        wait.assert_called_once_with()

    def test_wait_service_fails_when_container_exited_even_if_port_answers(self):
        logs = mock.Mock(stdout="bind: address already in use", stderr="")
        with mock.patch.object(experiment, "_container_running", return_value=False), \
             mock.patch.object(experiment, "_http_answers", return_value=True), \
             mock.patch.object(experiment.subprocess, "run", return_value=logs):
            with self.assertRaisesRegex(RuntimeError, "(?s)not running.*address already in use"):
                experiment._wait_service("hummbwtester-prometheus", "http://x/-/ready")

    def test_wait_service_returns_when_running_and_answering(self):
        with mock.patch.object(experiment, "_container_running", return_value=True), \
             mock.patch.object(experiment, "_http_answers", return_value=True):
            experiment._wait_service("hummbwtester-grafana", "http://x/api/health")

    def test_wait_service_times_out_when_running_but_silent(self):
        with mock.patch.object(experiment, "_container_running", return_value=True), \
             mock.patch.object(experiment, "_http_answers", return_value=False), \
             mock.patch.object(experiment.time, "sleep"):
            with self.assertRaisesRegex(RuntimeError, "does not answer"):
                experiment._wait_service("hummbwtester-grafana", "http://x/api/health", 2)

    def test_local_teardown_leaves_topology_running(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "qdiscs.json"
            state.write_text(json.dumps({"tc": {}, "peers": {"br-1": ["192.0.2.2"]}}))
            running = mock.Mock(returncode=0, stdout="container-id\n")
            with mock.patch.object(experiment.planner, "DOCKER_QDISC_STATE", state), \
                 mock.patch.object(experiment, "_check_local_prometheus_owner",
                                   return_value=True), \
                 mock.patch.object(experiment, "_check_local_grafana_owner",
                                   return_value=True), \
                 mock.patch.object(experiment.subprocess, "run", return_value=running) as run:
                self.assertEqual(experiment.teardown_local(), 0)
            self.assertFalse(state.exists())
        self.assertEqual(run.call_count, 3)
        self.assertEqual(run.call_args_list[0].args[0][-2:], ["prometheus", "grafana"])
        self.assertIn("cleanup", run.call_args_list[2].args[0])
        self.assertFalse(any("down" in call.args[0] for call in run.call_args_list))


if __name__ == "__main__":
    unittest.main()
