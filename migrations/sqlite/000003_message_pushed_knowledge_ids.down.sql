-- SQLite versions before 3.35 do not support DROP COLUMN; if upgrading from
-- an older version this down migration must be replaced with a table rebuild.
ALTER TABLE messages DROP COLUMN pushed_knowledge_ids;
