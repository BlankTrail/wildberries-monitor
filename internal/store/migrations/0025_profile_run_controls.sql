-- How a profile's own collection runs: through which exits, in how many
-- threads, with how many attempts per request.
--
-- Every job the chain builds hard-coded all three, so a profile that took an
-- hour could not be told to take twenty minutes, and a person with eight
-- proxies could not say which of them their own assortment should be read
-- through.
ALTER TABLE profiles ADD COLUMN channels TEXT NOT NULL DEFAULT '[]';
ALTER TABLE profiles ADD COLUMN threads INTEGER NOT NULL DEFAULT 0;
ALTER TABLE profiles ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
