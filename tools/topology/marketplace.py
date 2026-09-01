# Copyright 2026 ETH Zurich
#
# Licensed under the Apache License, Version 2.0 (the "License");
# you may not use this file except in compliance with the License.
# You may obtain a copy of the License at
#
#   http://www.apache.org/licenses/LICENSE-2.0
#
# Unless required by applicable law or agreed to in writing, software
# distributed under the License is distributed on an "AS IS" BASIS,
# WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
# See the License for the specific language governing permissions and
# limitations under the License.
"""
:mod:`marketplace` --- SCION topology marketplace generator
===========================================================

Everything a marketplace needs to run on a generated topology:
its config file, the entries advertising it to the other ASes,
the service or program running it,
and the entries its database starts out with.

Besides the topology generator, this module is imported by marketplace/tools/setup_marketplace.py,
which adds a marketplace to an already generated topology,
and by marketplace/tools/populate_marketplace.py,
which rebuilds its database.
Both run on the system interpreter, so the module keeps to the standard library and bcrypt.
"""
# Stdlib
import base64
import hashlib
import json
import math
import os
import re
import shlex
import sqlite3
from collections import namedtuple
from datetime import datetime, timedelta, timezone
from pathlib import Path

# External packages
import bcrypt

# SCION
from topology.common import ArgsTopoDicts, TopoID
from topology.scion_addr import ISD_AS
from topology.util import write_file

MARKETPLACE_CONFIG_NAME = 'marketplace.toml'
STATIC_INFO_CONFIG_NAME = 'staticInfoConfig.json'
MARKETPLACE_DB_NAME = 'marketplace.db'

# The schema the marketplace applies itself on startup. Prepopulating the
# database means applying it here, before the marketplace has ever run.
MARKETPLACE_SCHEMA = os.path.join('marketplace', 'db', 'schema.sql')

# The port of both APIs, the TCP one serving the ConnectRPC API and the web app
# from a single mux, and the SCION/QUIC one, which is UDP. They do not collide.
# It has to be inside the dispatched_ports range of the AS, otherwise only the
# shim dispatcher can deliver SCION packets to it. See DefaultAddr in
# marketplace/config.go.
MARKETPLACE_PORT = 31888

# Where the marketplace of a docker topology finds its files inside the container.
CONTAINER_CONFIG_DIR = '/etc/scion'
CONTAINER_CACHE_DIR = '/share/cache'
CONTAINER_TOML = '%s/%s' % (CONTAINER_CONFIG_DIR, MARKETPLACE_CONFIG_NAME)
CONTAINER_DB = '%s/%s' % (CONTAINER_CACHE_DIR, MARKETPLACE_DB_NAME)

# The marketplace of a supervisord topology binds the loopback address, and keeps
# its database next to the ones of the other services.
LOCAL_HOST = '127.0.0.1'
LOCAL_CACHE_DIR = 'gen-cache'

# The marketplace program of a supervisord config. There is a single marketplace,
# so unlike the per-AS services it needs no IA in its name.
PROGRAM_NAME = 'marketplace'

# What the marketplace binds, and what staticInfoConfig.json advertises for it.
Endpoints = namedtuple('Endpoints', 'host api_port scion_port')


class MarketplaceError(Exception):
    """An error that aborts the marketplace setup with a message."""


class AssetEvent:
    """The event types of the Asset_Events table.

    The values are the ones the marketplace writes, see AssetEventType in
    marketplace/db/types.go.
    """
    PUBLISHED = 0
    BOUGHT = 1


# The config of the marketplace, as a template rather than a dict, so that
# setup_marketplace.py can tell an unchanged file from a locally edited one.
MARKETPLACE_TOML = """[general]
id = "marketplace"
config_dir = "{config_dir}"

[log.console]
level = "debug"

[marketplace]
api_addr = "{api_addr}"
scion_api_addr = "{scion_api_addr}"
currency = "CHF"
currency_exponent = 2
supports_redemption_delegation = true
delegation_hourly_fee = 10000
transaction_fee_relative = 0.01
transaction_fee_absolute = 10
split_combine_fee_absolute = 10

[marketplace_db]
connection = "{db_path}"
"""


def hostPort(host: str, port: int) -> str:
    """host:port, with an IPv6 host in brackets."""
    if ":" in host:
        return "[%s]:%d" % (host, port)
    return "%s:%d" % (host, port)


def iaNumbers(ia):
    """The ISD and AS numbers of an IA, the way the marketplace database stores them."""
    try:
        isd_as = ISD_AS(str(ia))
    except ValueError:
        raise MarketplaceError("invalid ISD-AS %r" % str(ia))
    as_str = isd_as.as_str()
    if ":" not in as_str:
        return int(isd_as.isd_str()), int(as_str)
    asn = 0
    for part in as_str.split(":"):
        asn = (asn << 16) | int(part, 16)
    return int(isd_as.isd_str()), asn


def marketplaceToml(config_dir: str, endpoints: Endpoints, db_path: str) -> str:
    return MARKETPLACE_TOML.format(
        config_dir=config_dir,
        api_addr=hostPort(endpoints.host, endpoints.api_port),
        scion_api_addr=hostPort(endpoints.host, endpoints.scion_port),
        db_path=db_path,
    )


def marketplaceEntries(ia, endpoints: Endpoints):
    """The staticInfoConfig entries advertising this marketplace.

    They describe what the marketplace really binds: the web app is served by the
    same mux as the TCP API, so both live on the API port.
    """
    website = "https://%s" % hostPort(endpoints.host, endpoints.api_port)
    # The keys are the ones of the Hummingbird APIs document, see the marketplace
    # address block of "Client <-> Marketplace".
    return [
        {
            "name": "Test Market",
            "api_protocol": "connectrpc/TLS/QUIC/SCION",
            "api_address": "[%s,%s]:%d" % (
                ISD_AS(str(ia)), endpoints.host, endpoints.scion_port),
            "client_registration_website": website,
        },
        {
            "name": "Test Market",
            "api_protocol": "connectrpc/TLS/TCP",
            "api_address": website,
            "client_registration_website": website,
        },
    ]


def loadStaticInfo(path):
    """Reads staticInfoConfig.json and its embedded note object.

    Returns (static_info, note), both dicts. A missing file yields empty ones.
    The note is a JSON document of its own, encoded as a string, because the
    control service copies it verbatim into the beacons it propagates.
    """
    path = Path(path)
    if not path.exists():
        return {}, {}

    try:
        static_info = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as e:
        raise MarketplaceError("cannot read %s: %s" % (path, e))
    if not isinstance(static_info, dict):
        raise MarketplaceError(
            "%s: expected a JSON object, got %s" % (path, type(static_info).__name__))

    note = static_info.get("note", "")
    if note == "":
        return static_info, {}
    try:
        note = json.loads(note)
    except (TypeError, json.JSONDecodeError):
        raise MarketplaceError(
            "%s: the 'note' field is not a JSON object; refusing to overwrite it" % path)
    if not isinstance(note, dict):
        raise MarketplaceError(
            "%s: the 'note' field is not a JSON object; refusing to overwrite it" % path)
    return static_info, note


def mergeEntries(existing, entries):
    """Merges the marketplace entries into the existing ones, keyed by name+protocol."""
    merged = [e for e in existing if isinstance(e, dict)]
    changed = len(merged) != len(existing)
    for entry in entries:
        key = (entry["name"], entry["api_protocol"])
        for i, old in enumerate(merged):
            if (old.get("name"), old.get("api_protocol")) == key:
                if old != entry:
                    merged[i] = entry
                    changed = True
                break
        else:
            merged.append(entry)
            changed = True
    return merged, changed


def advertiseMarketplace(path, ia, endpoints: Endpoints) -> bool:
    """Advertises the marketplace in the staticInfoConfig.json at path.

    Whatever else the file already configures is kept, and so are the entries of
    other marketplaces. Returns whether the file was written.
    """
    static_info, note = loadStaticInfo(path)
    hummingbird = note.get("hummingbird", [])
    if not isinstance(hummingbird, list):
        raise MarketplaceError(
            "%s: 'note'.hummingbird is not a list; refusing to overwrite it" % path)

    hummingbird, changed = mergeEntries(hummingbird, marketplaceEntries(ia, endpoints))
    if not changed:
        return False
    note["hummingbird"] = hummingbird
    static_info["note"] = json.dumps(note)
    try:
        write_file(str(path), json.dumps(static_info, indent=2))
    except OSError as e:
        raise MarketplaceError("cannot write %s: %s" % (path, e))
    return True


def checkDispatchedPorts(as_dir, port: int) -> None:
    """Checks that the SCION API port is one the border router delivers directly."""
    topology = Path(as_dir) / "topology.json"
    if not topology.is_file():
        return
    try:
        ports = json.loads(topology.read_text(encoding="utf-8")).get("dispatched_ports")
    except (OSError, json.JSONDecodeError) as e:
        raise MarketplaceError("cannot read %s: %s" % (topology, e))
    if not ports or ports == "all":
        return
    match = re.fullmatch(r"(\d+)-(\d+)", str(ports).strip())
    if match is None:
        return
    start, end = int(match.group(1)), int(match.group(2))
    if not start <= port <= end:
        raise MarketplaceError(
            "the SCION API port %d is outside the dispatched_ports range %s of %s; the "
            "marketplace would only receive SCION packets through a shim dispatcher"
            % (port, ports, topology))


def dockerServiceName(topo_id) -> str:
    return 'marketplace%s' % TopoID(str(topo_id)).file_fmt()


def dockerService(image, control_name, dispatcher_name, volumes, user=None):
    """The compose service running the marketplace of an AS.

    It shares the network namespace of the control service, the way the control
    service and the hummingbird service already share the one of their dispatcher.
    It therefore carries no address of its own, and needs none.
    """
    entry = {
        'command': ['--config', CONTAINER_TOML],
        # The marketplace asks the control service for trust material on startup.
        'depends_on': {control_name: {'condition': 'service_healthy'}},
        'image': image,
        'network_mode': 'service:%s' % dispatcher_name,
        'volumes': volumes,
    }
    if user is not None:
        entry['user'] = user
    return entry


def supervisordProgram(config_path: str):
    """The supervisord settings of the marketplace program."""
    return {
        'autostart': 'false',
        'autorestart': 'false',
        # Like the router, the marketplace is a cgo binary.
        'environment': 'TZ=UTC,GODEBUG="cgocheck=0"',
        'stdout_logfile': 'logs/%s.log' % PROGRAM_NAME,
        'redirect_stderr': True,
        'startretries': 0,
        'startsecs': 5,
        'priority': 100,
        'command': ' '.join(
            shlex.quote(a) for a in ['bin/marketplace', '--config', config_path]),
    }


#
# The default entries of the database.
#

DEFAULT_PASSWORD = "1234"
DEFAULT_USERS = ["alice", "bob"]
DEFAULT_USER_BALANCE = 10**9
DEFAULT_AS_BALANCE = 0
DEFAULT_ASSET_BANDWIDTH = 1000000
DEFAULT_ASSET_BANDWIDTH_MIN = 10
DEFAULT_ASSET_BANDWIDTH_MAX = 1000000
DEFAULT_ASSET_PRICE = 1
DEFAULT_ASSET_TIME_GRANULARITY = 10
DEFAULT_ASSET_TIME_MIN_DURATION = 10
DEFAULT_ASSET_TIME_MAX_DURATION = 60*60*24
DEFAULT_ASSET_DURATION = timedelta(days=100)
DEFAULT_DELEGATION_RES_ID_LIMIT_LOW = 0
DEFAULT_DELEGATION_RES_ID_LIMIT_HIGH = 1000

# The flyover carries the bandwidth as a 10 bit codepoint into the points
# published by the AS, so there is one point per codepoint. The values must be
# the ones the router decodes, see ConvertBW in router/tokenbucket/tokenbucket.go.
BW_CODEPOINTS = 1 << 10
MIN_BW_KBPS = 10
MAX_BW_KBPS = 10_000_000
BW_LOG_ENCODING_START = 60

# Derivation of the Hummingbird secret value of an AS from its master key, see
# DeriveSecretValue in pkg/slayers/path/hummingbird/mac.go.
SECRET_VALUE_SALT = b"Derive hbird sv"
SECRET_VALUE_ITERATIONS = 1000
SECRET_VALUE_LENGTH = 16


def loadJson(path):
    with open(path, "r", encoding="utf-8") as file:
        return json.load(file)


def loadTopology(gen_dir):
    """Reads the IA, interface IDs and directory of every AS of a topology."""
    topologies = sorted(Path(gen_dir).glob("AS*/topology.json"))
    if not topologies:
        raise MarketplaceError("no AS*/topology.json found in %s" % gen_dir)
    ases = []
    for path in topologies:
        try:
            topo = loadJson(path)
        except (OSError, json.JSONDecodeError) as e:
            raise MarketplaceError("cannot read %s: %s" % (path, e))
        ia = topo.get("isd_as")
        if ia is None:
            raise MarketplaceError("%s has no isd_as" % path)
        ifids = sorted(
            int(ifid)
            for router in topo.get("border_routers", {}).values()
            for ifid in router.get("interfaces", {})
        )
        ases.append((ia, ifids, path.parent))
    return ases


def encodingPoints():
    """The bandwidth points of an AS, in kbps, one per codepoint.

    The first BW_LOG_ENCODING_START points are one kbps apart, starting at
    MIN_BW_KBPS, and the rest grow geometrically up to MAX_BW_KBPS. The router
    decodes a flyover with the very same points, so publishing anything else
    would sell a bandwidth that the router does not enforce.
    """
    step = (MAX_BW_KBPS / (MIN_BW_KBPS + BW_LOG_ENCODING_START)) ** (
        1.0 / (BW_CODEPOINTS - BW_LOG_ENCODING_START - 1)
    )
    points = []
    for codepoint in range(BW_CODEPOINTS):
        if codepoint < BW_LOG_ENCODING_START:
            points.append(MIN_BW_KBPS + codepoint)
        else:
            kbps = (MIN_BW_KBPS + BW_LOG_ENCODING_START) * step ** (
                codepoint - BW_LOG_ENCODING_START
            )
            points.append(math.ceil(kbps))
    return points


def secretValue(as_dir):
    """Derives the Hummingbird secret value of an AS from its master key.

    Handing it to the marketplace is what lets the marketplace redeem the assets
    of that AS, i.e. derive the authenticator of a flyover on its behalf.
    """
    path = Path(as_dir) / "keys" / "master0.key"
    try:
        master = base64.b64decode(path.read_text(encoding="utf-8").strip(), validate=True)
    except OSError as e:
        raise MarketplaceError("cannot read the master key %s: %s" % (path, e))
    except ValueError as e:
        raise MarketplaceError("%s is not a base64 encoded key: %s" % (path, e))
    return hashlib.pbkdf2_hmac(
        "sha256", master, SECRET_VALUE_SALT, SECRET_VALUE_ITERATIONS, SECRET_VALUE_LENGTH
    )


def interfacePairs(ifids):
    """The (ingress, egress) pairs of an AS.

    Interface 0 stands for no interface, i.e. a flyover that starts or ends in
    this AS, so that ASes with a single interface get assets as well.
    """
    pairs = [(i, e) for i in ifids for e in ifids if i != e]
    pairs += [(0, e) for e in ifids]
    pairs += [(i, 0) for i in ifids]
    return pairs


def defaultEntries(gen_dir, now=None):
    """Builds the default users, ASes, assets and delegations for a topology."""
    if now is None:
        now = datetime.now(timezone.utc)
    startsAt = now.replace(microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ")
    stopsAt = (now + DEFAULT_ASSET_DURATION).replace(microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ")

    ases = loadTopology(gen_dir)
    assets = [
        {
            "ia": ia,
            "bandwidth": DEFAULT_ASSET_BANDWIDTH,
            "bandwidth_min": DEFAULT_ASSET_BANDWIDTH_MIN,
            "bandwidth_max": DEFAULT_ASSET_BANDWIDTH_MAX,
            "price": DEFAULT_ASSET_PRICE,
            "time_granularity": DEFAULT_ASSET_TIME_GRANULARITY,
            "time_min_duration": DEFAULT_ASSET_TIME_MIN_DURATION,
            "time_max_duration": DEFAULT_ASSET_TIME_MAX_DURATION,
            "starts_at": startsAt,
            "stops_at": stopsAt,
            "ingress": ingress,
            "egress": egress,
        }
        for ia, ifids, _ in ases
        for ingress, egress in interfacePairs(ifids)
    ]
    return {
        "users": [
            {"name": name, "password": DEFAULT_PASSWORD}
            for name in DEFAULT_USERS
        ],
        "accounts": [
            {"user": name, "balance": DEFAULT_USER_BALANCE}
            for name in DEFAULT_USERS
        ],
        "ases": [
            {"ia": ia, "password": DEFAULT_PASSWORD, "balance": DEFAULT_AS_BALANCE}
            for ia, _, _ in ases
        ],
        "assets": assets,
        # Every AS delegates the redemption of its assets to the marketplace.
        # Without this the marketplace cannot derive the flyover authenticators,
        # and the assets it sells are worthless.
        "delegations": [
            {
                "ia": ia,
                "res_id_limit_low": DEFAULT_DELEGATION_RES_ID_LIMIT_LOW,
                "res_id_limit_high": DEFAULT_DELEGATION_RES_ID_LIMIT_HIGH,
                "expiration": stopsAt,
                "paid_until": stopsAt,
                "key": secretValue(as_dir).hex(),
                "encodings": encodingPoints(),
            }
            for ia, _, as_dir in ases
        ],
    }


#
# Filling the database.
#

def schemaHash(schema_path):
    """The hash identifying a schema, i.e. the version an import has to match."""
    try:
        with open(schema_path, "r", encoding="utf-8") as f:
            sql = f.read()
    except OSError as e:
        raise MarketplaceError("cannot read the schema %s: %s" % (schema_path, e))
    normalized = re.sub(r"\s+", " ", sql).strip()
    return hashlib.sha256(normalized.encode("utf-8")).hexdigest()


def verifySchema(schema_path, version):
    actual = schemaHash(schema_path)
    if version != actual:
        raise MarketplaceError("schema version mismatch; expected %s" % actual)


def applySchema(db, schema_path):
    try:
        with open(schema_path, "r", encoding="utf-8") as f:
            db.executescript(f.read())
    except OSError as e:
        raise MarketplaceError("cannot read the schema %s: %s" % (schema_path, e))


def hashPassword(password):
    if password is None:
        return None
    return bcrypt.hashpw(password.encode("utf-8"), bcrypt.gensalt(rounds=12)).decode("utf-8")


def dropTables(db):
    """Drops every table, so that the schema and the entries are recreated.

    Indexes are dropped along with their table, and so are the AUTOINCREMENT
    counters that SQLite keeps in its internal sqlite_sequence table.
    """
    tables = [
        name for (name,) in db.execute(
            "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%'"
        )
    ]
    for table in tables:
        db.execute('DROP TABLE "%s"' % table)
    return tables


def insertUsers(db, users):
    if users is None:
        return
    db.executemany(
        """
        INSERT INTO Users (name, pw_hash)
        VALUES (?, ?)
        ON CONFLICT(name) DO UPDATE SET pw_hash = excluded.pw_hash
        """,
        [(u.get("name"), hashPassword(u.get("password"))) for u in users],
    )


def insertAccounts(db, accounts):
    if accounts is None:
        return
    # Updated instead of replaced: a replace would delete the conflicting row and
    # insert it under a new id, orphaning the assets and reservations that refer
    # to it.
    db.executemany(
        """
        INSERT INTO Accounts (scope, balance, user_id)
        VALUES (?, ?, (
            SELECT id
            FROM Users
            WHERE name = ?
            LIMIT 1
        ))
        ON CONFLICT(user_id, scope) DO UPDATE SET balance = excluded.balance
        """,
        [(a.get("scope") or "", a.get("balance"), a.get("user")) for a in accounts],
    )


def insertASes(db, ases):
    if ases is None:
        return
    rows = []
    for a in ases:
        isd_id, as_id = iaNumbers(a.get("ia"))
        rows.append((
            isd_id,
            as_id,
            hashPassword(a.get("password")) or "",
            a.get("jwt_version") or 0,
            a.get("balance") or 0,
        ))
    db.executemany(
        """
        INSERT OR REPLACE INTO Ases (isd_id, as_id, pw_hash, jwt_version, balance)
        VALUES (?, ?, ?, ?, ?)
        """,
        rows,
    )


def insertAssets(db, assets):
    if assets is None:
        return
    rows = []
    events = []
    for a in assets:
        isd_id, as_id = iaNumbers(a.get("ia"))
        rows.append((
            isd_id, as_id, a.get("bandwidth"), a.get("bandwidth_min"), a.get("bandwidth_max"),
            a.get("price"), a.get("time_granularity"), a.get("time_min_duration"),
            a.get("time_max_duration"), a.get("starts_at"), a.get("stops_at"),
            a.get("ingress"), a.get("egress"), a.get("owner"),
        ))
        # The statistics of an AS are computed from the events,
        # not from the assets still on sale, so publishing one has to be recorded as well.
        # An asset that already has an owner means it was bought, which is a second event.
        events.append((AssetEvent.PUBLISHED, isd_id, as_id, a.get("ingress"),
                       a.get("egress"), a.get("bandwidth"), a.get("starts_at"),
                       a.get("stops_at"), a.get("price")))
        if a.get("owner") is not None:
            events.append((AssetEvent.BOUGHT, isd_id, as_id, a.get("ingress"),
                           a.get("egress"), a.get("bandwidth"), a.get("starts_at"),
                           a.get("stops_at"), a.get("price")))
    db.executemany(
        """
        INSERT INTO Assets (isd_id, as_id, bandwidth, bandwidth_min, bandwidth_max, price,
                            time_granularity, time_min_duration, time_max_duration, starts_at,
                            stops_at, ingress, egress, account_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (
            SELECT a.id
            FROM Accounts a
            JOIN Users u ON u.ID = a.user_id
            WHERE u.name = ? AND a.scope = ''
            LIMIT 1
        ))
        """,
        rows,
    )
    db.executemany(
        """
        INSERT INTO Asset_Events (event_type, isd_id, as_id, ingress, egress, bandwidth,
                                  starts_at, stops_at, price)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        """,
        events,
    )


def insertReservations(db, reservations):
    if reservations is None:
        return
    rows = []
    for r in reservations:
        isd_id, as_id = iaNumbers(r.get("ia"))
        rows.append((
            r.get("id"), isd_id, as_id, r.get("ingress"), r.get("egress"), r.get("bandwidth"),
            r.get("bw_encoded"), r.get("starts_at"), r.get("stops_at"),
            bytes.fromhex(r.get("key")), r.get("owner"),
        ))
    db.executemany(
        """
        INSERT OR REPLACE INTO Reservations (reservation_id, isd_id, as_id, ingress, egress,
                                             bandwidth, bw_encoded, starts_at, stops_at,
                                             key, account_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (
            SELECT id
            FROM Users
            WHERE name = ?
            LIMIT 1
        ))
        """,
        rows,
    )


def insertDelegations(db, delegations):
    if delegations is None:
        return
    rows = []
    for d in delegations:
        isd_id, as_id = iaNumbers(d.get("ia"))
        encodings = b"".join(
            i.to_bytes(4, byteorder="little") for i in d.get("encodings")
        )
        rows.append((
            isd_id, as_id, d.get("res_id_limit_low"), d.get("res_id_limit_high"),
            d.get("expiration"), d.get("paid_until"), bytes.fromhex(d.get("key")), encodings,
        ))
    db.executemany(
        """
        INSERT OR REPLACE INTO Redemption_Delegations (isd_id, as_id, res_id_limit_low,
            res_id_limit_high, expiration, paid_until, key, encodings)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?)
        """,
        rows,
    )


def insertAll(db, data):
    insertUsers(db, data.get("users"))
    insertAccounts(db, data.get("accounts"))
    insertASes(db, data.get("ases"))
    insertAssets(db, data.get("assets"))
    insertReservations(db, data.get("reservations"))
    insertDelegations(db, data.get("delegations"))


def populateDB(db_path, schema_path, data):
    """Rebuilds the marketplace database from the given entries.

    Every table is dropped first, so that both the schema and the entries are the
    ones of this run, and not the leftovers of a previous one. Returns the names
    of the tables that were dropped.
    """
    Path(db_path).parent.mkdir(parents=True, exist_ok=True)
    try:
        conn = sqlite3.connect(db_path)
    except sqlite3.Error as e:
        raise MarketplaceError("cannot open the database %s: %s" % (db_path, e))
    try:
        cursor = conn.cursor()
        dropped = dropTables(cursor)
        applySchema(cursor, schema_path)
        insertAll(cursor, data)
        conn.commit()
        return dropped
    except sqlite3.Error as e:
        conn.rollback()
        raise MarketplaceError("cannot fill the database %s: %s" % (db_path, e))
    except Exception:
        conn.rollback()
        raise
    finally:
        conn.close()


#
# The generator, run as part of the topology generation.
#

class MarketplaceGenArgs(ArgsTopoDicts):
    def __init__(self, args, topo_dicts, networks):
        """
        :param object args: Contains the passed command line arguments as named attributes.
        :param dict topo_dicts: The generated topo dicts from TopoGenerator.
        :param dict networks: The generated networks from SubnetGenerator.
        """
        super().__init__(args, topo_dicts)
        self.networks = networks


class MarketplaceGenerator(object):
    def __init__(self, args):
        """
        :param MarketplaceGenArgs args: Contains the passed command line arguments,
        the topo dicts and the networks.
        """
        self.args = args
        self.output_base = os.environ.get('SCION_OUTPUT_BASE', os.getcwd())

    def generate(self):
        """Writes the config of the marketplace and fills its database.

        The service or program running the marketplace is added by the backend generator,
        so that it lands in the compose file or the supervisord config along
        with the services of its AS.
        """
        topo_id = marketplace_topo_id(self.args)
        base = topo_id.base_dir(self.args.output_dir)
        endpoints = self._endpoints(topo_id)
        checkDispatchedPorts(base, endpoints.scion_port)

        if self.args.docker:
            config_dir, db_path = CONTAINER_CONFIG_DIR, CONTAINER_DB
        else:
            config_dir = base
            db_path = os.path.join(LOCAL_CACHE_DIR, MARKETPLACE_DB_NAME)
        write_file(os.path.join(base, MARKETPLACE_CONFIG_NAME),
                   marketplaceToml(config_dir, endpoints, db_path))

        # Every AS of the topology delegates its redemptions to this marketplace,
        # so every AS must advertise it.
        # The control service copies the note into the beacons it propagates,
        # which is how the ASes of a path tell a client where to buy.
        for as_topo_id in self.args.topo_dicts:
            advertiseMarketplace(
                os.path.join(as_topo_id.base_dir(self.args.output_dir),
                             STATIC_INFO_CONFIG_NAME),
                topo_id, endpoints)

        # The marketplace applies the schema itself, but it cannot invent the
        # entries: they are the ones a local topology is expected to start with.
        populateDB(
            os.path.join(self.output_base, LOCAL_CACHE_DIR, MARKETPLACE_DB_NAME),
            MARKETPLACE_SCHEMA,
            defaultEntries(self.args.output_dir),
        )

    def _endpoints(self, topo_id) -> Endpoints:
        if not self.args.docker:
            return Endpoints(LOCAL_HOST, MARKETPLACE_PORT, MARKETPLACE_PORT)
        # The marketplace joins the network namespace of the control service, so
        # it binds the very same address, and only its ports set it apart.
        name = control_service_name(topo_id)
        for net_desc in self.args.networks.values():
            if name in net_desc.ip_net:
                return Endpoints(str(net_desc.ip_net[name].ip),
                                 MARKETPLACE_PORT, MARKETPLACE_PORT)
        raise MarketplaceError("no address generated for %s" % name)


def control_service_name(topo_id) -> str:
    """The control service the marketplace of an AS shares its address with.

    Only a single control service instance per AS is currently supported, and the
    dispatcher owning the network namespace is named after it.
    """
    return 'cs%s-1' % TopoID(str(topo_id)).file_fmt()


def marketplace_topo_id(args):
    """The TopoID of the AS hosting the marketplace, or None if there is none."""
    if not getattr(args, 'marketplace', None):
        return None
    return TopoID(args.marketplace)


def hosts_marketplace(args, topo_id) -> bool:
    """Whether topo_id is the AS hosting the marketplace."""
    return marketplace_topo_id(args) == topo_id
