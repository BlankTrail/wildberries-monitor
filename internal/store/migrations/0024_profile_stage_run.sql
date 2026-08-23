-- Which run a chain's stage is waiting on, told apart from the ones before it.
--
-- The chain reuses one job per stage across rescans, so «the newest run of that
-- job» is the previous pass's finished run for as long as the new one has not
-- opened — and the chain read that as «this stage is done» and skipped a stage
-- it had only just started.
ALTER TABLE profiles ADD COLUMN stage_run INTEGER NOT NULL DEFAULT 0;
