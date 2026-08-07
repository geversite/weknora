package types

import "time"

// FolderGovernanceReport is the payload for the folder governance panel (M4).
type FolderGovernanceReport struct {
	EmptyFolders      []FolderEmptyInfo        `json:"empty_folders"`
	ImbalancedFolders []FolderImbalancedInfo   `json:"imbalanced_folders"`
	StaleSummaries    []FolderStaleSummaryInfo `json:"stale_summaries"`
	DuplicateFiles    []FolderDuplicateInfo    `json:"duplicate_files"`
	DeepFolders       []FolderDeepInfo         `json:"deep_folders"`
}

type FolderEmptyInfo struct {
	FolderID string `json:"folder_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
}

type FolderImbalancedInfo struct {
	FolderID   string `json:"folder_id"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	FileCount  int64  `json:"file_count"`
	Suggestion string `json:"suggestion"`
}

type FolderStaleSummaryInfo struct {
	FolderID       string    `json:"folder_id"`
	Name           string    `json:"name"`
	GeneratedAt    time.Time `json:"generated_at"`
	LastFileChange time.Time `json:"last_file_change"`
}

type FolderDuplicateInfo struct {
	FileHash     string   `json:"file_hash"`
	FileName     string   `json:"file_name"`
	FolderPaths  []string `json:"folder_paths"`
	KnowledgeIDs []string `json:"knowledge_ids"`
}

type FolderDeepInfo struct {
	FolderID string `json:"folder_id"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Depth    int    `json:"depth"`
}
