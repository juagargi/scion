import json
from pathlib import Path
import tempfile
import unittest

from tools.hummbwtester.orchestration import ConfigError, client_args, load_config


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
                "bandwidth": "2Mbps", "duration": "60s",
                "hummingbird_reservation": {
                    "bandwidth": 1000, "duration": "10s", "reverse_bandwidth": 1000,
                },
            }],
            "best_effort_clients": [{
                "client_id": "alpha", "isd_as": "1-ff00:0:110", "host": "172.20.0.22", "port": 0,
                "bandwidth": "1Mbps", "duration": "30s",
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

    def test_requires_workload_for_every_client(self):
        for client_type in ("hummingbird_clients", "best_effort_clients"):
            with self.subTest(client_type=client_type):
                config = self.base_config()
                del config[client_type][0]["bandwidth"]
                with self.assertRaises(ConfigError):
                    load_config(self.write_config(config))

    def test_requires_reservation_for_hummingbird_clients(self):
        config = self.base_config()
        del config["hummingbird_clients"][0]["hummingbird_reservation"]
        with self.assertRaises(ConfigError):
            load_config(self.write_config(config))

    def test_rejects_reservation_for_best_effort_clients(self):
        config = self.base_config()
        config["best_effort_clients"][0]["hummingbird_reservation"] = {
            "bandwidth": 1000, "duration": "10s", "reverse_bandwidth": 1000,
        }
        with self.assertRaises(ConfigError):
            load_config(self.write_config(config))

    def test_optional_tuning_is_omitted_or_passed_to_client(self):
        server, clients, _ = load_config(self.write_config(self.base_config()))
        best_effort, hummingbird = clients
        # Omitted settings must leave the tester's own defaults in effect.
        args = client_args(best_effort, server, "172.20.0.21:30255")
        self.assertNotIn("-payload-size", args)
        self.assertNotIn("-pong-rate", args)
        self.assertNotIn("-renewal-fraction", args)
        self.assertNotIn("-hummingbird", args)

        config = self.base_config()
        config["best_effort_clients"][0].update({
            "payload_size": 1200, "pong_rate": 2.0, "renewal_fraction": 0.5,
        })
        server, clients, _ = load_config(self.write_config(config))
        best_effort, hummingbird = clients
        args = client_args(best_effort, server, "172.20.0.21:30255")
        self.assertEqual(
            args[args.index("-bandwidth") + 1], "1Mbps")
        self.assertEqual(args[args.index("-duration") + 1], "30s")
        self.assertEqual(args[args.index("-payload-size") + 1], "1200")
        self.assertEqual(args[args.index("-pong-rate") + 1], "2.0")
        self.assertEqual(args[args.index("-renewal-fraction") + 1], "0.5")

        args = client_args(hummingbird, server, "172.20.0.21:30255")
        self.assertEqual(args[args.index("-hummingbird") + 1], "1000,10s,1000")


if __name__ == "__main__":
    unittest.main()
