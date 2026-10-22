CREATE TABLE IF NOT EXISTS Reservations(
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    reservation_id INTEGER NOT NULL,
    ingress INTEGER NOT NULL,
    egress INTEGER NOT NULL,
    starts_at INTEGER NOT NULL,
    stops_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_reservations ON Reservations(stops_at);