package types

import (
	"database/sql/driver"
	"encoding/json"
)

// IndexingStrategy controls which indexing pipelines are active for a knowledge base.
// Each boolean flag independently enables/disables a processing pipeline.
// When a document is uploaded, only the enabled pipelines will run.
type IndexingStrategy struct {
	// VectorEnabled enables semantic vector embedding and search
	VectorEnabled bool `yaml:"vector_enabled" json:"vector_enabled"`
	// KeywordEnabled enables keyword-based (BM25) search
	KeywordEnabled bool `yaml:"keyword_enabled" json:"keyword_enabled"`
	// WikiEnabled enables automatic wiki page generation from documents
	WikiEnabled bool `yaml:"wiki_enabled" json:"wiki_enabled"`
	// GraphEnabled enables knowledge graph entity/relation extraction
	GraphEnabled bool `yaml:"graph_enabled" json:"graph_enabled"`
	// ConflictDetectEnabled enables post-upload content conflict detection
	// between files within the same KB (M3). Default false; enabled per KB.
	ConflictDetectEnabled bool `yaml:"conflict_detect_enabled" json:"conflict_detect_enabled"`
	// FolderGovernanceEnabled enables file-level folder governance (M4):
	// folder tree, folder summaries, scoped retrieval. Default false; per KB.
	// It does NOT produce indexing requirements by itself, so it is excluded
	// from HasAnyIndexing / IsZero / NeedsChunks / NeedsEmbedding.
	FolderGovernanceEnabled bool `yaml:"folder_governance_enabled" json:"folder_governance_enabled"`
	// UserFeedbackEnabled enables the M5 automatic user-feedback-to-wiki
	// pipeline. When true, every user message is scanned for new factual
	// info and, if found, appended to the relevant wiki page automatically.
	// Requires WikiEnabled. Default false. It does NOT produce indexing
	// requirements by itself, so it is excluded from the indexing helpers.
	UserFeedbackEnabled bool `yaml:"user_feedback_enabled" json:"user_feedback_enabled"`
	// ClaimExtractEnabled enables post-ingest atomic claim extraction (C1,
	// Conflict V2). Claims power claim-key based conflict candidate pairing
	// and (later) corpus sweeps. Default false; per KB. It does NOT produce
	// indexing requirements by itself, so it is excluded from
	// HasAnyIndexing / IsZero / NeedsChunks / NeedsEmbedding.
	ClaimExtractEnabled bool `yaml:"claim_extract_enabled" json:"claim_extract_enabled"`
	// ConflictCascadeMode selects the research conflict verifier after C1
	// candidate generation. Empty/legacy preserves C1's per-pair LLM path;
	// rules enables C2-A's deterministic rule prefilter; rules_batch enables
	// C2-B's batched LLM path. Kept per-KB so experiment scripts can run
	// V1/C1/C2 ablations without process-wide environment changes.
	ConflictCascadeMode string `yaml:"conflict_cascade_mode" json:"conflict_cascade_mode"`
}

// Conflict cascade modes. Unknown/empty values deliberately fall back to
// legacy so an old persisted strategy cannot accidentally change detection.
const (
	ConflictCascadeModeLegacy     = "legacy"
	ConflictCascadeModeRules      = "rules"
	ConflictCascadeModeRulesBatch = "rules_batch"
)

// EffectiveConflictCascadeMode returns a safe, recognized cascade mode.
func (s IndexingStrategy) EffectiveConflictCascadeMode() string {
	switch s.ConflictCascadeMode {
	case ConflictCascadeModeRules, ConflictCascadeModeRulesBatch:
		return s.ConflictCascadeMode
	default:
		return ConflictCascadeModeLegacy
	}
}

// DefaultIndexingStrategy returns the default strategy matching the legacy behavior:
// vector and keyword indexing enabled, wiki and graph disabled.
func DefaultIndexingStrategy() IndexingStrategy {
	return IndexingStrategy{
		VectorEnabled:  true,
		KeywordEnabled: true,
		WikiEnabled:    false,
		GraphEnabled:   false,
	}
}

// NeedsEmbedding returns true if any pipeline that requires an embedding model is enabled.
func (s IndexingStrategy) NeedsEmbedding() bool {
	return s.VectorEnabled || s.KeywordEnabled
}

// NeedsChunks returns true if any pipeline that requires document chunks is enabled.
// Chunks are needed for vector indexing, keyword indexing, wiki generation, and graph extraction.
func (s IndexingStrategy) NeedsChunks() bool {
	return s.VectorEnabled || s.KeywordEnabled || s.WikiEnabled || s.GraphEnabled
}

// HasAnyIndexing returns true if at least one indexing pipeline is enabled.
func (s IndexingStrategy) HasAnyIndexing() bool {
	return s.VectorEnabled || s.KeywordEnabled || s.WikiEnabled || s.GraphEnabled
}

// IsZero returns true if the strategy has no pipelines enabled (zero value).
func (s IndexingStrategy) IsZero() bool {
	return !s.VectorEnabled && !s.KeywordEnabled && !s.WikiEnabled && !s.GraphEnabled
}

// IsUserFeedbackEnabled reports whether the M5 feedback pipeline is active.
// The feature is no longer user-toggleable: it runs whenever wiki indexing is
// enabled (feedback cannot run without wiki).
func (s IndexingStrategy) IsUserFeedbackEnabled() bool {
	return s.WikiEnabled
}

// Value implements the driver.Valuer interface for GORM serialization.
func (s IndexingStrategy) Value() (driver.Value, error) {
	return json.Marshal(s)
}

// Scan implements the sql.Scanner interface for GORM deserialization.
// When the database column is NULL (existing rows before migration),
// it returns DefaultIndexingStrategy() for backward compatibility.
func (s *IndexingStrategy) Scan(value interface{}) error {
	if value == nil {
		*s = DefaultIndexingStrategy()
		return nil
	}
	b, ok := value.([]byte)
	if !ok {
		*s = DefaultIndexingStrategy()
		return nil
	}
	if err := json.Unmarshal(b, s); err != nil {
		*s = DefaultIndexingStrategy()
		return nil
	}
	return nil
}
