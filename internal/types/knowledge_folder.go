package types

import (
	"time"

	"gorm.io/gorm"
)

// KnowledgeFolder is a node in the file-level folder tree that organizes
// knowledge entries (files) within a KB. This is INDEPENDENT from wiki_folders
// (which organize wiki pages). Structure mirrors WikiFolder for consistency.
type KnowledgeFolder struct {
	ID              string         `json:"id" gorm:"type:varchar(36);primaryKey"`
	TenantID        uint64         `json:"tenant_id" gorm:"index"`
	KnowledgeBaseID string         `json:"knowledge_base_id" gorm:"type:varchar(36);index"`
	ParentID        string         `json:"parent_id" gorm:"column:parent_id;type:varchar(36);index;default:''"`
	Name            string         `json:"name" gorm:"type:varchar(255)"`
	Path            string         `json:"path" gorm:"type:varchar(1024);default:'/'"`
	Depth           int            `json:"depth" gorm:"default:0"`
	SortOrder       int            `json:"sort_order" gorm:"default:0"`
	SummaryStatus   string         `json:"summary_status" gorm:"type:varchar(32);default:'none'"`
	CreatedAt       time.Time      `json:"created_at"`
	UpdatedAt       time.Time      `json:"updated_at"`
	DeletedAt       gorm.DeletedAt `json:"deleted_at" gorm:"index"`
}

func (KnowledgeFolder) TableName() string { return "knowledge_folders" }

// KnowledgeFolderNode is one tree node returned to the browser, enriched with
// live file count and child-folder presence for UI rendering.
type KnowledgeFolderNode struct {
	KnowledgeFolder
	FileCount   int64 `json:"file_count"`
	HasChildren bool  `json:"has_children"`
}

// KnowledgeFolderTreeResponse is the payload for listing the folder tree.
type KnowledgeFolderTreeResponse struct {
	Nodes []KnowledgeFolderNode `json:"nodes"`
}

// KnowledgeFolderCreateRequest creates a new folder under ParentID.
type KnowledgeFolderCreateRequest struct {
	ParentID string `json:"parent_id"`
	Name     string `json:"name" binding:"required"`
}

// KnowledgeFolderUpdateRequest renames and/or reparents a folder.
// ParentID is applied only when MoveParent is true (mirrors WikiFolderUpdateRequest).
type KnowledgeFolderUpdateRequest struct {
	Name       string `json:"name,omitempty"`
	ParentID   string `json:"parent_id,omitempty"`
	MoveParent bool   `json:"move_parent,omitempty"`
}

// FolderSummaryStatus constants (mirrors Knowledge.SummaryStatus)
const (
	FolderSummaryStatusNone       = "none"
	FolderSummaryStatusPending    = "pending"
	FolderSummaryStatusProcessing = "processing"
	FolderSummaryStatusCompleted  = "completed"
	FolderSummaryStatusFailed     = "failed"
)

// FolderRootID is the special folder_id sentinel meaning "root level"
// (files with folder_id = ”). Used in KnowledgeListFilter.FolderIDs and
// SearchTarget.FolderIDs.
const FolderRootID = "__root__"
