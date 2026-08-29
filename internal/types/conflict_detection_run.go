package types

import "time"

// ConflictDetectionRun is one durable, research-oriented execution record for
// a conflict:detect task. It intentionally records candidate/cascade counts
// rather than individual content so C1/C2 cost ablations can be exported by
// experiment scripts without scraping interleaved worker logs.
type ConflictDetectionRun struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	KnowledgeID     string `json:"knowledge_id" gorm:"type:varchar(36);index"`

	// CascadeMode is legacy / rules / rules_batch. DetectorVersion identifies
	// the pipeline generation for later reproducibility when rules evolve.
	CascadeMode     string `json:"cascade_mode" gorm:"type:varchar(32)"`
	DetectorVersion string `json:"detector_version" gorm:"type:varchar(32)"`
	Status          string `json:"status" gorm:"type:varchar(16);index"`

	CandidateClaimPairs    int `json:"candidate_claim_pairs"`
	CandidateFallbackPairs int `json:"candidate_fallback_pairs"`
	CandidateAfterDedupe   int `json:"candidate_after_dedupe"`
	CandidatesSubmitted    int `json:"candidates_submitted"`

	RuleNoConflict     int `json:"rule_no_conflict"`
	RuleDirectConflict int `json:"rule_direct_conflict"`
	RuleNeedsLLM       int `json:"rule_needs_llm"`

	// LLMPairCount counts logical gray-area pairs sent to an LLM route. The
	// call counters reflect actual provider requests (including retries and
	// batch-to-single fallback), which is what cost experiments need.
	LLMPairCount           int   `json:"llm_pair_count"`
	LLMBatchCallCount      int   `json:"llm_batch_call_count"`
	LLMSingleCallCount     int   `json:"llm_single_call_count"`
	LLMSingleFallbackCount int   `json:"llm_single_fallback_count"`
	LLMPromptTokens        int64 `json:"llm_prompt_tokens"`
	LLMCompletionTokens    int64 `json:"llm_completion_tokens"`

	FinalConflictCount int       `json:"final_conflict_count"`
	DurationMs         int64     `json:"duration_ms"`
	ErrorMessage       string    `json:"error_message" gorm:"type:text"`
	CreatedAt          time.Time `json:"created_at"`
	FinishedAt         time.Time `json:"finished_at"`
}

func (ConflictDetectionRun) TableName() string { return "conflict_detection_runs" }

const (
	ConflictDetectionRunStatusCompleted = "completed"
	ConflictDetectionRunStatusFailed    = "failed"
	ConflictDetectionRunStatusSkipped   = "skipped"

	// ConflictDetectorVersion is bumped whenever C2 decision semantics change
	// in a way that would make historical cost/quality runs non-comparable.
	ConflictDetectorVersion = "c2-v1"
)
