-- Migration: 000084_knowledge_push_allowed (down)
-- Description: 回滚 knowledge.push_allowed 列

ALTER TABLE knowledge DROP COLUMN IF EXISTS push_allowed;
