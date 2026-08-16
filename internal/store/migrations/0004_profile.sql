-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The seller's own profile and its competitive environment, spec section 4.7.
-- No producer in this milestone; the shape follows that section and nothing
-- else.

-- profiles is the "mine" contour. Spec section 4.7 is explicit that the flag
-- lives here and not on the product: the same listing is mine in one profile
-- and a competitor's in another, so ownership is a membership, not an
-- attribute of the thing owned.
--
-- source_input is the one link or bare article the user pasted. It is kept
-- because the resolver's chain -- link, nmID, card, supplierID, storefront --
-- is worth being able to re-run when the user asks "why did it decide this
-- shop is mine".
CREATE TABLE profiles (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    name         TEXT    NOT NULL DEFAULT '',
    source_input TEXT    NOT NULL DEFAULT '',
    seller_id    INTEGER,
    created_at   INTEGER NOT NULL,
    updated_at   INTEGER NOT NULL
) STRICT;

-- profile_items is what belongs to a profile: the seller, its brands, its
-- products. One table with a checked kind rather than three, because every
-- question asked of it is "is this mine", which reads all three together and
-- never one alone.
CREATE TABLE profile_items (
    profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL CHECK (kind IN ('seller', 'brand', 'product')),
    entity_id  INTEGER NOT NULL,
    added_at   INTEGER NOT NULL,
    PRIMARY KEY (profile_id, kind, entity_id)
) STRICT;

-- phrases is a search phrase and what checking it produced.
--
-- WB publishes no list of the phrases a seller ranks for, so these are
-- derived: generated from the card, expanded through the site's own
-- suggestions, or uploaded by the user. The state machine is what keeps the
-- most expensive part of onboarding from being paid for twice -- a phrase
-- already checked and found irrelevant is never queued again.
--
-- The key is (profile, text, product, region), which is narrower than the
-- "Phrase" entity spec section 4.7 describes. It has to be: the rule that
-- decides the state -- "the product entered the top N for it" -- is stated per
-- product and per region, and one phrase can be working for one listing and
-- irrelevant for its neighbour. The per-phrase view is a grouping over this
-- one; the reverse cannot be recovered. nm_id 0 and an empty dest mean a
-- candidate not yet tied to a listing, which is what an uploaded phrase is
-- before the first check.
CREATE TABLE phrases (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    profile_id INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    text       TEXT    NOT NULL,
    state      TEXT    NOT NULL CHECK (state IN ('candidate', 'working', 'checked-irrelevant')),
    origin     TEXT    NOT NULL CHECK (origin IN ('generated', 'suggested', 'uploaded')),
    nm_id      INTEGER NOT NULL DEFAULT 0,
    dest       TEXT    NOT NULL DEFAULT '',
    -- best_rank is the position that decided the state, kept so the top-N
    -- threshold can be changed later without re-checking everything.
    best_rank  INTEGER,
    checked_at INTEGER,
    created_at INTEGER NOT NULL,
    UNIQUE (profile_id, text, nm_id, dest)
) STRICT;

CREATE INDEX idx_phrases_profile_state ON phrases(profile_id, state);

-- competitors is a seller or a listing in the competitive environment, with
-- the two numbers spec section 4.7 ranks them by and the hand edits that
-- override the ranking.
--
-- pinned is never overwritten by the recompute: the section says a pinned
-- competitor is never displaced automatically, and a set the user cannot hold
-- still is a set they stop trusting. excluded is its mirror.
--
-- position_delta is an average of ranks, not money, which is why it is REAL.
CREATE TABLE competitors (
    profile_id     INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    kind           TEXT    NOT NULL CHECK (kind IN ('seller', 'product')),
    entity_id      INTEGER NOT NULL,
    adjacency      INTEGER NOT NULL DEFAULT 0,
    position_delta REAL,
    pinned         INTEGER NOT NULL DEFAULT 0,
    excluded       INTEGER NOT NULL DEFAULT 0,
    computed_at    INTEGER NOT NULL,
    PRIMARY KEY (profile_id, kind, entity_id)
) STRICT;

-- benchmarks is one comparison of one listing against one baseline, keyed as
-- spec section 4.7 requires plus the baseline it was measured against.
--
-- baseline is either the median of the top K or one pinned competitor, and
-- baseline_id names the second one; zero for the median, which has no id.
--
-- The columns come in pairs -- mine, then theirs -- and every pair is one row of
-- the comparison table in spec section 4.7 and nothing else. Deltas are not
-- stored: they are a subtraction, and a stored subtraction is a second copy
-- that can disagree with its operands.
--
-- Two units are the spec's own and are not reinterpreted here:
-- feedbacks_per_day has no averaging window stated in section 4.7, and
-- delivery_time2 is the site's raw figure, the same one snapshots keep,
-- because WB documents no unit for it.
CREATE TABLE benchmarks (
    profile_id                INTEGER NOT NULL REFERENCES profiles(id) ON DELETE CASCADE,
    nm_id                     INTEGER NOT NULL,
    query                     TEXT    NOT NULL,
    dest                      TEXT    NOT NULL,
    ts                        INTEGER NOT NULL,
    baseline                  TEXT    NOT NULL,
    baseline_id               INTEGER NOT NULL DEFAULT 0,

    -- Organic placement beside placement as a shopper sees it. Section 4.7
    -- compares both, because that is what says whether the seat was lost to
    -- the ranking or bought by somebody's bid.
    position_organic          INTEGER,
    position_with_ads         INTEGER,
    rival_position_organic    INTEGER,
    rival_position_with_ads   INTEGER,

    price                     INTEGER,
    rival_price               INTEGER,
    currency                  TEXT    NOT NULL DEFAULT '',
    discount_pct              INTEGER,
    rival_discount_pct        INTEGER,

    rating                    REAL,
    rival_rating              REAL,
    feedbacks                 INTEGER,
    rival_feedbacks           INTEGER,
    feedbacks_per_day         REAL,
    rival_feedbacks_per_day   REAL,

    total_quantity            INTEGER,
    rival_total_quantity      INTEGER,
    available                 INTEGER,
    rival_available           INTEGER,
    delivery_time2            INTEGER,
    rival_delivery_time2      INTEGER,

    -- Card completeness: the only gap in this table a seller can close today,
    -- without money and without waiting, which is why section 4.7 breaks it
    -- out instead of folding it into one score.
    photo_count               INTEGER,
    rival_photo_count         INTEGER,
    has_video                 INTEGER,
    rival_has_video           INTEGER,
    description_len           INTEGER,
    rival_description_len     INTEGER,
    options_filled_pct        INTEGER,
    rival_options_filled_pct  INTEGER,

    in_promo                  INTEGER,
    rival_in_promo            INTEGER,
    has_ad                    INTEGER,
    rival_has_ad              INTEGER,

    PRIMARY KEY (profile_id, nm_id, query, dest, ts, baseline, baseline_id)
) STRICT;

CREATE INDEX idx_benchmarks_nm_query_dest_ts ON benchmarks(nm_id, query, dest, ts);
