#!/usr/bin/env python3

"""Rebuilds the database of a marketplace.

The topology generator already prepopulates the database of the marketplace it
adds with -m/--marketplace, so this script is only needed to load a different set
of entries, or to reset the database of a running topology.

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

import argparse
import sys
from pathlib import Path

# We need to import from the topology generator, which is not in sys.path.
# This is unavoidable unless we move either the topology generation,
# or the marketplace tools.
sys.path.insert(0, str(Path(__file__).resolve().parents[2] / "tools"))

# Linters like flake8 would complain about the import not being top-level. Avoid with noqa: e402
from topology.marketplace import (  # noqa: E402
    MARKETPLACE_DB_NAME,
    MARKETPLACE_SCHEMA,
    LOCAL_CACHE_DIR,
    MarketplaceError,
    defaultEntries,
    loadJson,
    populateDB,
    verifySchema,
)

DEFAULT_DB = str(Path(LOCAL_CACHE_DIR) / MARKETPLACE_DB_NAME)


def main(args) -> None:
    if args.default_entries:
        data = defaultEntries(args.gen_dir)
    else:
        try:
            data = loadJson(args.file)
        except (OSError, ValueError) as e:
            raise MarketplaceError("cannot read %s: %s" % (args.file, e))
        verifySchema(args.schema, data.get("version"))

    dropped = populateDB(args.db, args.schema, data)
    if dropped:
        print("dropped", ", ".join(dropped))
    print("stored in database")
    # The database is the one bind-mounted into the containers,
    # but a running marketplace keeps its handles on the tables just dropped.
    if (Path(args.gen_dir) / "scion-dc.yml").is_file():
        print("this topology runs on docker: restart the marketplace service if it is running")


if __name__ == "__main__":
    parser = argparse.ArgumentParser(
        description="Loads assets, users, reservations and delegations into marketplace db. "
                    "Every table is dropped first, and rebuilt from the given entries."
    )
    parser.add_argument(
        "--db",
        default=DEFAULT_DB,
        help="Path to the SQLite database. Its tables are dropped and recreated on every run. "
             "(default: %s)" % DEFAULT_DB
    )
    parser.add_argument(
        "--schema",
        default=MARKETPLACE_SCHEMA,
        help="Path to the schema.sql file (default: %s)." % MARKETPLACE_SCHEMA
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
             "redemption delegations for the topology in --gen-dir. This is what "
             "topogen -m does."
    )
    parser.add_argument(
        "--gen-dir",
        default="gen",
        help="Path to the generated topology, read by --default-entries (default: gen)."
    )

    try:
        main(parser.parse_args())
    except MarketplaceError as e:
        print("error: %s" % e, file=sys.stderr)
        sys.exit(1)
    except Exception as e:
        # Most likely a malformed entry in --file. Don't traceback.
        print("error: %s: %s" % (type(e).__name__, e), file=sys.stderr)
        sys.exit(1)
