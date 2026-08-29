-- Migration: 000086_conflict_detection_runs
-- Aggregate measurements for C1/C2 conflict:detect executions. This table is
-- intentionally append-only: zero-conflict and failed runs are research data.
DO $$ BEGIN RAISE NOTICE '[Migration 000086] Creating conflict_detection_runs table'; END $$;

CREATE TABLE IF NOT EXISTS conflict_detection_runs (
    id                         VARCHAR(36) PRIMARY KEY,
    tenant_id                  BIGINT      NOT NULL,
    knowledge_base_id          VARCHAR(36) NOT NULL,
    knowledge_id               VARCHAR(36) NOT NULL,

    cascade_mode               VARCHAR(32) NOT NULL DEFAULT 'legacy',
    detector_version           VARCHAR(32) NOT NULL DEFAULT 'c2-v1',
    status                     VARCHAR(16) NOT NULL DEFAULT 'completed',

    candidate_claim_pairs      INT NOT NULL DEFAULT 0,
    candidate_fallback_pairs   INT NOT NULL DEFAULT 0,
    candidate_after_dedupe     INT NOT NULL DEFAULT 0,
    candidates_submitted       INT NOT NULL DEFAULT 0,

    rule_no_conflict           INT NOT NULL DEFAULT 0,
    rule_direct_conflict       INT NOT NULL DEFAULT 0,
    rule_needs_llm             INT NOT NULL DEFAULT 0,

    llm_pair_count             INT    NOT NULL DEFAULT 0,
    llm_batch_call_count       INT    NOT NULL DEFAULT 0,
    llm_single_call_count      INT    NOT NULL DEFAULT 0,
    llm_single_fallback_count  INT    NOT NULL DEFAULT 0,
    llm_prompt_tokens          BIGINT NOT NULL DEFAULT 0,
    llm_completion_tokens      BIGINT NOT NULL DEFAULT 0,

    final_conflict_count       INT NOT NULL DEFAULT 0,
    duration_ms                BIGINT NOT NULL DEFAULT 0,
    error_message              TEXT NOT NULL DEFAULT '',
    created_at                 TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at                TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_conflict_detection_runs_kb_created
    ON conflict_detection_runs (knowledge_base_id, created_at);
CREATE INDEX IF NOT EXISTS idx_conflict_detection_runs_knowledge
    ON conflict_detection_runs (knowledge_id);
CREATE INDEX IF NOT EXISTS idx_conflict_detection_runs_tenant
    ON conflict_detection_runs (tenant_id);

COMMENT ON TABLE conflict_detection_runs IS 'C1/C2 conflict detector aggregate cost and routing measurements';
COMMENT ON COLUMN conflict_detection_runs.cascade_mode IS 'legacy / rules / rules_batch';
COMMENT ON COLUMN conflict_detection_runs.llm_pair_count IS 'Logical gray-area pair attempts routed to an LLM';

DO $$ BEGIN RAISE NOTICE '[Migration 000086] done'; END $$;
