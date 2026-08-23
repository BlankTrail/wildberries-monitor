-- Four pairs of comparison columns that nothing ever wrote, and three of them
-- nothing ever could.
--
-- Migration 0004 declared the whole of spec section 4.7's comparison table at
-- once. Most of it filled up as the collectors arrived; these did not, and the
-- reasons differ enough to be worth stating one at a time rather than deleting
-- in silence.
--
--   * photo_count, rival_photo_count, has_video, rival_has_video — the card's
--     own media. Spec section 4.4 lists photo and video links as one of its
--     nine field groups and the wb package declares that group absent: no
--     client in it fetches a product's media, deliberately, since a checkbox
--     that collects nothing also makes the cost estimate count requests
--     nobody will make. (reviews.photo_count is a different number: how many
--     photographs a customer attached to one review.)
--
--   * position_with_ads, rival_position_with_ads — the place as a shopper
--     sees it, paid seats included. What the ads reading gives is who WB was
--     advertising for a phrase, never where in the page those seats sat, so
--     the interleaved rank cannot be reconstructed from anything collected
--     here. The organic pair stays; the comparison screen says out loud that
--     this one is missing rather than showing an empty column.
--
--   * available, rival_available — in stock or not. This one is different:
--     it could be filled, from total_quantity, which the pair beside it
--     already carries. A column that restates its neighbour is not a fact the
--     screen lacked, it is a second place to read the same number and a wider
--     table to read it in.
--
-- The general rule, which cost this schema an entire milestone of a false
-- «реклама: нет» before it was learned: a column nothing fills is not free.
-- It waits for a reader, and the reader believes it.
ALTER TABLE benchmarks DROP COLUMN photo_count;
ALTER TABLE benchmarks DROP COLUMN rival_photo_count;
ALTER TABLE benchmarks DROP COLUMN has_video;
ALTER TABLE benchmarks DROP COLUMN rival_has_video;
ALTER TABLE benchmarks DROP COLUMN position_with_ads;
ALTER TABLE benchmarks DROP COLUMN rival_position_with_ads;
ALTER TABLE benchmarks DROP COLUMN available;
ALTER TABLE benchmarks DROP COLUMN rival_available;
