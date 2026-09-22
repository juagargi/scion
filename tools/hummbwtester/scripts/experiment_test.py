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

    def test_local_prometheus_refuses_foreign_container(self):
        with mock.patch.object(experiment, "_local_prometheus_owner", return_value="other/prometheus"):
            with self.assertRaisesRegex(RuntimeError, "belongs to other/prometheus"):
                experiment._check_local_prometheus_owner()

    def test_local_teardown_leaves_topology_running(self):
        with tempfile.TemporaryDirectory() as directory:
            state = Path(directory) / "qdiscs.json"
            state.write_text(json.dumps({"tc": {}, "peers": {"br-1": ["192.0.2.2"]}}))
            running = mock.Mock(returncode=0, stdout="container-id\n")
            with mock.patch.object(experiment.planner, "DOCKER_QDISC_STATE", state), \
                 mock.patch.object(experiment, "_check_local_prometheus_owner", return_value=True), \
                 mock.patch.object(experiment.subprocess, "run", return_value=running) as run:
                self.assertEqual(experiment.teardown_local(), 0)
            self.assertFalse(state.exists())
        self.assertEqual(run.call_count, 3)
        self.assertIn("prometheus", run.call_args_list[0].args[0])
        self.assertIn("cleanup", run.call_args_list[2].args[0])
        self.assertFalse(any("down" in call.args[0] for call in run.call_args_list))


if __name__ == "__main__":
    unittest.main()
