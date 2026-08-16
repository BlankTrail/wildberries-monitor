-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The signal half of spec section 5.1: what buyers, sellers and the site say
-- about a product, plus the two tables the tracker writes.
--
-- Foreign keys here point at rows this schema owns and nothing else. In
-- particular nm_id is never a foreign key to products: reviews are fetched by
-- imtId, a shelf holds listings nobody has scraped on their own, and an event
-- can name a product this database has never seen a search row for. A
-- foreign key would turn each of those into a failed insert, which is the
-- monitor losing a reading it already paid a request for.

-- reviews is one buyer's review, keyed on the site's own review id, which is
-- a string and not a number.
--
-- nm_id and imt_id are both stored: the endpoint is keyed on imtId, the review
-- itself names the variant that was bought, and a rating problem that is
-- really "the M runs small" is only findable through the second.
CREATE TABLE reviews (
    id                   TEXT    PRIMARY KEY,
    nm_id                INTEGER NOT NULL,
    imt_id               INTEGER NOT NULL,
    text                 TEXT    NOT NULL DEFAULT '',
    pros                 TEXT    NOT NULL DEFAULT '',
    cons                 TEXT    NOT NULL DEFAULT '',
    -- valuation is this one review's stars, 1 through 5, never the card's
    -- aggregate.
    valuation            INTEGER NOT NULL,
    size                 TEXT    NOT NULL DEFAULT '',
    color                TEXT    NOT NULL DEFAULT '',
    created_at           INTEGER NOT NULL,
    -- updated_at is null, not zero, when the payload sent no edit date: zero
    -- would read as 1970 and sort before every real review.
    updated_at           INTEGER,
    -- excluded_from_rating is the flag; the reasons behind it are rows in
    -- review_exclusion_reasons below.
    excluded_from_rating INTEGER NOT NULL DEFAULT 0,
    photo_count          INTEGER NOT NULL DEFAULT 0,
    first_seen_at        INTEGER NOT NULL,
    last_seen_at         INTEGER NOT NULL
) STRICT;

-- The window a card's reviews are read in is "this card, newest first".
CREATE INDEX idx_reviews_imt_created ON reviews(imt_id, created_at);
CREATE INDEX idx_reviews_nm_created ON reviews(nm_id, created_at);

-- review_answers is the seller's reply, at most one per review.
--
-- It is a table and not two columns on reviews because wb.Review.Answer is a
-- pointer: a review with no reply and a reply whose text is empty are
-- different facts, and the absence of a row says the first without needing a
-- null-versus-empty convention on a text column.
CREATE TABLE review_answers (
    review_id  TEXT PRIMARY KEY REFERENCES reviews(id) ON DELETE CASCADE,
    text       TEXT NOT NULL DEFAULT '',
    -- Null when the answer object carried no createDate. The payload spells
    -- that key differently from the review's own createdDate, so an answer
    -- with no date is a shape that really occurs.
    created_at INTEGER
) STRICT;

-- review_tags holds three catalogues of small integer ids under one roof:
-- Review.Tags, Reasons.Good and Reasons.Bad. They are the same kind of thing
-- -- an id into a WB lookup this product does not decode -- and they are
-- always read together with their review, so three tables would be three
-- joins for no distinction anyone acts on. kind is what tells them apart, and
-- the CHECK is what keeps a fourth spelling from inventing a fourth
-- catalogue.
--
-- catalog_id is INTEGER, and question_tags.tag below is TEXT. That is not an
-- inconsistency to be tidied away: a review's tags are ids into a lookup
-- keyed by product category, while a question's are the site's own symbolic
-- strings. The two fields share a name in the payload and in wb, and nothing
-- else. Aligning the column types would make one of them unstorable and the
-- other meaningless.
CREATE TABLE review_tags (
    review_id  TEXT    NOT NULL REFERENCES reviews(id) ON DELETE CASCADE,
    kind       TEXT    NOT NULL CHECK (kind IN ('tag', 'reason-good', 'reason-bad')),
    catalog_id INTEGER NOT NULL,
    PRIMARY KEY (review_id, kind, catalog_id)
) STRICT;

-- review_exclusion_reasons is why a review was left out of the card's rating.
--
-- These are the site's own display strings, not catalogue ids, so they cannot
-- share review_tags. position keeps the order the site listed them in, for
-- the reason product_options keeps its own: the order is the site's, and
-- reordering it would make two scrapes of an unchanged review differ where
-- nothing changed.
CREATE TABLE review_exclusion_reasons (
    review_id TEXT    NOT NULL REFERENCES reviews(id) ON DELETE CASCADE,
    position  INTEGER NOT NULL,
    reason    TEXT    NOT NULL,
    PRIMARY KEY (review_id, position)
) STRICT;

-- review_summaries is the card's own aggregate as it stood at one reading:
-- the rating every shopper sees, the counts behind it, and the size-accuracy
-- figure.
--
-- It is a table of its own and not columns on snapshots. Folding it back in
-- looks like a simplification, so the three reasons it is not one are written
-- here rather than left to be rediscovered:
--
--   1. This is keyed on imt_id; a snapshot is keyed on (nm_id, dest). Those
--      are two of the site's numberings at two different grains, and putting
--      one into the other's column is exactly the substitution the wb package
--      refuses to make -- see wb.Event, which carries ImtID beside NmID
--      rather than letting either stand in for the other.
--   2. This is not regional. A snapshot is regional by construction: price,
--      stock and the delivery window all move with dest. A rating does not.
--   3. with_photo, with_text, with_video, size_matching and the star
--      histogram have no columns on a snapshot and must not gain any. A
--      snapshot describes an offer; this describes a reputation.
--
-- ts is part of the key because the only question ever asked of this table is
-- how a card's rating moved over time.
CREATE TABLE review_summaries (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    imt_id        INTEGER NOT NULL,
    ts            INTEGER NOT NULL,
    valuation     REAL    NOT NULL DEFAULT 0,
    count         INTEGER NOT NULL DEFAULT 0,
    with_photo    INTEGER NOT NULL DEFAULT 0,
    with_text     INTEGER NOT NULL DEFAULT 0,
    with_video    INTEGER NOT NULL DEFAULT 0,
    -- Null, never zero, when the payload sent no size-accuracy figure: zero
    -- is a measurement, and the worst one available. wb keeps this a pointer
    -- for the same reason.
    size_matching REAL
) STRICT;

-- The key and the query are the same pair, so one unique index serves both:
-- it refuses a second copy of one reading and answers "how did this card's
-- rating move" without a scan.
CREATE UNIQUE INDEX idx_review_summaries_imt_ts ON review_summaries(imt_id, ts);

-- review_distribution is the star histogram of one summary, one row per star
-- rating.
--
-- wb.ReviewSummary.Distribution is a Go map, and a map has no order. Written
-- in whatever order the writer happened to walk it, the histogram would come
-- back as rows in no particular order, with nothing stopping one star rating
-- from appearing twice and being counted twice. stars is part of the key, so
-- the histogram is stored as a histogram and not as a list of pairs.
CREATE TABLE review_distribution (
    summary_id INTEGER NOT NULL REFERENCES review_summaries(id) ON DELETE CASCADE,
    stars      INTEGER NOT NULL,
    count      INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (summary_id, stars)
) STRICT;

-- questions is one buyer's question and, where there is one, the reply.
--
-- The reply lives in columns here rather than in a child table, which is the
-- opposite of what reviews do above, because spec section 5.1 gives reviews an
-- answers table and questions none. The pointer distinction survives anyway:
-- answer_text NULL is "no reply at all", the empty string is "a reply with
-- empty text", and QuestionUnanswered fires only on the first.
CREATE TABLE questions (
    id                 TEXT    PRIMARY KEY,
    nm_id              INTEGER NOT NULL,
    imt_id             INTEGER NOT NULL,
    text               TEXT    NOT NULL DEFAULT '',
    created_at         INTEGER NOT NULL,
    supplier_article   TEXT    NOT NULL DEFAULT '',
    answer_text        TEXT,
    answer_created_at  INTEGER,
    answer_supplier_id INTEGER,
    first_seen_at      INTEGER NOT NULL,
    last_seen_at       INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_questions_imt_created ON questions(imt_id, created_at);

-- question_tags is a question's tags, which are the site's own symbolic
-- strings -- "PLATFORM_QUERY" and its kind -- and not the catalogue ids
-- review_tags holds. See the comment there for why the two column types
-- differ on purpose; the short version is that they are different data
-- wearing the same field name.
--
-- position keeps the site's order, and the key is (question_id, position)
-- rather than (question_id, tag): position is what a repeated tag and an
-- order-only change both need, and a key on the string would silently drop
-- the first and hide the second.
CREATE TABLE question_tags (
    question_id TEXT    NOT NULL REFERENCES questions(id) ON DELETE CASCADE,
    position    INTEGER NOT NULL,
    tag         TEXT    NOT NULL,
    PRIMARY KEY (question_id, position)
) STRICT;

-- sellers is the public seller record, keyed on the site's supplier id.
--
-- Every nullable column is nullable because wb.Seller holds a pointer there:
-- a rating the site did not send and a rating of zero are different facts, and
-- a seller with no rating yet must not sort as the worst seller on the site.
--
-- Spec section 4.7 also lists INN, OGRN and the legal address as public seller
-- data. No column is declared for them: wb.Seller carries none, so nothing in
-- this milestone could fill them, and a column nobody writes reads as "this
-- seller has no INN" rather than "nobody looked". The producer that gains them
-- brings its own migration.
CREATE TABLE sellers (
    id                INTEGER PRIMARY KEY,
    name              TEXT    NOT NULL DEFAULT '',
    full_name         TEXT    NOT NULL DEFAULT '',
    type              TEXT    NOT NULL DEFAULT '',
    valuation         REAL,
    feedback_count    INTEGER,
    registered_at     INTEGER,
    item_count        INTEGER,
    delivery_duration INTEGER,
    is_premium        INTEGER NOT NULL DEFAULT 0,
    loyalty_level     INTEGER NOT NULL DEFAULT 0,
    first_seen_at     INTEGER NOT NULL,
    last_seen_at      INTEGER NOT NULL
) STRICT;

-- brands is the brand as an object, which spec section 4.3 asks for so that
-- products.brand stops being a bare string.
--
-- site_id is WB's second numbering of the same brand and travels beside id
-- because wb.Brand carries both. The item count, rating and seller count spec
-- section 4.3 also names are not declared: wb.Brand has no such fields, and
-- the brand storefront that would produce them arrives in a later milestone
-- with its own migration.
CREATE TABLE brands (
    id            INTEGER PRIMARY KEY,
    site_id       INTEGER NOT NULL DEFAULT 0,
    name          TEXT    NOT NULL DEFAULT '',
    url           TEXT    NOT NULL DEFAULT '',
    first_seen_at INTEGER NOT NULL,
    last_seen_at  INTEGER NOT NULL
) STRICT;

-- shelves is one recommendation or storefront shelf as it stood at one moment.
--
-- Spec section 4.3 keys a shelf on (source, source_key, kind, ts). That is not
-- enough to identify a row on its own: one fetch returns a whole set of
-- shelves, so position carries the set's own order and the surrogate id is
-- what shelf_items hang off.
--
-- source names what the shelf was asked for -- a query, a product, the home
-- page -- and source_key is that thing rendered as text, since a phrase and an
-- nmID cannot share a column type. No CHECK on source: the set of shelf
-- sources grows with each producer, and a released database that refuses a
-- new one would fail the write rather than the build.
--
-- kind separates the payload's two arrays, banners and shelfs. Nothing
-- observed so far explains how they differ, and merging them would assume
-- they are the same thing.
--
-- dest is the region the shelf was read for. wb.Shelves carries no region of
-- its own, so the writer takes it from the products the shelf holds -- they all
-- carry one. Spec section 4.2 requires shelf snapshots to be pinned to a fixed
-- region precisely because their composition moves with it, so a shelf without
-- a region is not comparable with anything.
CREATE TABLE shelves (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    source     TEXT    NOT NULL,
    source_key TEXT    NOT NULL,
    kind       TEXT    NOT NULL CHECK (kind IN ('banner', 'shelf')),
    title      TEXT    NOT NULL DEFAULT '',
    position   INTEGER NOT NULL DEFAULT 0,
    preset_id  INTEGER NOT NULL DEFAULT 0,
    dest       TEXT    NOT NULL DEFAULT '',
    ts         INTEGER NOT NULL
) STRICT;

CREATE INDEX idx_shelves_source_ts ON shelves(source, source_key, ts);

-- shelf_items is the shelf's contents, in the site's order. "Third in 'people
-- also buy'" is the fact worth storing; a set of products without their places
-- answers none of the questions section 6.1 asks about shelves.
CREATE TABLE shelf_items (
    shelf_id INTEGER NOT NULL REFERENCES shelves(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    nm_id    INTEGER NOT NULL,
    PRIMARY KEY (shelf_id, position)
) STRICT;

-- duplicates is one reading of every seller's listing of the same physical
-- product, in one region, at one moment.
--
-- The minimum price is stored on the slice rather than recomputed from the
-- items, because the payload sends its own minimum and its own owner, and the
-- two can disagree with the page's contents when the page is a window onto a
-- larger group.
CREATE TABLE duplicates (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    match_id           INTEGER NOT NULL,
    dest               TEXT    NOT NULL,
    ts                 INTEGER NOT NULL,
    -- total is the payload's own count of the whole group, not the number of
    -- rows in duplicate_items, which is only this page of it.
    total              INTEGER NOT NULL DEFAULT 0,
    min_price          INTEGER,
    min_price_currency TEXT    NOT NULL DEFAULT '',
    min_price_nm_id    INTEGER
) STRICT;

CREATE INDEX idx_duplicates_match_dest_ts ON duplicates(match_id, dest, ts);

CREATE TABLE duplicate_items (
    duplicate_id INTEGER NOT NULL REFERENCES duplicates(id) ON DELETE CASCADE,
    position     INTEGER NOT NULL,
    nm_id        INTEGER NOT NULL,
    PRIMARY KEY (duplicate_id, position)
) STRICT;

-- observations is one reading of one thing, with the context that reading is
-- only true in. dest and app_type are that context: price, stock and the
-- delivery window all move with the region, and the app is shown offers the
-- web is not, so two readings from different contexts differ in ways that are
-- all real and all meaningless.
--
-- kind is text and not the iota value of wb.ObservationKind. Those numbers are
-- nobody's contract -- inserting a kind into the middle of that const block
-- would silently relabel every row already written -- while the String() form
-- is stable. There is no CHECK on it for the same reason source has none: the
-- catalogue lives in wb and grows there, and a released database must not
-- refuse a reading because a new kind exists.
--
-- payload is the reading itself as JSON. It is what makes an observation
-- comparable months later, when the Go value it came from is long gone.
CREATE TABLE observations (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    observed_at INTEGER NOT NULL,
    dest        TEXT    NOT NULL DEFAULT '',
    app_type    INTEGER NOT NULL DEFAULT 0,
    kind        TEXT    NOT NULL,
    payload     TEXT    NOT NULL DEFAULT ''
) STRICT;

CREATE INDEX idx_observations_kind_at ON observations(kind, observed_at);

-- events is one thing worth telling somebody about.
--
-- kind is wb.EventKind's own string. That spelling is deliberately part of
-- that package's contract, because it is what a stored event will still say
-- months after it was written; storing anything else here would orphan the
-- history on the first rename.
--
-- observed_at is when the reading that revealed the event was taken, not when
-- the change happened: the change happened somewhere between two readings and
-- no amount of care is more precise than that.
--
-- nm_id and imt_id are the site's two numberings of what the event is about,
-- and zero in either means "this event does not name one". That is a real
-- case, not a defect: a floor violation read from a payload that withheld the
-- owner names a price and nothing to look at.
--
-- confidence is how much of the event was read and how much was concluded.
-- An operator acting on a certainty and on a guess is doing different things,
-- and a system that presents both the same way teaches them to trust neither.
CREATE TABLE events (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    kind        TEXT    NOT NULL,
    observed_at INTEGER NOT NULL,
    nm_id       INTEGER NOT NULL DEFAULT 0,
    imt_id      INTEGER NOT NULL DEFAULT 0,
    dest        TEXT    NOT NULL DEFAULT '',
    confidence  REAL    NOT NULL DEFAULT 0
) STRICT;

CREATE INDEX idx_events_nm_observed_at ON events(nm_id, observed_at);
CREATE INDEX idx_events_kind_observed_at ON events(kind, observed_at);

-- event_changes is the field-level evidence behind an event's headline:
-- "sizes[45].price.product went from 82400 to 70000" under "a competitor cut
-- their price". position keeps the order the differ produced them in, which is
-- the order they read sensibly in.
CREATE TABLE event_changes (
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    field    TEXT    NOT NULL,
    was      TEXT    NOT NULL,
    now      TEXT    NOT NULL,
    PRIMARY KEY (event_id, position)
) STRICT;
