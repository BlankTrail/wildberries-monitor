-- Which job's run wrote this reading.
--
-- «Результаты этого задания» was answered through job_products until now: the
-- set of articles a job walked, joined to every reading of them. That answers
-- «мои товары отсюда», which is a real question and the one somebody usually
-- means — but it is not «что собрало вот это». A product a job collected once
-- in July brings its August readings along, and readings taken by a different
-- job for the same article are indistinguishable from this one's.
--
-- The column is the exact answer for everything written after it exists.
-- Nullable, and null means one of two things that read the same way: a reading
-- from before this migration, or a reading nothing scheduled — a profile
-- resolution, a shelf's holder product, a directory refresh. Neither belongs
-- to a job, and both are correctly missing from a job's own results.
--
-- Readings from before it are not lost to the filter: see ProductFilter.JobID,
-- which falls back to job_products for exactly the rows this column cannot
-- speak for, so an existing database keeps answering while new readings start
-- answering exactly.
--
-- One job per row, not a set. A reading the store thinned away — because an
-- identical one had just been written for the same article, region and
-- audience — belongs to whoever wrote it, and the second job sees that same
-- row through its own other readings of the product. A link table would make
-- that case exact at the cost of a row per reading per job, on the one table
-- that already grows fastest.
ALTER TABLE snapshots ADD COLUMN job_id INTEGER;

-- The direction the results screen reads it in.
CREATE INDEX idx_snapshots_job ON snapshots(job_id);
