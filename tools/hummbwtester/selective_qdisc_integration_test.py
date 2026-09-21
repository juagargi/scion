#!/usr/bin/env python3
"""Privileged integration test for selective_qdisc.py.

Run directly from the repository root:

    sudo ./tools/hummbwtester/selective_qdisc_integration_test.py

The outer process re-executes the tests in a new network and mount namespace.
The tests create only disposable veth devices in that namespace;
no other interface is inspected or changed.
"""

from __future__ import annotations

import errno
import json
import os
from pathlib import Path
import secrets
import shutil
import signal
import socket
import subprocess
import sys
import time
import unittest


INSIDE_FLAG = "--inside-network-namespace"
HELPER = Path(__file__).with_name("selective_qdisc.py").resolve()
STATE_DIRECTORY = Path("/run/hummbwtester-qdisc")
TOKEN_ENV = "HUMMBWTESTER_QDISC_TEST_TOKEN"


def run(arguments: list[str], *, check: bool = True) -> subprocess.CompletedProcess[str]:
    return subprocess.run(arguments, check=check, text=True, capture_output=True)


def run_json(arguments: list[str]) -> object:
    result = run(arguments)
    return json.loads(result.stdout or "[]")


def test_names(token: str) -> tuple[str, str, str]:
    return (f"itest-{token}-v4", f"itest-{token}-v6", f"itest-{token}-rollback")


def remove_test_state(token: str) -> None:
    for name in test_names(token):
        (STATE_DIRECTORY / f"{name}.json").unlink(missing_ok=True)
    try:
        STATE_DIRECTORY.rmdir()
    except OSError:
        pass


class SelectiveQdiscIntegrationTest(unittest.TestCase):
    peer: subprocess.Popen[str]
    receiver: subprocess.Popen[str]

    @classmethod
    def setUpClass(cls) -> None:
        cls.token = os.environ[TOKEN_ENV]
        cls.ipv4_name, cls.ipv6_name, cls.rollback_name = test_names(cls.token)
        cls.peer = subprocess.Popen([
            "unshare", "--net", sys.executable, "-c",
            "import signal; signal.pause()",
        ], text=True)
        cls.receiver = None  # type: ignore[assignment]
        try:
            cls._wait_for_peer_namespace()
            cls._create_links()
            cls._start_receiver()
        except BaseException:
            cls._cleanup()
            raise

    @classmethod
    def tearDownClass(cls) -> None:
        cls._cleanup()

    @classmethod
    def _wait_for_peer_namespace(cls) -> None:
        own_namespace = os.readlink("/proc/self/ns/net")
        peer_namespace = Path(f"/proc/{cls.peer.pid}/ns/net")
        for _ in range(200):
            if cls.peer.poll() is not None:
                raise RuntimeError("peer network-namespace process exited early")
            try:
                if os.readlink(peer_namespace) != own_namespace:
                    return
            except FileNotFoundError:
                pass
            time.sleep(0.01)
        raise RuntimeError("timed out waiting for peer network namespace")

    @classmethod
    def _ip(cls, *arguments: str, peer: bool = False) -> None:
        command = ["ip", *arguments]
        if peer:
            command = ["nsenter", "-t", str(cls.peer.pid), "-n", *command]
        run(command)

    @classmethod
    def _create_links(cls) -> None:
        for local, remote in (("tx4", "rx4"), ("tx6", "rx6"), ("txr", "rxr")):
            cls._ip("link", "add", local, "type", "veth", "peer", "name", remote)
            cls._ip("link", "set", remote, "netns", str(cls.peer.pid))

        cls._ip("address", "add", "192.0.2.1/30", "dev", "tx4")
        cls._ip("link", "set", "tx4", "up")
        cls._ip("address", "add", "192.0.2.2/30", "dev", "rx4", peer=True)
        cls._ip("link", "set", "rx4", "up", peer=True)

        cls._ip("link", "set", "tx6", "up")
        cls._ip("-6", "address", "add", "fe80::1/64", "dev", "tx6", "nodad")
        cls._ip("link", "set", "rx6", "up", peer=True)
        cls._ip(
            "-6", "address", "add", "fe80::2/64", "dev", "rx6", "nodad", peer=True,
        )

        cls._ip("address", "add", "198.51.100.1/30", "dev", "txr")
        cls._ip("link", "set", "txr", "up")
        cls._ip("address", "add", "198.51.100.2/30", "dev", "rxr", peer=True)
        cls._ip("link", "set", "rxr", "up", peer=True)
        cls._ip("link", "set", "lo", "up", peer=True)

    @classmethod
    def _start_receiver(cls) -> None:
        receiver_code = """
import select
import socket

ipv4 = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
ipv4.bind(("192.0.2.2", 45000))
ipv6 = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
ipv6.setsockopt(socket.IPPROTO_IPV6, socket.IPV6_V6ONLY, 1)
ipv6.bind(("fe80::2", 45030, 0, socket.if_nametoindex("rx6")))
print("ready", flush=True)
while True:
    readable, _, _ = select.select([ipv4, ipv6], [], [])
    for connection in readable:
        connection.recvfrom(65535)
"""
        cls.receiver = subprocess.Popen([
            "nsenter", "-t", str(cls.peer.pid), "-n", sys.executable, "-c", receiver_code,
        ], text=True, stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        assert cls.receiver.stdout is not None
        if cls.receiver.stdout.readline().strip() != "ready":
            stderr = cls.receiver.stderr.read() if cls.receiver.stderr else ""
            raise RuntimeError(f"receiver failed to start: {stderr}")

    @classmethod
    def _helper(
        cls, *arguments: str, check: bool = True,
    ) -> subprocess.CompletedProcess[str]:
        return run([sys.executable, str(HELPER), *arguments], check=check)

    @classmethod
    def _down(cls, name: str) -> None:
        cls._helper("down", "--name", name, check=False)

    @classmethod
    def _cleanup(cls) -> None:
        for attribute in ("ipv4_name", "ipv6_name", "rollback_name"):
            name = getattr(cls, attribute, None)
            if name:
                cls._down(name)
        receiver = getattr(cls, "receiver", None)
        if receiver is not None:
            receiver.send_signal(signal.SIGTERM)
            try:
                receiver.wait(timeout=2)
            except subprocess.TimeoutExpired:
                receiver.kill()
                receiver.wait()
        peer = getattr(cls, "peer", None)
        if peer is not None:
            peer.send_signal(signal.SIGTERM)
            try:
                peer.wait(timeout=2)
            except subprocess.TimeoutExpired:
                peer.kill()
                peer.wait()
        remove_test_state(getattr(cls, "token", ""))

    def _root_kind(self, device: str) -> str:
        qdiscs = run_json(["tc", "-j", "qdisc", "show", "dev", device])
        roots = [entry for entry in qdiscs if entry.get("root") is True]
        self.assertEqual(len(roots), 1)
        return roots[0]["kind"]

    def _assert_hierarchy(self, device: str, protocol: str, source: str) -> None:
        qdiscs = run_json(["tc", "-j", "-s", "qdisc", "show", "dev", device])
        self.assertTrue(any(
            entry.get("root") is True and entry.get("kind") == "prio" and
            entry.get("handle") == "1:"
            for entry in qdiscs
        ))
        self.assertTrue(any(
            entry.get("kind") == "tbf" and entry.get("parent") == "1:1"
            for entry in qdiscs
        ))
        filters = run_json([
            "tc", "-j", "-d", "filter", "show", "dev", device, "parent", "1:",
        ])
        rendered = json.dumps(filters)
        self.assertIn(f'"protocol": "{protocol}"', rendered)
        self.assertIn(f'"src_ip": "{source}"', rendered)
        self.assertIn('"ip_proto": "udp"', rendered)
        self.assertIn('"classid": "1:1"', rendered)

    def test_ipv4_ipv6_selection_backpressure_and_cleanup(self) -> None:
        self._helper(
            "diagnose", "--name", self.ipv4_name,
            "--device", "tx4", "--local", "192.0.2.1:45001",
            "--remote", "192.0.2.2:45000", "--expected-root", "noqueue",
        )
        self._helper(
            "diagnose", "--name", self.ipv6_name,
            "--device", "tx6", "--local", "[fe80::1%tx6]:45031",
            "--remote", "[fe80::2]:45030", "--expected-root", "noqueue",
        )
        self._helper(
            "up", "--name", self.ipv4_name,
            "--device", "tx4", "--local", "192.0.2.1:45001",
            "--remote", "192.0.2.2:45000", "--expected-root", "noqueue",
            "--rate", "100kbit", "--burst", "2kb", "--limit", "8mb",
        )
        self._helper(
            "up", "--name", self.ipv6_name,
            "--device", "tx6", "--local", "[fe80::1%tx6]:45031",
            "--remote", "[fe80::2]:45030", "--expected-root", "noqueue",
            "--rate", "100kbit", "--burst", "2kb", "--limit", "8mb",
        )
        try:
            self._assert_hierarchy("tx4", "ip", "192.0.2.1")
            self._assert_hierarchy("tx6", "ipv6", "fe80::1")
            self._send_ipv6_packet()
            self._assert_ipv4_backpressure()
        finally:
            self._down(self.ipv4_name)
            self._down(self.ipv6_name)
        self.assertEqual(self._root_kind("tx4"), "noqueue")
        self.assertEqual(self._root_kind("tx6"), "noqueue")
        self.assertFalse((STATE_DIRECTORY / f"{self.ipv4_name}.json").exists())
        self.assertFalse((STATE_DIRECTORY / f"{self.ipv6_name}.json").exists())

    def _send_ipv6_packet(self) -> None:
        connection = socket.socket(socket.AF_INET6, socket.SOCK_DGRAM)
        self.addCleanup(connection.close)
        index = socket.if_nametoindex("tx6")
        connection.bind(("fe80::1", 45031, 0, index))
        connection.sendto(b"ipv6", ("fe80::2", 45030, 0, index))

    def _assert_ipv4_backpressure(self) -> None:
        connection = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
        self.addCleanup(connection.close)
        connection.bind(("192.0.2.1", 45001))
        connection.setsockopt(socket.SOL_SOCKET, socket.SO_SNDBUF, 16384)
        connection.setblocking(False)
        blocked_errno = None
        for _ in range(200_000):
            try:
                connection.sendto(b"x" * 1400, ("192.0.2.2", 45000))
            except BlockingIOError as err:
                blocked_errno = err.errno
                break
        self.assertEqual(blocked_errno, errno.EAGAIN)
        qdiscs = run_json(["tc", "-j", "-s", "qdisc", "show", "dev", "tx4"])
        tbf = next(entry for entry in qdiscs if entry.get("kind") == "tbf")
        self.assertEqual(tbf.get("drops"), 0)
        self.assertGreater(tbf.get("backlog", 0), 0)

    def test_partial_install_rolls_back(self) -> None:
        result = self._helper(
            "up", "--name", self.rollback_name,
            "--device", "txr", "--local", "198.51.100.1:46001",
            "--remote", "198.51.100.2:46000", "--expected-root", "noqueue",
            "--rate", "10mbit", "--burst", "0b", "--limit", "1mb",
            check=False,
        )
        self.assertNotEqual(result.returncode, 0)
        self.assertEqual(self._root_kind("txr"), "noqueue")
        self.assertFalse((STATE_DIRECTORY / f"{self.rollback_name}.json").exists())


def main() -> int:
    missing = [command for command in ("ip", "nsenter", "tc", "unshare")
               if shutil.which(command) is None]
    if missing:
        print("missing required commands: " + ", ".join(missing), file=sys.stderr)
        return 1
    if INSIDE_FLAG in sys.argv:
        sys.argv.remove(INSIDE_FLAG)
        unittest.main()
        return 0
    if os.geteuid() != 0:
        print(
            "this integration test needs CAP_NET_ADMIN; run it with sudo as shown in its docstring",
            file=sys.stderr,
        )
        return 1
    token = secrets.token_hex(4)
    environment = os.environ.copy()
    environment[TOKEN_ENV] = token
    try:
        result = subprocess.run([
            "unshare", "--net", "--mount", "--mount-proc",
            sys.executable, str(Path(__file__).resolve()), INSIDE_FLAG,
        ], env=environment, check=False)
        return result.returncode
    finally:
        remove_test_state(token)


if __name__ == "__main__":
    raise SystemExit(main())
