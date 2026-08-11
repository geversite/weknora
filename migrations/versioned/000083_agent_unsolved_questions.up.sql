-- Migration: 000083_agent_unsolved_questions
-- Description: 智能体级别的"未解决问题"记录表。每次助手回答完成后由 LLM 判定回答是否完善，
-- 若未完善则记录一行，供智能体编辑页展示与运营复盘。

DO $$ BEGIN RAISE NOTICE '[Migration 000083] Creating agent_unsolved_questions...'; END $$;

CREATE TABLE IF NOT EXISTS agent_unsolved_questions (
    id VARCHAR(36) PRIMARY KEY DEFAULT uuid_generate_v4(),
    tenant_id INTEGER NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
    agent_id VARCHAR(36) NOT NULL,
    agent_tenant_id INTEGER NOT NULL DEFAULT 0,
    session_id VARCHAR(36) NOT NULL,
    assistant_message_id VARCHAR(36) NOT NULL,
    user_question TEXT NOT NULL,
    answer_summary TEXT NOT NULL DEFAULT '',
    reason TEXT NOT NULL DEFAULT '',
    status VARCHAR(16) NOT NULL,
    model_id VARCHAR(64) NOT NULL DEFAULT '',
    prompt_tokens INTEGER NOT NULL DEFAULT 0,
    completion_tokens INTEGER NOT NULL DEFAULT 0,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    error_code VARCHAR(64) NOT NULL DEFAULT '',
    resolved BOOLEAN NOT NULL DEFAULT FALSE,
    generated_at TIMESTAMP WITH TIME ZONE,
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- 按智能体 + 未处理状态查询的索引（编辑页列表主要走这条路径）
CREATE INDEX IF NOT EXISTS idx_agent_unsolved_questions_agent
    ON agent_unsolved_questions(tenant_id, agent_id, resolved, created_at DESC);
-- 按会话查询（用于会话删除时级联清理）
CREATE INDEX IF NOT EXISTS idx_agent_unsolved_questions_session
    ON agent_unsolved_questions(tenant_id, session_id);
-- 按助手消息查询（用于去重 / 重试判定）
CREATE INDEX IF NOT EXISTS idx_agent_unsolved_questions_message
    ON agent_unsolved_questions(tenant_id, assistant_message_id);
-- 按状态查询（用于统计）
CREATE INDEX IF NOT EXISTS idx_agent_unsolved_questions_status
    ON agent_unsolved_questions(status, created_at DESC);

DO $$ BEGIN RAISE NOTICE '[Migration 000083] agent_unsolved_questions ready'; END $$;
