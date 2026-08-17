-- SPDX-License-Identifier: AGPL-3.0-or-later
--
-- A phrase list is a file of key phrases the user uploaded, kept here rather
-- than inside the job that uses it.
--
-- Kept out of the job's own params for one reason: size. A params column is
-- read whole every time the job is read, and a job whose params hold a
-- hundred thousand phrases costs several megabytes to answer "what is this
-- job called". Rows are read by the page, and the planner streams them.
--
-- Its own object rather than rows keyed on a job, because the upload happens
-- while the job is still being composed and has no id yet, and because a list
-- worth uploading once is worth pointing a second job at.
--
-- Not the phrases table from 0004. That one belongs to a seller profile and
-- carries a state machine about whether a product ranks for a phrase; this is
-- a flat list of what to search for. Sharing the table would mean either a
-- nullable profile or a fake one, and a state column that means nothing here.
CREATE TABLE phrase_lists (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    name       TEXT    NOT NULL,
    -- count is what actually landed after blanks and duplicates were dropped,
    -- not how many lines the file had. It is stored rather than counted on
    -- demand because the constructor's estimate reads it on every keystroke
    -- and COUNT(*) over a hundred thousand rows is not that.
    count      INTEGER NOT NULL,
    created_at INTEGER NOT NULL
) STRICT;

-- phrase_list_items is one phrase.
--
-- The key is (list, text), not (list, position): duplicates in an uploaded
-- file are common — the same phrase in two exported groups — and each one
-- would otherwise be paid for twice at full price. Deduplicating on the way
-- in through this index is also what lets the upload stream: the index does
-- the remembering, so the server never holds the file's phrases in memory.
--
-- position is kept beside it so the list can be shown and planned in the
-- order the file had. It is not unique: it is provenance, not identity.
CREATE TABLE phrase_list_items (
    list_id  INTEGER NOT NULL REFERENCES phrase_lists(id) ON DELETE CASCADE,
    text     TEXT    NOT NULL,
    position INTEGER NOT NULL,
    PRIMARY KEY (list_id, text)
) STRICT;

CREATE INDEX idx_phrase_list_items_order ON phrase_list_items(list_id, position);
