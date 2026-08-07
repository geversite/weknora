-- Migration: 000005_folder_governance (sqlite)
-- M4: File-level folder hierarchy for knowledge entries.
CREATE TABLE IF NOT EXISTS knowledge_folders (
    id TEXT PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id TEXT NOT NULL,
    parent_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL,
    path TEXT NOT NULL DEFAULT '/',
    depth INTEGER NOT NULL DEFAULT 0,
    sort_order INTEGER NOT NULL DEFAULT 0,
    summary_status TEXT NOT NULL DEFAULT 'none',
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at DATETIME
);
CREATE INDEX IF NOT EXISTS idx_kfolders_tenant ON knowledge_folders (tenant_id);
CREATE INDEX IF NOT EXISTS idx_kfolders_kb ON knowledge_folders (knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_kfolders_parent ON knowledge_folders (knowledge_base_id, parent_id);
CREATE INDEX IF NOT EXISTS idx_kfolders_path ON knowledge_folders (knowledge_base_id, path);

CREATE TABLE IF NOT EXISTS folder_summaries (
    id TEXT PRIMARY KEY,
    tenant_id INTEGER NOT NULL,
    knowledge_base_id TEXT NOT NULL,
    folder_id TEXT NOT NULL UNIQUE,
    content TEXT NOT NULL DEFAULT '',
    content_format TEXT NOT NULL DEFAULT 'markdown',
    is_manual_edit INTEGER NOT NULL DEFAULT 0,
    summary_version INTEGER NOT NULL DEFAULT 0,
    generated_at DATETIME,
    edited_at DATETIME,
    input_snapshot TEXT,
    created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS idx_fsummaries_kb ON folder_summaries (knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_fsummaries_folder ON folder_summaries (folder_id);

ALTER TABLE knowledges ADD COLUMN folder_id TEXT DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_knowledges_folder ON knowledges (knowledge_base_id, folder_id);

ALTER TABLE chunks ADD COLUMN folder_id TEXT DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_chunks_folder ON chunks (folder_id);
