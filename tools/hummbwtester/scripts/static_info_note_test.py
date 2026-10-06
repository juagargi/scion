import json
import os
from pathlib import Path
import tempfile
import unittest

from tools.hummbwtester.scripts import static_info_note as note


ENTRY = {
    "name": "hummbwtester",
    "api_protocol": "connectrpc/TLS/QUIC/SCION",
    "api_address": "[71-2:0:5c,127.0.0.1]:31888",
    "client_registration_website": "https://127.0.0.1:8888",
}
OTHER = {"name": "other", "api_protocol": "connectrpc/TLS/TCP",
         "api_address": "https://other.invalid", "client_registration_website": ""}


def hummingbird(config):
    return json.loads(config["Note"]).get("hummingbird")


class StaticInfoNoteTest(unittest.TestCase):
    def test_ensure_keeps_other_settings_and_note_keys(self):
        config = {
            "Latency": {"1": {"Inter": "41ms"}},
            "Note": json.dumps({"info": {"name": "UFMS"}, "hummingbird": [OTHER]}),
        }
        self.assertTrue(note.ensure_entry(config, ENTRY))
        self.assertEqual(config["Latency"], {"1": {"Inter": "41ms"}})
        self.assertEqual(json.loads(config["Note"])["info"], {"name": "UFMS"})
        self.assertEqual(hummingbird(config), [ENTRY, OTHER])
        self.assertFalse(note.ensure_entry(config, ENTRY))

    def test_ensure_replaces_stale_entries_of_same_name_or_address(self):
        stale = {**ENTRY, "client_registration_website": "https://old.invalid"}
        renamed = {**ENTRY, "name": "SCIERA Market"}
        config = {"Note": json.dumps({"hummingbird": [stale, OTHER, renamed]})}
        self.assertTrue(note.ensure_entry(config, ENTRY))
        self.assertEqual(hummingbird(config), [ENTRY, OTHER])

    def test_ensure_creates_note_in_empty_config_and_honors_key_spelling(self):
        config = {}
        self.assertTrue(note.ensure_entry(config, ENTRY))
        self.assertEqual(hummingbird(config), [ENTRY])
        lowercase = {"note": ""}
        note.ensure_entry(lowercase, ENTRY)
        self.assertEqual(list(lowercase), ["note"])

    def test_refuses_note_that_is_not_a_json_object(self):
        for value in ("free text", json.dumps(["list"]), json.dumps({"hummingbird": {}})):
            with self.subTest(value=value), self.assertRaises(note.Error):
                note.ensure_entry({"Note": value}, ENTRY)

    def test_remove_drops_only_named_entries_and_empty_keys(self):
        config = {"Note": json.dumps({"info": {}, "hummingbird": [ENTRY, OTHER]})}
        self.assertTrue(note.remove_entry(config, "hummbwtester"))
        self.assertEqual(hummingbird(config), [OTHER])
        self.assertFalse(note.remove_entry(config, "hummbwtester"))
        only_ours = {"Note": json.dumps({"hummingbird": [ENTRY]})}
        self.assertTrue(note.remove_entry(only_ours, "hummbwtester"))
        self.assertEqual(only_ours, {})

    def test_store_keeps_mode_and_removes_emptied_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "staticInfoConfig.json"
            path.write_text("{}")
            os.chmod(path, 0o640)
            note.store(path, {"Note": ""})
            self.assertEqual(path.stat().st_mode & 0o777, 0o640)
            self.assertEqual(json.loads(path.read_text()), {"Note": ""})
            note.store(path, {})
            self.assertFalse(path.exists())


if __name__ == "__main__":
    unittest.main()
