-- Migration: 000007_conflict_detection_runs (sqlite / Lite mode)
CREATE TABLE IF NOT EXISTS conflict_detection_runs (
    id                         TEXT PRIMARY KEY,
    tenant_id                  INTEGER NOT NULL,
    knowledge_base_id          TEXT    NOT NULL,
    knowledge_id               TEXT    NOT NULL,

    cascade_mode               TEXT    NOT NULL DEFAULT 'legacy',
    detector_version           TEXT    NOT NULL DEFAULT 'c2-v1',
    status                     TEXT    NOT NULL DEFAULT 'completed',

    candidate_claim_pairs      INTEGER NOT NULL DEFAULT 0,
    candidate_fallback_pairs   INTEGER NOT NULL DEFAULT 0,
    candidate_after_dedupe     INTEGER NOT NULL DEFAULT 0,
    candidates_submitted       INTEGER NOT NULL DEFAULT 0,

    rule_no_conflict           INTEGER NOT NULL DEFAULT 0,
    rule_direct_conflict       INTEGER NOT NULL DEFAULT 0,
    rule_needs_llm             INTEGER NOT NULL DEFAULT 0,

    llm_pair_count             INTEGER NOT NULL DEFAULT 0,
    llm_batch_call_count       INTEGER NOT NULL DEFAULT 0,
    llm_single_call_count      INTEGER NOT NULL DEFAULT 0,
    llm_single_fallback_count  INTEGER NOT NULL DEFAULT 0,
    llm_prompt_tokens          INTEGER NOT NULL DEFAULT 0,
    llm_completion_tokens      INTEGER NOT NULL DEFAULT 0,

    final_conflict_count       INTEGER NOT NULL DEFAULT 0,
    duration_ms                INTEGER NOT NULL DEFAULT 0,
    error_message              TEXT    NOT NULL DEFAULT '',
    created_at                 DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    finished_at                DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_conflict_detection_runs_kb_created
    ON conflict_detection_runs (knowledge_base_id, created_at);
CREATE INDEX IF NOT EXISTS idx_conflict_detection_runs_knowledge
    ON conflict_detection_runs (knowledge_id);
CREATE INDEX IF NOT EXISTS idx_conflict_detection_runs_tenant
    ON conflict_detection_runs (tenant_id);
