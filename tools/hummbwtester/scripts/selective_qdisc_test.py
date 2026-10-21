import argparse
import json
import unittest
from unittest import mock

from tools.hummbwtester.scripts import selective_qdisc


class SelectiveQdiscTest(unittest.TestCase):
    def args(self, *, ipv6=False):
        if ipv6:
            local = selective_qdisc.Endpoint.parse("[fe80::77c:140%eno4.140]:50031")
            remote = selective_qdisc.Endpoint.parse("[fe80::2:0:5c:140]:50031")
            device = "eno4.140"
        else:
            local = selective_qdisc.Endpoint.parse("10.6.7.1:50001")
            remote = selective_qdisc.Endpoint.parse("10.6.7.2:50001")
            device = "ens192"
        return argparse.Namespace(
            name="humm-br2",
            device=device,
            local=local,
            remote=remote,
            rate="10mbit",
            burst="50kb",
            limit="1mb",
            expected_root="noqueue",
        )

    def test_builds_ipv4_prio_tbf_and_flower_commands(self):
        config = selective_qdisc.build_config(self.args())
        command = selective_qdisc.prio_command(config)
        self.assertEqual(command[command.index("bands"):], [
            "bands", "2", "priomap", *("1" for _ in range(16)),
        ])
        command = selective_qdisc.tbf_command(config)
        self.assertEqual(command[command.index("tbf"):], [
            "tbf", "rate", "10mbit", "burst", "50kb", "limit", "1mb",
        ])
        command = selective_qdisc.filter_command(config)
        self.assertEqual(command[command.index("flower"):], [
            "flower", "ip_proto", "udp",
            "src_ip", "10.6.7.1", "dst_ip", "10.6.7.2",
            "src_port", "50001", "dst_port", "50001", "classid", "1:1",
        ])
        self.assertIn("ip", command)

    def test_builds_scoped_ipv6_filter_without_zone(self):
        config = selective_qdisc.build_config(self.args(ipv6=True))
        command = selective_qdisc.filter_command(config)
        self.assertIn("ipv6", command)
        self.assertIn("fe80::77c:140", command)
        self.assertNotIn("fe80::77c:140%eno4.140", command)
        self.assertEqual(str(config.local), "[fe80::77c:140%eno4.140]:50031")

    def test_requires_brackets_for_ipv6(self):
        with self.assertRaisesRegex(argparse.ArgumentTypeError, "must use"):
            selective_qdisc.Endpoint.parse("2001:db8::1:50001")

    def test_rejects_mixed_address_families(self):
        args = self.args()
        args.remote = selective_qdisc.Endpoint.parse("[2001:db8::1]:50001")
        with self.assertRaisesRegex(selective_qdisc.Error, "same address family"):
            selective_qdisc.build_config(args)

    def test_rejects_zone_that_does_not_match_device(self):
        args = self.args(ipv6=True)
        args.device = "eno4.144"
        with self.assertRaisesRegex(selective_qdisc.Error, "does not match"):
            selective_qdisc.build_config(args)

    def test_rejects_unsafe_tc_value(self):
        with self.assertRaises(argparse.ArgumentTypeError):
            selective_qdisc.validate_tc("10mbit;reboot")

    def test_status_keeps_plain_text_where_tc_ignores_json(self):
        # iproute2 5.15 prints `tc -j class show` as text; parsing it used to abort `up`.
        text = "class prio 1:1 parent 1: leaf 10: \n Sent 1962408 bytes 6467 pkt\n"
        outputs = {"class": text, "qdisc": '[{"kind":"prio","handle":"1:","root":true}]',
                   "filter": "[]"}

        def run(arguments, *, check=True, capture=False):
            return mock.Mock(returncode=0, stdout=outputs[arguments[3]], stderr="")

        config = selective_qdisc.Config(
            "rnp-ufms", "eno4.140", selective_qdisc.Endpoint("fe80::77c:140", 50031, "eno4.140"),
            selective_qdisc.Endpoint("fe80::2:0:5c:140", 50031), "10mbit", "50kb", "256kb",
            "noqueue",
        )
        with mock.patch.object(selective_qdisc, "run", side_effect=run), \
             mock.patch("builtins.print") as output:
            self.assertEqual(selective_qdisc.status(config), 0)
        report = json.loads(output.call_args.args[0])
        self.assertTrue(report["managed_root_present"])
        self.assertEqual(report["classes"], {"text": text.strip()})
        self.assertEqual(report["filters"], [])

    def test_restore_mq_has_explicit_fallback(self):
        args = self.args()
        args.expected_root = "mq"
        config = selective_qdisc.build_config(args)
        with mock.patch.object(
            selective_qdisc, "root_matches", side_effect=[False, False, True],
        ), mock.patch.object(
            selective_qdisc, "managed_root_present", return_value=True,
        ), mock.patch.object(selective_qdisc, "run") as run:
            selective_qdisc.restore_baseline(config)
        self.assertEqual(run.call_args_list, [
            mock.call(["tc", "qdisc", "del", "dev", "ens192", "root"]),
            mock.call(["tc", "qdisc", "replace", "dev", "ens192", "root", "mq"]),
        ])


if __name__ == "__main__":
    unittest.main()
