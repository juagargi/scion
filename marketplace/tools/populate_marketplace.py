#!/usr/bin/env python3

import base64
import json
import math
import sqlite3
import hashlib
import bcrypt
import argparse
import re
from datetime import datetime, timedelta, timezone
from pathlib import Path

"""
An account without a scope, or with an empty one, is the main account of its
user; there can be only one per user. Assets and reservations given an "owner"
are owned by the main account of that user.

Example JSON file:
{
    "version": "390412f2780897af26f11debb387aed30a2e52eefcca33d9fc204b9f1217011b",
    "users": [
        {
            "name": "Alice",
            "password": "1234"
        }
    ],
    "accounts": [
        {
            "user": "Alice",
            "balance": 1000000000
        },
        {
            "user": "Alice",
            "scope": "bandwidth-tester",
            "balance": 100000
        }
    ],
    "ases": [
        {
            "ia": "1-ff00:0:110"
        }
    ],
    "assets": [
        {
            "ia": "1-ff00:0:110",
            "bandwidth": 1000,
            "bandwidth_min": 10,
            "bandwidth_max": 1000,
            "price": 1,
            "time_granularity": 5,
            "time_min_duration": 5,
            "starts_at": "2026-01-01T00:00:00Z",
            "stops_at": "2027-01-01T00:00:00Z",
            "ingress": 1
        },
        {
            "ia": "1-ff00:0:110",
            "bandwidth": 1000,
            "bandwidth_min": 10,
            "bandwidth_max": 1000,
            "price": 1,
            "time_granularity": 5,
            "time_min_duration": 5,
            "starts_at": "2026-01-01T00:00:00Z",
            "stops_at": "2027-01-01T00:00:00Z",
            "ingress": 2
        },
        {
            "ia": "1-ff00:0:110",
            "bandwidth": 1000,
            "bandwidth_min": 10,
            "bandwidth_max": 1000,
            "price": 1,
            "time_granularity": 5,
            "time_min_duration": 5,
            "starts_at": "2026-01-01T00:00:00Z",
            "stops_at": "2027-01-01T00:00:00Z",
            "egress": 1
        },
        {
            "ia": "1-ff00:0:110",
            "bandwidth": 1000,
            "bandwidth_min": 10,
            "bandwidth_max": 1000,
            "price": 1,
            "time_granularity": 5,
            "time_min_duration": 5,
            "starts_at": "2026-01-01T00:00:00Z",
            "stops_at": "2027-01-01T00:00:00Z",
            "egress": 2
        },
        {
            "ia": "1-ff00:0:110",
            "bandwidth": 1000,
            "bandwidth_min": 10,
            "bandwidth_max": 1000,
            "price": 1,
            "time_granularity": 5,
            "time_min_duration": 5,
            "starts_at": "2026-01-01T00:00:00Z",
            "stops_at": "2027-01-01T00:00:00Z",
            "ingress": 1,
            "egress": 0
        },
        {
            "ia": "1-ff00:0:110",
            "bandwidth": 1000,
            "bandwidth_min": 10,
            "bandwidth_max": 1000,
            "price": 1,
            "time_granularity": 5,
            "time_min_duration": 5,
            "starts_at": "2026-01-01T00:00:00Z",
            "stops_at": "2027-01-01T00:00:00Z",
            "ingress": 2,
            "egress": 0
        },
        {
            "ia": "1-ff00:0:110",
            "bandwidth": 1000,
            "bandwidth_min": 10,
            "bandwidth_max": 1000,
            "price": 1,
            "time_granularity": 5,
            "time_min_duration": 5,
            "starts_at": "2026-01-01T00:00:00Z",
            "stops_at": "2027-01-01T00:00:00Z",
            "ingress": 0,
            "egress": 1
        },
        {
            "ia": "1-ff00:0:110",
            "bandwidth": 1000,
            "bandwidth_min": 10,
            "bandwidth_max": 1000,
            "price": 1,
            "time_granularity": 5,
            "time_min_duration": 5,
            "starts_at": "2026-01-01T00:00:00Z",
            "stops_at": "2027-01-01T00:00:00Z",
            "ingress": 0,
            "egress": 2
        }
    ],
    "reservations": [
        {
            "id": 1,
            "ia": "1-ff00:0:110",
            "owner": "Alice",
            "ingress": 1,
            "egress": 2,
            "bandwidth": 100,
            "bw_encoded": 20,
            "starts_at": "2026-08-01T00:00:00Z",
            "stops_at": "2026-08-02T00:00:00Z",
            "key": "a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1"
        }
    ],
    "delegations": [
        {
            "ia": "1-ff00:0:110",
            "res_id_limit": 100000,
            "expiration": "2027-01-01T00:00:00Z",
            "paid_until": "2027-01-01T00:00:00Z",
            "key": "c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3c3",
            "encodings": [
                10, 20, 30, 40, 50, 60, 70, 80, 90, 100
            ]
        }
    ]
}
"""

DEFAULT_PASSWORD = "1234"
DEFAULT_USERS = ["alice", "bob"]
DEFAULT_USER_BALANCE = 1000000
DEFAULT_AS_BALANCE = 0
DEFAULT_ASSET_BANDWIDTH = 1000000
DEFAULT_ASSET_BANDWIDTH_MIN = 10
DEFAULT_ASSET_BANDWIDTH_MAX = 1000000
DEFAULT_ASSET_PRICE = 1
DEFAULT_ASSET_TIME_GRANULARITY = 10
DEFAULT_ASSET_TIME_MIN_DURATION = 10
DEFAULT_ASSET_DURATION = timedelta(days=100)
DEFAULT_DELEGATION_RES_ID_LIMIT = 1000
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
        data = json.load(file)
    return data

def loadTopology(genDir):
    """Reads the IA, interface IDs and directory of every AS of a topology."""
    topologies = sorted(Path(genDir).glob("AS*/topology.json"))
    if not topologies:
        raise FileNotFoundError(f"no AS*/topology.json found in {genDir}")
    ases = []
    for path in topologies:
        topo = loadJson(path)
        ia = topo.get("isd_as")
        if ia is None:
            raise ValueError(f"{path} has no isd_as")
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

def secretValue(asDir):
    """Derives the Hummingbird secret value of an AS from its master key.

    Handing it to the marketplace is what lets the marketplace redeem the assets
    of that AS, i.e. derive the authenticator of a flyover on its behalf.
    """
    path = asDir / "keys" / "master0.key"
    try:
        master = base64.b64decode(path.read_text(encoding="utf-8").strip(), validate=True)
    except OSError as e:
        raise FileNotFoundError(f"cannot read the master key {path}: {e}")
    except ValueError as e:
        raise ValueError(f"{path} is not a base64 encoded key: {e}")
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

def defaultEntries(genDir, now=None):
    """Builds the default users, ASes and assets for the topology in genDir."""
    if now is None:
        now = datetime.now(timezone.utc)
    startsAt = now.replace(microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ")
    stopsAt = (now + DEFAULT_ASSET_DURATION).replace(microsecond=0).strftime("%Y-%m-%dT%H:%M:%SZ")

    ases = loadTopology(genDir)
    assets = [
        {
            "ia": ia,
            "bandwidth": DEFAULT_ASSET_BANDWIDTH,
            "bandwidth_min": DEFAULT_ASSET_BANDWIDTH_MIN,
            "bandwidth_max": DEFAULT_ASSET_BANDWIDTH_MAX,
            "price": DEFAULT_ASSET_PRICE,
            "time_granularity": DEFAULT_ASSET_TIME_GRANULARITY,
            "time_min_duration": DEFAULT_ASSET_TIME_MIN_DURATION,
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
                "res_id_limit": DEFAULT_DELEGATION_RES_ID_LIMIT,
                "expiration": stopsAt,
                "paid_until": stopsAt,
                "key": secretValue(asDir).hex(),
                "encodings": encodingPoints(),
            }
            for ia, _, asDir in ases
        ],
    }

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
        db.execute(f'DROP TABLE "{table}"')
    return tables

def insertAll(db, data) -> bool:
    if not insertUsers(db, data.get("users")):
        return False
    if not insertAccounts(db, data.get("accounts")):
        return False
    if not insertASes(db, data.get("ases")):
        return False
    if not insertAssets(db, data.get("assets")):
        return False
    if not insertReservations(db, data.get("reservations")):
        return False
    return insertDelegations(db, data.get("delegations"))

def hash_file(filename):
    with open(filename, "r", encoding="utf-8") as f:
        sql = f.read()
    normalized = re.sub(r"\s+", " ", sql).strip()
    return hashlib.sha256(normalized.encode("utf-8")).hexdigest()

def verifyScheme(schemePath, version):
    actualVersion = hash_file(schemePath)
    if version != actualVersion:
        print("scheme version mismatch. Expected: ", actualVersion)
        return False
    return True

def hash_password(password: str) -> str:
    if password is None:
        return None
    hashed = bcrypt.hashpw(
        password.encode("utf-8"),
        bcrypt.gensalt(rounds=12)
    )
    return hashed.decode("utf-8")

def insertAccounts(db, accounts) -> bool:
    if accounts is None:
        return True
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
        [
            (u.get("scope") or "", u.get("balance"), u.get("user"))
            for u in accounts
        ],
    )
    return True

def insertUsers(db,users) -> bool:
    if users is None:
        return True
    for u in users:
        u["pw_hash"] = hash_password(u.get("password"))
    db.executemany(
        """
        INSERT INTO Users (name, pw_hash)
        VALUES (?, ?)
        ON CONFLICT(name) DO UPDATE SET pw_hash = excluded.pw_hash
        """,
        [
            (u.get("name"), u.get("pw_hash"))
            for u in users
        ],
    )
    return True

def parseIA(ia_string):
    parts = ia_string.split("-")
    isd_id = int(parts[0])
    as_string = parts[1]
    as_parts = as_string.split(":")
    if len(as_parts) == 1:
        return isd_id, int(as_string)
    if len(as_parts) != 3:
        print("invalid IA", ia_string)
        return 0,0
    as_id = 0
    for i in range(3):
        as_id <<= 16
        as_id |= int(as_parts[i], 16)
    return isd_id, as_id

def insertAssets(db, assets) -> bool:
    if assets is None:
        return True
    for a in assets:
        isd_id, as_id = parseIA(a.get("ia"))
        if isd_id == 0 and as_id == 0:
            return False
        a["isd_id"] = isd_id
        a["as_id"] = as_id
    db.executemany(
        """
        INSERT INTO Assets (isd_id, as_id, bandwidth, bandwidth_min, bandwidth_max, price, time_granularity, time_min_duration, starts_at, stops_at, ingress, egress, account_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (
            SELECT a.id
            FROM Accounts a
            JOIN Users u ON u.ID = a.user_id
            WHERE u.name = ? AND a.scope = ''
            LIMIT 1
        ))
        """,
        [
            (u.get("isd_id"), u.get("as_id"), u.get("bandwidth"), u.get("bandwidth_min"), u.get("bandwidth_max"), u.get("price"), u.get("time_granularity"), u.get("time_min_duration"), u.get("starts_at"), u.get("stops_at"), u.get("ingress"), u.get("egress"), u.get("owner"),)
            for u in assets
        ],
    )
    return True

def insertASes(db, ases) -> bool:
    if ases is None:
        return True
    for a in ases:
        isd_id, as_id = parseIA(a.get("ia"))
        if isd_id == 0 and as_id == 0:
            return False
        a["isd_id"] = isd_id
        a["as_id"] = as_id
        a["pw_hash"] = hash_password(a.get("password"))
        if a.get("pw_hash") == None:
            a["pw_hash"] = ""
        if a.get("jwt_version") == None:
            a["jwt_version"] = 0
        if a.get("balance") == None:
            a["balance"] = 0
    db.executemany(
        """
        INSERT OR REPLACE INTO Ases (isd_id, as_id, pw_hash, jwt_version, balance)
        VALUES (?, ?, ?, ?, ?)
        """,
        [
            (u.get("isd_id"), u.get("as_id"), u.get("pw_hash"), u.get("jwt_version"), u.get("balance"))
            for u in ases
        ],
    )
    return True

def insertReservations(db, reservations):
    if reservations is None:
        return True
    for a in reservations:
        isd_id, as_id = parseIA(a.get("ia"))
        if isd_id == 0 and as_id == 0:
            return False
        a["isd_id"] = isd_id
        a["as_id"] = as_id
        a["key_bytes"] = bytes.fromhex(a.get("key"))
    for u in reservations:
        db.execute(
            """
        INSERT OR REPLACE INTO Reservations (reservation_id, isd_id, as_id, ingress, egress, bandwidth, bw_encoded, starts_at, stops_at, key, account_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, (
            SELECT id
            FROM Users
            WHERE name = ?
            LIMIT 1
        ))
        """, (u.get("id") , u.get("isd_id"), u.get("as_id"), u.get("ingress"), u.get("egress"), u.get("bandwidth"), u.get("bw_encoded"), u.get("starts_at"), u.get("stops_at"), u.get("key_bytes"),u.get("owner"))
        )
    return True

def insertDelegations(db, delegations):
    if delegations is None:
        return True
    for a in delegations:
        isd_id, as_id = parseIA(a.get("ia"))
        if isd_id == 0 and as_id == 0:
            return False
        a["isd_id"] = isd_id
        a["as_id"] = as_id
        a["key_bytes"] = bytes.fromhex(a.get("key"))
        a["encoding_bytes"] = b"".join(
            i.to_bytes(4, byteorder="little")
            for i in a.get("encodings")
        )
    db.executemany(
        """
        INSERT OR REPLACE INTO Redemption_Delegations (isd_id, as_id, res_id_limit, expiration, paid_until, key, encodings)
        VALUES (?, ?, ?, ?, ?, ?, ?)
        """,
        [
            (u.get("isd_id"), u.get("as_id"), u.get("res_id_limit"), u.get("expiration"), u.get("paid_until"), u.get("key_bytes"),u.get("encoding_bytes"))
            for u in delegations
        ],
    )
    return True

def applyScheme(db, schemePath):
    with open(schemePath, "r", encoding="utf-8") as f:
        db.executescript(f.read())

def main(args):
    data = {}
    if args.file is not None:
        data = loadJson(args.file)
        if not verifyScheme(args.schema, data.get("version")):
            return
    if args.default_entries:
        try:
            data = defaultEntries(args.gen_dir)
        except (OSError, ValueError) as e:
            print("cannot read the topology:", e)
            return
    try:
        conn = sqlite3.connect(args.db)
    except sqlite3.Error as e:
        print("cannot open the database:", e)
        return
    cursor = conn.cursor()
    try:
        dropped = dropTables(cursor)
        if dropped:
            print("dropped", ", ".join(dropped))
        applyScheme(cursor, args.schema)
        if insertAll(cursor, data):
            conn.commit()
            print("stored in database")
            # The database is the one bind mounted into the containers, but a
            # running marketplace keeps its handles on the tables just dropped.
            if (Path(args.gen_dir) / "scion-dc.yml").is_file():
                print("this topology runs on docker: restart the marketplace service "
                      "if it is already running")
        else:
            conn.rollback()
            print("changes rolled back")
    except Exception as e:
        print("error", e)
        conn.rollback()
    finally:
        conn.close()

if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Loads assets, users, reservations and delegations into marketplace db. "
                    "Every table is dropped first, and rebuilt from the given entries."
    )
    parser.add_argument(
        "--db",
        default="gen-cache/marketplace.db",
        help="Path to the SQLite database. Its tables are dropped and recreated on every run."
    )
    parser.add_argument(
        "--schema",
        default="marketplace/db/schema.sql",
        help="Path to the schema.sql file."
    )
    source = parser.add_mutually_exclusive_group(required=True)
    source.add_argument(
        "--file",
        help="Path to the JSON file to import."
    )
    source.add_argument(
        "--default-entries",
        action="store_true",
        help="Fill the database with the default users, ASes, assets and "
             "redemption delegations for the topology in --gen-dir."
    )
    parser.add_argument(
        "--gen-dir",
        default="gen",
        help="Path to the generated topology, read by --default-entries (default: gen)."
    )

    args = parser.parse_args()
    main(args)