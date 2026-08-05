package types

import "time"

// ReferenceEvent records that a knowledge file was cited by an assistant
// message. One row = one file cited by one message.
type ReferenceEvent struct {
	ID              string    `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64    `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string    `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	KnowledgeID     string    `json:"knowledge_id" gorm:"type:varchar(36);index"`
	MessageID       string    `json:"message_id" gorm:"type:varchar(36)"`
	SessionID       string    `json:"session_id" gorm:"type:varchar(36);index"`
	ReferenceType   string    `json:"reference_type" gorm:"type:varchar(32);default:'rag'"`
	Score           float64   `json:"score"`
	CreatedAt       time.Time `json:"created_at" gorm:"index"`
}

func (ReferenceEvent) TableName() string { return "reference_events" }

// ReferenceType constants.
const (
	ReferenceTypeRAG   = "rag"   // RAG chunk citation
	ReferenceTypeAgent = "agent" // Agent tool citation
	ReferenceTypePush  = "push"  // File push (M2)
	ReferenceTypeWiki  = "wiki"  // Wiki page source doc
)

// CitationStats is the aggregated citation statistics for a KB.
type CitationStats struct {
	TotalCount   int64                    `json:"total_count"`
	RecentCount  int64                    `json:"recent_count"` // 近30天
	TopCited     []KnowledgeCitationCount `json:"top_cited"`
	ZeroCitedIDs []string                 `json:"zero_cited_ids"`
}

// KnowledgeCitationCount is a per-file citation count with last-cited time.
type KnowledgeCitationCount struct {
	KnowledgeID string    `json:"knowledge_id"`
	Count       int64     `json:"count"`
	LastCitedAt time.Time `json:"last_cited_at"`
}
