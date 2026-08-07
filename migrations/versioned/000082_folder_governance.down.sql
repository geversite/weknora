-- Migration: 000082_folder_governance (down)
DROP INDEX IF EXISTS idx_chunks_folder;
ALTER TABLE chunks DROP COLUMN IF EXISTS folder_id;
DROP INDEX IF EXISTS idx_knowledges_folder;
ALTER TABLE knowledges DROP COLUMN IF EXISTS folder_id;
DROP TABLE IF EXISTS folder_summaries;
DROP TABLE IF EXISTS knowledge_folders;
