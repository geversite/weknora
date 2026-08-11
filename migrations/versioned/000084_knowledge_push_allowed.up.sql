-- Migration: 000084_knowledge_push_allowed
-- Description: 为 knowledges 表添加 push_allowed 列，控制单个文档是否允许被推送/生成下载链接。
-- 默认 true 保持向后兼容（现有文档维持原有"可推送"行为）。

DO $$ BEGIN RAISE NOTICE '[Migration 000084] Adding knowledges.push_allowed...'; END $$;

ALTER TABLE knowledges
    ADD COLUMN IF NOT EXISTS push_allowed BOOLEAN NOT NULL DEFAULT TRUE;

DO $$ BEGIN RAISE NOTICE '[Migration 000084] knowledges.push_allowed ready'; END $$;
