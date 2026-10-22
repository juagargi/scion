CREATE TABLE IF NOT EXISTS Assets(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER,
    isd_id INTEGER NOT NULL,
    as_id INTEGER NOT NULL,
    bandwidth INTEGER NOT NULL CHECK (bandwidth > 0),
    bandwidth_min INTEGER NOT NULL,
    bandwidth_max INTEGER NOT NULL CHECK (bandwidth_max >= bandwidth_min),
    price INTEGER NOT NULL,
    time_granularity INTEGER NOT NULL,
    time_min_duration INTEGER NOT NULL,
    time_max_duration INTEGER NOT NULL,
    starts_at INTEGER NOT NULL,
    stops_at INTEGER NOT NULL,
    ingress INTEGER,
    egress INTEGER,
    state INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (account_id) REFERENCES Accounts(id)
);
CREATE INDEX IF NOT EXISTS idx_asset_owner ON Assets(account_id, isd_id, as_id);
CREATE INDEX IF NOT EXISTS idx_assets_validity ON Assets(isd_id, as_id, stops_at);

CREATE TABLE IF NOT EXISTS Asset_Segment(
    asset_id INTEGER NOT NULL,
    starts_at INTEGER NOT NULL,
    stops_at INTEGER NOT NULL,
    available INTEGER NOT NULL CHECK (available >= 0),
    PRIMARY KEY (asset_id, starts_at),
    CHECK (starts_at < stops_at),
    FOREIGN KEY(asset_id) REFERENCES Assets(id) ON DELETE CASCADE
) WITHOUT ROWID;

CREATE TABLE IF NOT EXISTS Reservations(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    reservation_id INTEGER NOT NULL,
    account_id INTEGER NOT NULL,
    isd_id INTEGER NOT NULL,
    as_id INTEGER NOT NULL,
    ingress INTEGER NOT NULL,
    egress INTEGER NOT NULL,
    bandwidth INTEGER NOT NULL,
    bw_encoded INTEGER NOT NULL,
    starts_at INTEGER NOT NULL,
    stops_at INTEGER NOT NULL,
    key BLOB NOT NULL,
    FOREIGN KEY (account_id) REFERENCES Accounts(id)
);
CREATE INDEX IF NOT EXISTS idx_reservations_owner ON Reservations(account_id);
CREATE INDEX IF NOT EXISTS idx_reservations_used ON Reservations(isd_id, as_id, starts_at, stops_at);
CREATE TABLE IF NOT EXISTS Users(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT NOT NULL UNIQUE,
    pw_hash TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_users_name ON Users(name);
CREATE TABLE IF NOT EXISTS Accounts(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id INTEGER NOT NULL,
    -- The main account of a user has an empty scope. It must not be NULL: SQLite
    -- considers every NULL distinct, so UNIQUE(user_id, scope) would not keep a
    -- user from having several main accounts.
    scope TEXT NOT NULL DEFAULT '',
    balance INTEGER NOT NULL DEFAULT 0 CHECK (balance >= 0),
    jwt_version INTEGER NOT NULL DEFAULT 0,
    UNIQUE(user_id, scope),
    FOREIGN KEY(user_id) REFERENCES Users(id)
);
CREATE INDEX IF NOT EXISTS idx_accounts ON Accounts(user_id);
CREATE TABLE IF NOT EXISTS Ases(
    isd_id INTEGER NOT NULL,
    as_id INTEGER NOT NULL,
    pw_hash TEXT NOT NULL DEFAULT '',
    jwt_version INTEGER NOT NULL DEFAULT 0,
    balance INTEGER NOT NULL DEFAULT 0 CHECK (balance >= 0),
    PRIMARY KEY(isd_id, as_id)
);
CREATE TABLE IF NOT EXISTS Redemption_Delegations(
    isd_id INTEGER NOT NULL,
    as_id INTEGER NOT NULL,
    res_id_limit_low INTEGER NOT NULL DEFAULT 0,
    res_id_limit_high INTEGER NOT NULL,
    expiration INTEGER NOT NULL CHECK (expiration <= paid_until),
    paid_until INTEGER NOT NULL,
    key BLOB NOT NULL,
    encodings BLOB NOT NULL,
    PRIMARY KEY(isd_id, as_id)
);
CREATE INDEX IF NOT EXISTS idx_redemption_expiration ON Redemption_Delegations(expiration);
CREATE TABLE IF NOT EXISTS Asset_Events(
    isd_id INTEGER NOT NULL,
    as_id INTEGER NOT NULL,
    ingress INTEGER,
    egress INTEGER,
    bandwidth INTEGER NOT NULL,
    starts_at INTEGER NOT NULL,
    stops_at INTEGER NOT NULL,
    price INTEGER NOT NULL,
    event_type INTEGER NOT NULL,
    event_at INTEGER NOT NULL DEFAULT (unixepoch())
);
CREATE INDEX IF NOT EXISTS idx_asset_events ON Asset_Events(event_type, isd_id, as_id, stops_at, starts_at);