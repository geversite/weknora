package types

import "time"

// FolderSummary is the LLM-generated structured summary of a folder's
// knowledge entries. One row per folder (UNIQUE on folder_id).
// When IsManualEdit is true, auto-regeneration is suppressed until an
// explicit refresh is requested.
type FolderSummary struct {
	ID              string     `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64     `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string     `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	FolderID        string     `json:"folder_id" gorm:"type:varchar(36);uniqueIndex"`
	Content         string     `json:"content" gorm:"type:text"`
	ContentFormat   string     `json:"content_format" gorm:"type:varchar(16);default:'markdown'"`
	IsManualEdit    bool       `json:"is_manual_edit" gorm:"default:false"`
	SummaryVersion  int        `json:"summary_version" gorm:"default:0"`
	GeneratedAt     *time.Time `json:"generated_at"`
	EditedAt        *time.Time `json:"edited_at"`
	InputSnapshot   JSON       `json:"input_snapshot" gorm:"type:json"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func (FolderSummary) TableName() string { return "folder_summaries" }

// FolderSummaryInputSnapshot records the inputs used to generate a summary,
// so we can detect staleness (file added/removed/changed since generation).
type FolderSummaryInputSnapshot struct {
	FileCount      int              `json:"file_count"`
	FileTitles     []string         `json:"file_titles"`
	CitationCounts map[string]int64 `json:"citation_counts"` // knowledgeID -> count
	GeneratedAt    time.Time        `json:"generated_at"`

	// ChildFolderCount is the number of direct child folders at generation
	// time. Parent-folder summaries aggregate child-folder summaries, so a
	// change in the child-folder set (added/removed/renamed) must invalidate
	// the parent summary.
	ChildFolderCount int `json:"child_folder_count,omitempty"`
	// ChildSummaryVersions records each child folder's SummaryVersion at
	// generation time. When any child's summary is regenerated its version
	// bumps, so the parent is considered stale and rescheduled.
	ChildSummaryVersions map[string]int `json:"child_summary_versions,omitempty"` // folderID -> version
}

// FolderSummaryEditRequest is the payload for manual editing.
type FolderSummaryEditRequest struct {
	Content       string `json:"content" binding:"required"`
	ContentFormat string `json:"content_format,omitempty"`
}
