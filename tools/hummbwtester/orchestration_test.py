import json
from pathlib import Path
import tempfile
import unittest

from tools.hummbwtester.orchestration import ConfigError, load_config


class ConfigTest(unittest.TestCase):
    def write_config(self, value):
        # Keep each fixture isolated: load_config accepts a path because the production scripts
        # read the manually edited JSON file from disk.
        directory = tempfile.TemporaryDirectory()
        path = Path(directory.name) / "hummbwtester.json"
        path.write_text(json.dumps(value))
        self.addCleanup(directory.cleanup)
        return path

    def base_config(self):
        # Deliberately put the clients in reverse lexical order. The port assignment must depend
        # only on client_id, not on whether the client is Hummingbird or best-effort, nor on its
        # position in the JSON arrays.
        return {
            "server": {"isd_as": "1-ff00:0:112", "host": "fd00::1", "port": 12345},
            "hummingbird_clients": [{
                "client_id": "zeta", "isd_as": "1-ff00:0:111", "host": "172.20.0.29", "port": 0,
            }],
            "best_effort_clients": [{
                "client_id": "alpha", "isd_as": "1-ff00:0:110", "host": "172.20.0.22", "port": 0,
            }],
            "tc": {"rate": "10mbit", "burst": "50kb", "latency": "1ms"},
        }

    def test_clients_are_sorted_for_metrics_ports(self):
        _, clients, _ = load_config(self.write_config(self.base_config()))
        # The first sorted client owns the fixed base port; every subsequent client increments it.
        self.assertEqual([("alpha", 9090), ("zeta", 9091)],
                         [(client.client_id, client.metrics_port) for client in clients])

    def test_rejects_unscoped_client_fields(self):
        config = self.base_config()
        # sciond is derived from gen/sciond_addresses.json, so accepting it in the experiment
        # configuration would make the topology and JSON disagree silently.
        config["best_effort_clients"][0]["sciond"] = "172.20.0.21:30255"
        with self.assertRaises(ConfigError):
            load_config(self.write_config(config))


if __name__ == "__main__":
    unittest.main()
