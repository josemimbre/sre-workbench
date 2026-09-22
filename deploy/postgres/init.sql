-- The catalogue the shop reads from. Small on purpose: the interesting failure here is
-- never the data, it is what happens when a handful of connections have to serve
-- twenty requests a second.

CREATE TABLE items (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    price_cents INTEGER NOT NULL
);

INSERT INTO items (id, name, price_cents)
SELECT
    'sku-' || n,
    'Item ' || n,
    500 + (n * 37) % 9500
FROM generate_series(0, 49) AS n;
