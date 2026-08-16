-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- Paid placement and promotions. Neither has a producer in this milestone:
-- these tables are declared now because spec section 5.1 lists them, and
-- because adding tables to a database holding a year of history is cheap
-- while rewriting one is not.
--
-- Where the spec does not say enough to declare a column honestly, the column
-- is either absent or declared in its widest form with the reason stated. No
-- column here was invented to look complete.

-- ad_placements is one paid seat in one search result, for one phrase in one
-- region, at one moment.
--
-- Spec section 4.3 makes this a separate entity rather than a flag on
-- positions: a phrase has many paid seats at once, they move faster than
-- organic ones, and each has its own owner.
--
-- slice_fingerprint is repeated on every row of the same reading. Spec
-- section 5.2 applies the "write only when something changed" rule to an ad
-- slice as a whole -- if the seats and their order are unchanged, no new slice
-- is written -- and this is what lets that comparison be one indexed lookup
-- instead of reassembling and comparing the previous list.
--
-- bid_minor is minor units and nullable, and nullable is the load-bearing
-- part: spec section 4.2 warns that WB may not publish bids at all, and a
-- missing bid stored as zero reads as "this seat was free". The _minor
-- suffix matches rules.threshold_minor below and is what lets
-- TestSchema_MoneyIsMinorUnits (schema_signals_test.go, task 3) find this
-- column by its general "every %_minor column is INTEGER" sweep instead of
-- depending only on this table's own dedicated test to keep watching it.
--
-- placement_type has no CHECK. The vocabulary of placement types is what the
-- risky-source reconnaissance of milestone M1 is meant to discover; freezing
-- a guess here would make the first real value a write failure.
--
-- There is no app_type column: spec section 4.3 keys an ad slice on
-- (query, dest, ts) and nothing in the spec fills a fourth dimension.
--
-- There is no page column either. positions.page (migration 0001) exists
-- because wb.Product.Page is a field the search response actually carries;
-- there is no wb.AdPlacement type at all yet, spec section 4.3's field list
-- for a paid seat is nmID, position, placement type, bid, seller, and
-- nothing here would have a real value to hold before a producer exists.
CREATE TABLE ad_placements (
    id                INTEGER PRIMARY KEY AUTOINCREMENT,
    query             TEXT    NOT NULL,
    dest              TEXT    NOT NULL,
    ts                INTEGER NOT NULL,
    position          INTEGER NOT NULL,
    nm_id             INTEGER NOT NULL,
    supplier_id       INTEGER,
    placement_type    TEXT    NOT NULL DEFAULT '',
    bid_minor         INTEGER,
    bid_currency      TEXT    NOT NULL DEFAULT '',
    slice_fingerprint TEXT    NOT NULL
) STRICT;

-- The third index spec section 5.2 names, for the table it names as the
-- fastest-growing one in the schema.
CREATE INDEX idx_ad_placements_query_dest_ts ON ad_placements(query, dest, ts);
CREATE INDEX idx_ad_placements_nm_ts ON ad_placements(nm_id, ts);

-- promos is one promotion: what it is called, when it runs, on what terms.
--
-- id is TEXT because spec section 4.3 says "identifier" without saying whether
-- the public payload spells it as a number or a string. TEXT holds either
-- without loss; INTEGER would reject one of the two outright. A later
-- migration can narrow it once a producer has seen the real thing.
--
-- conditions is the site's own wording, stored verbatim. The spec names
-- "terms" as part of a promotion and gives them no structure, and a parsed
-- shape invented here would be a shape nobody parses into.
CREATE TABLE promos (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL DEFAULT '',
    starts_at     INTEGER,
    ends_at       INTEGER,
    conditions    TEXT NOT NULL DEFAULT '',
    first_seen_at INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL
) STRICT;

-- promo_items is who was in a promotion and at what price, as of one reading.
--
-- ts is part of the key because membership is exactly the thing being
-- watched: spec section 6.1 has PromoJoined, PromoLeft and PromoPriceChanged,
-- and none of the three is answerable from a table that only knows the
-- current state.
CREATE TABLE promo_items (
    promo_id TEXT    NOT NULL REFERENCES promos(id) ON DELETE CASCADE,
    nm_id    INTEGER NOT NULL,
    ts       INTEGER NOT NULL,
    price    INTEGER,
    currency TEXT    NOT NULL DEFAULT '',
    PRIMARY KEY (promo_id, nm_id, ts)
) STRICT;

CREATE INDEX idx_promo_items_nm_ts ON promo_items(nm_id, ts);
