-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The promotions Wildberries is running — spec section 4.6's type 8.
--
-- The site publishes what it is running as a list of banners, and each
-- promotion has a record of its own holding the two things nothing else does:
-- the preset its goods are filed under and the shard of the search index they
-- live in. Neither can be computed from the slug; the record is the only place
-- they exist.
--
-- Kept on disk so that the job constructor can offer a promotion without a
-- request per keystroke, and so that a saved job carries the preset it was
-- built against rather than looking it up mid-run — the same trade the
-- catalogue directory makes, for the same reason.
CREATE TABLE promotions (
    -- The tail of the promotion's own address, which is what the site calls it
    -- everywhere a person can see. The numeric id is beside it rather than the
    -- key: a promotion that ends and comes back next season is a new number
    -- and the same name.
    slug       TEXT    PRIMARY KEY,
    name       TEXT    NOT NULL,
    promo_id   INTEGER NOT NULL DEFAULT 0,
    -- shard and query are the address halves, as the site's record writes
    -- them: «promo/bucket_6» and «preset=1005032».
    shard      TEXT    NOT NULL DEFAULT '',
    query      TEXT    NOT NULL DEFAULT '',
    fetched_at INTEGER NOT NULL
) STRICT;
