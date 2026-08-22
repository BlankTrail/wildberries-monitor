-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The two bounds spec section 4.7 asks for by name: «экран показывает оценку до
-- запуска и позволяет ограничить число кандидатов на товар, число товаров и
-- глубину проверки».
--
-- The depth was already a constant — one page, because that is what a hundred
-- places is and a hundred places is where «рабочая» is drawn. These two are
-- the ones that multiply: a seller with ten thousand goods produces a candidate
-- list nobody asked the size of, and the check that follows is one request per
-- phrase. Zero means «сколько есть», which is the right default for a seller
-- with fifty products and the wrong one for a seller with ten thousand — so the
-- screen says what the number will be before it is spent.
ALTER TABLE profiles ADD COLUMN phrases_per_product INTEGER NOT NULL DEFAULT 0;
ALTER TABLE profiles ADD COLUMN phrase_products INTEGER NOT NULL DEFAULT 0;
