-- Migration: 000082_folder_governance
-- M4: File-level folder hierarchy for knowledge entries.
-- Adds knowledge_folders tree, folder_summaries, knowledges.folder_id,
-- chunks.folder_id, and a new chunk_type 'folder_summary'.
DO $$ BEGIN RAISE NOTICE '[Migration 000082] Folder governance infrastructure'; END $$;

-- 1. 文件夹树表（结构与 wiki_folders 对称）
CREATE TABLE IF NOT EXISTS knowledge_folders (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    parent_id VARCHAR(36) NOT NULL DEFAULT '',   -- 根为空串
    name VARCHAR(255) NOT NULL,
    path VARCHAR(1024) NOT NULL DEFAULT '/',     -- 物化路径，根为 '/'
    depth INT NOT NULL DEFAULT 0,
    sort_order INT NOT NULL DEFAULT 0,
    summary_status VARCHAR(32) NOT NULL DEFAULT 'none',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP WITH TIME ZONE
);

CREATE INDEX IF NOT EXISTS idx_kfolders_tenant ON knowledge_folders (tenant_id);
CREATE INDEX IF NOT EXISTS idx_kfolders_kb ON knowledge_folders (knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_kfolders_parent ON knowledge_folders (knowledge_base_id, parent_id);
CREATE INDEX IF NOT EXISTS idx_kfolders_path ON knowledge_folders (knowledge_base_id, path);
CREATE INDEX IF NOT EXISTS idx_kfolders_deleted ON knowledge_folders (deleted_at);

COMMENT ON TABLE knowledge_folders IS 'M4: File-level folder tree for knowledge entries (independent from wiki_folders)';

-- 2. 文件夹摘要表
CREATE TABLE IF NOT EXISTS folder_summaries (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id BIGINT NOT NULL,
    knowledge_base_id VARCHAR(36) NOT NULL,
    folder_id VARCHAR(36) NOT NULL,
    content TEXT NOT NULL DEFAULT '',
    content_format VARCHAR(16) NOT NULL DEFAULT 'markdown',
    is_manual_edit BOOLEAN NOT NULL DEFAULT FALSE,
    summary_version INT NOT NULL DEFAULT 0,
    generated_at TIMESTAMP WITH TIME ZONE,
    edited_at TIMESTAMP WITH TIME ZONE,
    input_snapshot JSONB,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (folder_id)
);

CREATE INDEX IF NOT EXISTS idx_fsummaries_tenant ON folder_summaries (tenant_id);
CREATE INDEX IF NOT EXISTS idx_fsummaries_kb ON folder_summaries (knowledge_base_id);
CREATE INDEX IF NOT EXISTS idx_fsummaries_folder ON folder_summaries (folder_id);

COMMENT ON TABLE folder_summaries IS 'M4: LLM-generated folder-level summaries with manual-edit protection';
COMMENT ON COLUMN folder_summaries.is_manual_edit IS 'When true, auto-regeneration requires explicit refresh request';

-- 3. knowledges 表新增 folder_id 归属列
ALTER TABLE knowledges ADD COLUMN IF NOT EXISTS folder_id VARCHAR(36) DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_knowledges_folder ON knowledges (knowledge_base_id, folder_id);

COMMENT ON COLUMN knowledges.folder_id IS 'M4: primary folder assignment (empty = root/unassigned)';

-- 4. chunks 表新增 folder_id（文件夹摘要 chunk 指向文件夹，knowledge_id 留空）
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS folder_id VARCHAR(36) DEFAULT '';
CREATE INDEX IF NOT EXISTS idx_chunks_folder ON chunks (folder_id);

COMMENT ON COLUMN chunks.folder_id IS 'M4: set for folder_summary chunks; empty for normal chunks';

DO $$ BEGIN RAISE NOTICE '[Migration 000082] done'; END $$;
