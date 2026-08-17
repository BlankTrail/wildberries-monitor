-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- The egress tables were declared by migration 0005 without repositories, on
-- the same grounds jobs was: inventing an interface before its producer exists
-- gets the interface wrong. The producer exists now — the channels screen and
-- the engine that opens ports through what it saves — and it wants two things
-- taken away and one added.
--
-- 1. proxies is dropped, and with it the open question its own comment raised.
--    It was to hold each parsed entry of a list channel, credentials included,
--    in cleartext, in the one file a user is expected to copy between machines.
--    It turns out nothing needs it: a list channel is a file or a URL, and the
--    rotor that serves it reads that source itself and re-reads it on its own
--    schedule. Storing a second copy would have bought a cache that goes stale
--    against a list its owner edits, at the price of keeping their proxy
--    passwords on our disk. The credentials now live only where the user put
--    them.
--
-- 2. channels.weight is dropped. It was to be how ports are spread over
--    channels, and that is real — spec section 3.5 asks for it — but it is not
--    an input: the mixer starts every channel level and moves the weight itself
--    as channels produce blocks or die. There is nowhere to send a number from
--    here, and a knob on a screen that changes nothing is worse than no knob.
--
-- 3. channels.default_scheme is added, for the one thing section 3.5 leaves to
--    the user. Four of the five accepted spellings of a proxy line carry no
--    scheme, so a bare "host:port:user:pass" has to be told what it is. Getting
--    it wrong is not a parse error — it is a channel that dials every address
--    the wrong way and looks like a list of dead proxies.
--
-- SQLite rewrites the table for a dropped column, which is why this is a
-- migration and not an application-level default. Both tables are empty in
-- every database that exists: nothing has ever written to either.

DROP TABLE proxies;

ALTER TABLE channels DROP COLUMN weight;

ALTER TABLE channels ADD COLUMN default_scheme TEXT NOT NULL DEFAULT ''
    CHECK (default_scheme IN ('', 'http', 'https', 'socks5', 'socks5h', 'socks4'));
