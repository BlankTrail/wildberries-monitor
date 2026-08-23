-- The job that turns a pasted link into a seller, remembered like the other two.
--
-- Without it the collector cannot tell which profile the run it is inside
-- belongs to, so every paste inserted a fresh profile and the chain that was
-- supposed to follow the resolve had nothing to follow.
ALTER TABLE profiles ADD COLUMN resolve_job INTEGER NOT NULL DEFAULT 0;
