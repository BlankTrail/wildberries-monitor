-- Which promotion a product was in at this reading.
--
-- Spec section 4.3 lists «промо-метка и промо-цена» among what a snapshot
-- carries, and section 4.4 prices the whole promo group at «метки бесплатно с
-- деталями» — the mark rides on every listing beside the rating and the stock.
-- It was being read past.
--
-- The promo price needs no column: the price a promotion charges is the sale
-- price, which price_sale already holds. A second column for the same number
-- is the mistake migration 0027 dropped four columns for.
--
-- Nullable, and nil means the payload named no promotion — which for this
-- field is also what «не в акции» looks like, because the site omits the key
-- rather than sending a nought. The two are not distinguishable from the
-- response and this column does not pretend otherwise.
--
-- In the change digest, unlike the raw payload beside it: joining a promotion
-- and leaving one are exactly the changes section 6.1 names as PromoJoined and
-- PromoLeft, and a reading that recorded one while the digest ignored it would
-- be thinned away.
ALTER TABLE snapshots ADD COLUMN promo_id INTEGER;

-- The direction the results table reads it in: «покажи всё, что сейчас в этой
-- акции».
CREATE INDEX idx_snapshots_promo ON snapshots(promo_id);
