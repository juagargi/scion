CREATE TABLE IF NOT EXISTS Assets(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    account_id INTEGER,
    isd_id INTEGER NOT NULL,
    as_id INTEGER NOT NULL,
    bandwidth INTEGER NOT NULL,
    bandwidth_min INTEGER NOT NULL,
    bandwidth_max INTEGER NOT NULL CHECK (bandwidth_max >= bandwidth_min),
    price INTEGER NOT NULL,
    time_granularity INTEGER NOT NULL,
    time_min_duration INTEGER NOT NULL,
    starts_at TEXT NOT NULL,
    stops_at TEXT NOT NULL,
    ingress INTEGER,
    egress INTEGER,
    state INTEGER NOT NULL DEFAULT 0,
    FOREIGN KEY (account_id) REFERENCES Accounts(id)
);
CREATE INDEX IF NOT EXISTS idx_asset_owner ON Assets(account_id);
CREATE INDEX IF NOT EXISTS idx_asset_validity ON Assets(starts_at, stops_at);
CREATE INDEX IF NOT EXISTS idx_assets_ia_validity_start ON Assets(isd_id, as_id, starts_at, stops_at);
CREATE INDEX IF NOT EXISTS idx_assets_ia_validity_end ON Assets(isd_id, as_id, stops_at, starts_at);
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
    starts_at TEXT NOT NULL,
    stops_at TEXT NOT NULL,
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
    expiration TEXT NOT NULL CHECK (expiration <= paid_until),
    paid_until TEXT NOT NULL,
    key BLOB NOT NULL,
    encodings BLOB NOT NULL,
    PRIMARY KEY(isd_id, as_id)
);
CREATE INDEX IF NOT EXISTS idx_redemption_expiration ON Redemption_Delegations(expiration);