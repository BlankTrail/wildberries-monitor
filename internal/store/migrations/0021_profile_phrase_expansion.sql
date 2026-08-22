-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- What a profile collects, refined: which of the seller's categories, how deep
-- to expand the phrases, and how many.
--
-- Section 4.7's second step — «расширение через поисковые подсказки WB» — has
-- an address now, and with it a cost: one request per phrase expanded. That
-- makes it the same kind of decision the check already was, and it needs the
-- same kind of bound: a number somebody sets, with zero meaning «сколько
-- есть» rather than «нисколько».
--
-- subjects is the categories. A seller with four hundred goods across nine of
-- them usually cares about three, and the expensive half of onboarding is
-- priced per phrase — so narrowing it to the categories that matter is the
-- difference between an afternoon and a minute. Empty means all of them.
ALTER TABLE profiles ADD COLUMN subjects TEXT NOT NULL DEFAULT '[]';
-- How many phrases to send to the site's own suggestions. Zero is all of them.
ALTER TABLE profiles ADD COLUMN suggest_limit INTEGER NOT NULL DEFAULT 0;
-- How many times to expand: one round takes the candidates made from the cards
-- and asks the site what people type instead; a second round asks the same of
-- what came back. Zero switches the expansion off entirely, which is what a
-- profile that only wants the seller's own words asks for.
ALTER TABLE profiles ADD COLUMN suggest_rounds INTEGER NOT NULL DEFAULT 1;
