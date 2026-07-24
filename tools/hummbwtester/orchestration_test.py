import json
from pathlib import Path
import tempfile
import unittest
from unittest import mock

from tools.hummbwtester import orchestration
from tools.hummbwtester.orchestration import (
    BFDHealth,
    ConfigError,
    bfd_health_from_metrics,
    client_args,
    inter_as_router_peers,
    load_config,
    patch_compose,
    patch_toml_section,
    verify_bfd_health,
)


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
            "router": {
                "send_buffer_size": 16384,
                "ingress_batch_size": 64,
                "egress_batch_size": 1,
                "egress_queue_size": 64,
            },
            "tc": {"rate": "10mbit", "burst": "50kb", "limit": "256kb"},
        }

    def test_clients_are_sorted_for_metrics_ports(self):
        _, clients, _, _ = load_config(self.write_config(self.base_config()))
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

    def test_requires_positive_router_tuning(self):
        for key in self.base_config()["router"]:
            with self.subTest(key=key):
                config = self.base_config()
                config["router"][key] = 0
                with self.assertRaises(ConfigError):
                    load_config(self.write_config(config))

    def test_rejects_legacy_router_batch_size(self):
        config = self.base_config()
        config["router"]["batch_size"] = 1
        with self.assertRaises(ConfigError):
            load_config(self.write_config(config))

    def test_requires_all_router_tuning_values(self):
        for key in self.base_config()["router"]:
            with self.subTest(key=key):
                config = self.base_config()
                del config["router"][key]
                with self.assertRaises(ConfigError):
                    load_config(self.write_config(config))

    def test_rejects_latency_instead_of_explicit_limit(self):
        config = self.base_config()
        config["tc"] = {"rate": "10mbit", "burst": "50kb", "latency": "1ms"}
        with self.assertRaises(ConfigError):
            load_config(self.write_config(config))

    def test_optional_tuning_is_omitted_or_passed_to_client(self):
        server, clients, _, _ = load_config(self.write_config(self.base_config()))
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
        server, clients, _, _ = load_config(self.write_config(config))
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


class SetupPatchTest(unittest.TestCase):
    def test_patch_toml_section_adds_and_updates_idempotently(self):
        cases = {
            "without section": "[general]\nid = \"br1\"\n",
            "with section": (
                "[general]\nid = \"br1\"\n\n[router]\nsend_buffer_size = 999\nbatch_size = 1\n\n"
                "[metrics]\nprometheus = \"127.0.0.1:30442\"\n"
            ),
        }
        for name, original in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                path = Path(directory) / "br.toml"
                path.write_text(original)
                values = {
                    "send_buffer_size": 16384,
                    "ingress_batch_size": 64,
                    "egress_batch_size": 1,
                    "egress_queue_size": 64,
                }
                patch_toml_section(path, "router", values, remove={"batch_size"})
                once = path.read_text()
                patch_toml_section(path, "router", values, remove={"batch_size"})
                self.assertEqual(once, path.read_text())
                self.assertEqual(once.count("[router]"), 1)
                self.assertEqual(once.count("send_buffer_size = 16384"), 1)
                self.assertEqual(once.count("ingress_batch_size = 64"), 1)
                self.assertEqual(once.count("egress_batch_size = 1"), 1)
                self.assertEqual(once.count("egress_queue_size = 64"), 1)
                self.assertNotRegex(once, r"(?m)^batch_size[ \t]*=")
                if "[metrics]" in original:
                    self.assertIn("[metrics]", once)

    def test_inter_as_router_peers_excludes_internal_network(self):
        compose = {
            "services": {
                "br110-a": {
                    "networks": {
                        "external": {"ipv4_address": "192.0.2.1"},
                        "internal": {"ipv4_address": "192.0.2.9"},
                    },
                },
                "br111-a": {
                    "networks": {"external": {"ipv4_address": "192.0.2.2"}},
                },
                "br110-b": {
                    "networks": {"internal": {"ipv4_address": "192.0.2.10"}},
                },
            },
        }
        ias = {"br110-a": "1-ff00:0:110", "br110-b": "1-ff00:0:110",
               "br111-a": "1-ff00:0:111"}
        with mock.patch.object(orchestration, "br_ias", return_value=ias):
            self.assertEqual(inter_as_router_peers(compose), {
                "br110-a": ["192.0.2.2"],
                "br111-a": ["192.0.2.1"],
            })

    def test_patch_compose_creates_namespace_helper_per_router(self):
        compose = {
            "services": {
                "br1-ff00_0_110-1": {"image": "router"},
                "br1-ff00_0_111-1": {"image": "router"},
                "hummbwtester_tc_setup": {"network_mode": "host"},
            },
        }
        peers = {
            "br1-ff00_0_110-1": ["172.20.0.3"],
            "br1-ff00_0_111-1": ["172.20.0.2"],
        }
        tc = {"rate": "10mbit", "burst": "50kb", "limit": "256kb"}
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "scion-dc.yml"
            with mock.patch.object(orchestration, "COMPOSE", output):
                patch_compose(compose, peers, tc)
        self.assertNotIn("hummbwtester_tc_setup", compose["services"])
        for router, peer_addresses in peers.items():
            helper = compose["services"][orchestration.tc_helper_name(router)]
            self.assertEqual(helper["network_mode"], f"service:{router}")
            self.assertEqual(helper["depends_on"], [router])
            self.assertEqual(helper["command"], [
                "setup", "10mbit", "50kb", "256kb", *peer_addresses,
            ])


class BFDHealthTest(unittest.TestCase):
    def test_aggregates_external_bfd_metrics(self):
        snapshot = bfd_health_from_metrics([
            '\n'.join([
                'router_bfd_state_changes_total{interface="1"} 2',
                'router_bfd_sent_packets_total{interface="1"} 10',
                'router_bfd_received_packets_total{interface="1"} 9',
                'router_interface_up{interface="1"} 1',
            ]),
            '\n'.join([
                'router_bfd_state_changes_total{interface="2"} 3',
                'router_bfd_sent_packets_total{interface="2"} 20',
                'router_bfd_received_packets_total{interface="2"} 19',
                'router_interface_up{interface="2"} 1',
            ]),
        ])
        self.assertEqual(snapshot.state_changes, 5)
        self.assertEqual(snapshot.packets_sent, 30)
        self.assertEqual(snapshot.packets_received, 28)
        self.assertEqual(len(snapshot.interface_up), 2)
        self.assertTrue(all(value == 1 for value in snapshot.interface_up.values()))

    def test_rejects_bfd_state_change_or_down_interface(self):
        before = BFDHealth(4, 100, 100, {'router[0]{interface="1"}': 1})
        after = BFDHealth(5, 110, 110, {'router[0]{interface="1"}': 1})
        with mock.patch("builtins.print"), self.assertRaises(RuntimeError):
            verify_bfd_health(before, after)
        after = BFDHealth(4, 110, 110, {'router[0]{interface="1"}': 0})
        with mock.patch("builtins.print"), self.assertRaises(RuntimeError):
            verify_bfd_health(before, after)


if __name__ == "__main__":
    unittest.main()
