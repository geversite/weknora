-- Migration: 000080_message_pushed_knowledge_ids
-- M2: track which knowledge files were pushed by the push_files tool,
-- so RecordReferenceEvents can mark the corresponding reference events as
-- reference_type='push' instead of the generic 'agent' type.
DO $$ BEGIN RAISE NOTICE '[Migration 000080] Adding pushed_knowledge_ids to messages'; END $$;

ALTER TABLE messages
    ADD COLUMN IF NOT EXISTS pushed_knowledge_ids JSONB NOT NULL DEFAULT '[]'::jsonb;

COMMENT ON COLUMN messages.pushed_knowledge_ids IS
    'Knowledge IDs pushed by push_files tool in this message; used to tag reference events as push type';

DO $$ BEGIN RAISE NOTICE '[Migration 000080] done'; END $$;
