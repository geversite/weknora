-- Migration: 000005_folder_governance (sqlite, down)
DROP INDEX IF EXISTS idx_chunks_folder;
ALTER TABLE chunks DROP COLUMN folder_id;
DROP INDEX IF EXISTS idx_knowledges_folder;
ALTER TABLE knowledges DROP COLUMN folder_id;
DROP TABLE IF EXISTS folder_summaries;
DROP TABLE IF EXISTS knowledge_folders;
