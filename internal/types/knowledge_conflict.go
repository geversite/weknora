package types

import "time"

// KnowledgeConflict records a content conflict detected between two knowledge
// files within the same KB. One row = one conflicting chunk pair. Rows are
// created post-upload by the async conflict-detection pipeline and are resolved
// by an Owner/Admin through the adjudication queue (M3).
type KnowledgeConflict struct {
	ID              string `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64 `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	// KnowledgeIDA is typically the newly-uploaded file; KnowledgeIDB the
	// pre-existing file whose chunk semantically overlaps with A's.
	KnowledgeIDA string `json:"knowledge_id_a" gorm:"type:varchar(36);index"`
	KnowledgeIDB string `json:"knowledge_id_b" gorm:"type:varchar(36);index"`
	ChunkIDA     string `json:"chunk_id_a" gorm:"type:varchar(36);index"`
	ChunkIDB     string `json:"chunk_id_b" gorm:"type:varchar(36);index"`
	// ContentA / ContentB are snapshots of the conflicting chunks taken at
	// detection time. They remain stable even if the source chunk is later
	// edited (adjudication targets the concrete contradiction found).
	ContentA       string     `json:"content_a" gorm:"type:text"`
	ContentB       string     `json:"content_b" gorm:"type:text"`
	ConflictType   string     `json:"conflict_type" gorm:"type:varchar(32);default:'fact_contradiction'"`
	LLMReason      string     `json:"llm_reason" gorm:"type:text"`
	Status         string     `json:"status" gorm:"type:varchar(20);default:'pending';index"`
	ResolvedBy     string     `json:"resolved_by" gorm:"type:varchar(64)"`
	ResolvedAt     *time.Time `json:"resolved_at"`
	ResolutionNote string     `json:"resolution_note" gorm:"type:text"`
	DetectedBy     string     `json:"detected_by" gorm:"type:varchar(20);default:'upload'"`
	CreatedAt      time.Time  `json:"created_at" gorm:"index"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// TableName returns the table name for the KnowledgeConflict model.
func (KnowledgeConflict) TableName() string { return "knowledge_conflicts" }

// ConflictType constants
const (
	// ConflictTypeFactContradiction: mutually exclusive claims about the SAME fact.
	ConflictTypeFactContradiction = "fact_contradiction"
	// ConflictTypePartialContradiction: largely agree but contradict on specific points.
	ConflictTypePartialContradiction = "partial_contradiction"
	// ConflictTypeVersionUpdate: one is a newer revision/supersession of the other (not a real conflict).
	ConflictTypeVersionUpdate = "version_update"
)

// ConflictStatus constants
const (
	ConflictStatusPending             = "pending"
	ConflictStatusResolvedKeepBoth    = "resolved_keep_both"
	ConflictStatusResolvedNewer       = "resolved_newer_wins"
	ConflictStatusResolvedOlder       = "resolved_older_wins"
	ConflictStatusResolvedNotConflict = "resolved_not_conflict"
)

// ConflictDetectedBy constants
const (
	ConflictDetectedByUpload = "upload"
	ConflictDetectedByManual = "manual"
)

// ConflictResolution is the payload for adjudicating a conflict.
type ConflictResolution struct {
	ConflictID string `json:"conflict_id"`
	Resolution string `json:"resolution"`
	Note       string `json:"note,omitempty"`
}

// ConflictAdjudicationResult describes the side-effects of a resolution so
// the service can disable / demote the adjudicated-losing chunks and clear
// their rerank penalty.
type ConflictAdjudicationResult struct {
	ConflictID           string   `json:"conflict_id"`
	DisabledChunkIDs     []string `json:"disabled_chunk_ids"`      // chunks disabled (adjudicated wrong/outdated side)
	DemotedKnowledgeIDs  []string `json:"demoted_knowledge_ids"`   // files to demote (non-disable)
	ClearPenaltyChunkIDs []string `json:"clear_penalty_chunk_ids"` // chunks no longer under penalty
}

// IsPending reports whether the conflict has not yet been adjudicated.
func (c *KnowledgeConflict) IsPending() bool {
	return c != nil && c.Status == ConflictStatusPending
}
