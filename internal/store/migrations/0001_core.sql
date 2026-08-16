-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The stable/volatile split. wb.Product is one row of one response and fuses
-- both halves; keeping that shape would mean rewriting a product's name and
-- brand on every pass and losing the ability to ask what changed. See spec
-- section 4.3, which calls this split load-bearing.

CREATE TABLE products (
    nm_id             INTEGER PRIMARY KEY,
    -- imt_id groups a product's variants and is what reviews are keyed on.
    -- Null until a card has been fetched: a search result never carries it.
    imt_id            INTEGER,
    -- match_id groups every seller's listing of the same physical product.
    -- Zero is the site's own sentinel for "belongs to no group", not an
    -- absence, which is why it is NOT NULL with a zero default.
    match_id          INTEGER NOT NULL DEFAULT 0,
    root_id           INTEGER,
    name              TEXT    NOT NULL DEFAULT '',
    brand             TEXT    NOT NULL DEFAULT '',
    brand_id          INTEGER,
    supplier_id       INTEGER,
    supplier_name     TEXT    NOT NULL DEFAULT '',
    subject_id        INTEGER,
    subject_parent_id INTEGER,

    -- The card's static half. Null until Client.Card has run for this
    -- product; a search result carries none of it.
    slug              TEXT,
    subject_name      TEXT,
    subject_root_name TEXT,
    vendor_code       TEXT,
    description       TEXT,
    contents          TEXT,
    season            TEXT,
    colour_names      TEXT,
    card_created      TEXT,
    card_updated      TEXT,

    first_seen_at     INTEGER NOT NULL,
    last_seen_at      INTEGER NOT NULL
) STRICT;

-- product_options is the card's characteristics, in the site's own order.
-- position is stored rather than inferred: the card shows them in the order
-- they arrive, and reordering them would make two scrapes differ where the
-- data did not.
CREATE TABLE product_options (
    nm_id    INTEGER NOT NULL REFERENCES products(nm_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name     TEXT    NOT NULL,
    value    TEXT    NOT NULL,
    PRIMARY KEY (nm_id, position)
) STRICT;

-- product_compositions is the card's declared material composition, in the
-- site's own order.
--
-- It is its own table rather than a reserved name inside product_options,
-- because a composition is a different fact from a characteristic. Filtering
-- characteristics by a magic key to find the composition works until the day
-- a seller names a characteristic that key, and then it returns a material
-- list that is not one. products.contents is a third fact again -- what is in
-- the box -- and is not this either.
--
-- position is stored for the same reason product_options stores it: the card
-- shows the parts in the order they arrive, and reordering them would make
-- two scrapes differ where the data did not.
CREATE TABLE product_compositions (
    nm_id    INTEGER NOT NULL REFERENCES products(nm_id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    name     TEXT    NOT NULL,
    PRIMARY KEY (nm_id, position)
) STRICT;

-- snapshots is everything that moves, keyed as spec section 4.3 requires.
--
-- anchor marks a row written because a day passed with no change, not
-- because something moved. Retention and any "what changed" query must be
-- able to tell the two apart: an anchor is evidence the product was still
-- there, not evidence that it changed.
--
-- fingerprint is the digest of the volatile half this row carries. The next
-- pass compares against it instead of against every column, which is what
-- makes "write only when something changed" one cheap comparison rather
-- than twenty.
CREATE TABLE snapshots (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    nm_id          INTEGER NOT NULL REFERENCES products(nm_id) ON DELETE CASCADE,
    dest           TEXT    NOT NULL,
    app_type       INTEGER NOT NULL,
    ts             INTEGER NOT NULL,
    anchor         INTEGER NOT NULL DEFAULT 0,
    fingerprint    TEXT    NOT NULL,

    rating         REAL,
    rating_key     TEXT    NOT NULL DEFAULT '',
    feedbacks      INTEGER,
    feedback_key   TEXT    NOT NULL DEFAULT '',
    total_quantity INTEGER,

    -- Prices in minor units, taken from the cheapest size so the sale and
    -- base price always describe the same variant. Currency travels with
    -- them because a price without one is a number, not an amount.
    price_base     INTEGER,
    price_sale     INTEGER,
    discount_pct   INTEGER,
    currency       TEXT    NOT NULL DEFAULT '',

    -- The product-level delivery figures the site repeats outside the size
    -- objects. They move with dest, which is why dest is part of the key.
    time1          INTEGER,
    time2          INTEGER,
    dist           INTEGER,
    warehouse_id   INTEGER
) STRICT;

-- The index spec section 5.2 names first. Every history question is "this
-- product, this region, over time".
CREATE INDEX idx_snapshots_nm_dest_ts ON snapshots(nm_id, dest, ts);

CREATE TABLE snapshot_sizes (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    snapshot_id   INTEGER NOT NULL REFERENCES snapshots(id) ON DELETE CASCADE,
    name          TEXT    NOT NULL DEFAULT '',
    orig_name     TEXT    NOT NULL DEFAULT '',
    price_basic   INTEGER,
    price_product INTEGER,
    price_total   INTEGER
) STRICT;

CREATE INDEX idx_snapshot_sizes_snapshot ON snapshot_sizes(snapshot_id);

CREATE TABLE snapshot_stocks (
    snapshot_size_id INTEGER NOT NULL REFERENCES snapshot_sizes(id) ON DELETE CASCADE,
    warehouse_id     INTEGER NOT NULL,
    qty              INTEGER NOT NULL,
    priority         INTEGER NOT NULL DEFAULT 0,
    delivery_type    INTEGER NOT NULL DEFAULT 0,
    time1            INTEGER,
    time2            INTEGER,
    dist             INTEGER,
    PRIMARY KEY (snapshot_size_id, warehouse_id)
) STRICT;

-- positions is organic placement only. Paid placement is ad_placements, a
-- separate table with its own owner per row; see spec section 4.3.
CREATE TABLE positions (
    nm_id INTEGER NOT NULL REFERENCES products(nm_id) ON DELETE CASCADE,
    query TEXT    NOT NULL,
    dest  TEXT    NOT NULL,
    ts    INTEGER NOT NULL,
    rank  INTEGER NOT NULL,
    page  INTEGER NOT NULL,
    PRIMARY KEY (nm_id, query, dest, ts)
) STRICT;

-- The second index spec section 5.2 names.
CREATE INDEX idx_positions_query_dest_ts ON positions(query, dest, ts);
