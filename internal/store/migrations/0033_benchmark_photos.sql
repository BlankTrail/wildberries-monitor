-- The photograph count is back in the comparison.
--
-- Migration 0027 dropped this pair, and the reason it gave was true at the
-- time: spec section 4.4 lists photo and video links as one of its nine field
-- groups, the wb package declared that group absent, and a column nothing
-- fills is not free — it waits for a reader, and the reader believes it.
--
-- What changed is that the count never needed the group. Every listing the
-- site answers with carries `pics` beside the rating and the stock, and
-- migration 0031 put it on the snapshot. So the one measure of spec section
-- 4.7's card completeness that was missing costs nothing to compare, and the
-- section calls that completeness the one gap a seller can close the same day,
-- without money and without waiting.
--
-- Its neighbours in that measure are already here: description length is
-- computed from the card, and the share of filled characteristics from
-- product_options. Video is the one still absent, and for the reason 0027
-- gave: nothing collects it.
ALTER TABLE benchmarks ADD COLUMN photo_count       INTEGER;
ALTER TABLE benchmarks ADD COLUMN rival_photo_count INTEGER;
