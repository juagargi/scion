import argparse
import unittest

from tools.hummbwtester import veth_shaper


class VethShaperTest(unittest.TestCase):
    def args(self):
        # E.g. parameters observed at sciera-ufes
        return argparse.Namespace(
            name="humm-br2",
            device="ens192",
            out_device="hummbr2o",
            in_device="hummbr2i",
            local=veth_shaper.Endpoint.parse("10.6.7.1:50001"),
            remote=veth_shaper.Endpoint.parse("10.6.7.2:50001"),
            veth_network="169.254.254.0/30",
            rate="10mbit",
            burst="50kb",
            limit="256kb",
            mark=0x48554232,
            table=50001,
            rule_priority=10001,
        )

    def test_builds_veth_addresses_and_exact_flow_rules(self):
        config = veth_shaper.build_config(self.args())
        self.assertEqual(config.out_address, "169.254.254.1/30")
        self.assertEqual(config.in_address, "169.254.254.2/30")
        self.assertEqual(veth_shaper.output_mark_rule(config)[:12], [
            "-o", "ens192", "-p", "udp",
            "-s", "10.6.7.1", "--sport", "50001",
            "-d", "10.6.7.2", "--dport", "50001",
        ])
        self.assertIn("hummbr2i", veth_shaper.forward_rule(config))

    def test_rejects_non_ipv4_endpoint(self):
        with self.assertRaisesRegex(argparse.ArgumentTypeError, "IPv4"):
            veth_shaper.Endpoint.parse("[2001:db8::1]:50001")

    def test_rejects_endpoint_inside_veth_network(self):
        args = self.args()
        args.local = veth_shaper.Endpoint.parse("169.254.254.1:50001")
        with self.assertRaisesRegex(veth_shaper.Error, "must not contain"):
            veth_shaper.build_config(args)

    def test_rejects_veth_name_that_cannot_be_used_as_a_sysctl_component(self):
        args = self.args()
        args.in_device = "humm.br2i"
        with self.assertRaisesRegex(veth_shaper.Error, "veth name"):
            veth_shaper.build_config(args)


if __name__ == "__main__":
    unittest.main()
