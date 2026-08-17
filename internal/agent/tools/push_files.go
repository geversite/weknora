package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/Tencent/WeKnora/internal/utils"
)

var pushFilesTool = BaseTool{
	name: ToolPushFiles,
	description: `Generate downloadable links for knowledge base files and present them as cards to the user.

## Purpose
Use this tool when the user explicitly requests a file (e.g. "把那份手册发我", "download link for that manual") OR when the best deliverable is the file itself (templates, manuals, reports, datasets).

## What This Tool Does
- Generates time-limited download links for the specified knowledge files.
- Links are presented as structured cards (filename / type / size / expiry).
- Pushed files are recorded as citations (reference_type=push).

## What This Tool Does NOT Do
- Does NOT read or summarize file content (use knowledge_search / list_knowledge_chunks for that).
- Does NOT push files outside the session's accessible knowledge bases.
- Does NOT generate links for files that have no downloadable storage path.

## Parameters
- knowledge_ids (required): 1–10 document identifiers. Use the EXACT value you saw, preferring a real knowledge_id. Sources of valid IDs:
  - In a wiki workflow: the real knowledge_id value inside <source knowledge_id="..."> tags returned by wiki_read_page / wiki_search (under the sources section). Use that literal value directly.
  - In a RAG workflow: the short dN document handles surfaced by knowledge_search / grep_chunks / list_knowledge_chunks.
- expiry_hours (optional): link validity in hours (default 24, max 168=7d).

## CRITICAL — handle integrity
- Pass a REAL, literal identifier that appeared in your read/search results. NEVER invent, guess, extrapolate, or "index" a handle.
- NEVER fabricate a dN-style token (e.g. d59) when you were never given one — a wiki workflow provides real knowledge_id UUIDs (use those), not dN tokens. A fabricated dN is unresolvable and the whole call is rejected.
- In a wiki workflow, a document's summary page slug has the form summary/<knowledge_id>. You MAY pass that slug verbatim as a knowledge_ids value — the tool automatically strips the summary/ prefix and resolves it to the real document. This is the SAFEST option when a wiki page links to a document as [[summary/<id>|title]].
- folder_summary/<id> and folder/<id> are folder nodes, NOT documents; stripping their prefix yields a folder id and the push will fail. Do not pass folder nodes — read the folder's page (wiki_read_page) to find the concrete document, then push that document (via its summary/<knowledge_id> slug or real knowledge_id).
- A fabricated identifier makes the whole tool call fail (success=false, 0 links generated).

## Output
Returns a file_push card list. Each card has: filename, file_type, file_size, download_url, expires_at.
The agent should briefly explain what each file is in the final answer alongside the cards.`,
	schema: json.RawMessage(`{
  "type": "object",
  "properties": {
    "knowledge_ids": {
      "type": "array",
      "description": "REQUIRED: 1-10 document IDs to push. Valid: (a) a document's summary/ page slug (e.g. summary/<knowledge_id>) — the tool strips the prefix automatically; (b) a real knowledge_id UUID from wiki_read_page/wiki_search <source knowledge_id=\"...\"> tags; (c) a short dN handle from knowledge_search/grep_chunks. Never invent/extrapolate an ID; never pass folder nodes (folder_summary/<id>, folder/<id>).",
      "items": {"type": "string"},
      "minItems": 1,
      "maxItems": 10
    },
    "expiry_hours": {
      "type": "integer",
      "description": "Optional: link validity in hours (default 24, max 168)",
      "minimum": 1,
      "maximum": 168
    }
  },
  "required": ["knowledge_ids"]
}`),
}

// PushFilesInput defines the input for push_files tool.
type PushFilesInput struct {
	KnowledgeIDs []string `json:"knowledge_ids"`
	ExpiryHours  int      `json:"expiry_hours,omitempty"`
}

// PushedFileCard is one element of the push result card list.
type PushedFileCard struct {
	KnowledgeID      string `json:"knowledge_id"`
	KnowledgeBaseID  string `json:"knowledge_base_id"`
	Title            string `json:"title"`
	FileName         string `json:"file_name"`
	FileType         string `json:"file_type"`
	FileSize         int64  `json:"file_size"`
	DownloadURL      string `json:"download_url"`
	ExpiresAt        string `json:"expires_at"` // RFC3339
	ExpiresInHours   int    `json:"expires_in_hours"`
	StorageProvider  string `json:"storage_provider"`             // local/s3/cos/... (仅供前端展示图标)
	PushFailedReason string `json:"push_failed_reason,omitempty"` // 失败原因（如文件无存储路径）
}

// PushFilesTool generates downloadable links for KB files.
type PushFilesTool struct {
	BaseTool
	knowledgeService interfaces.KnowledgeService
	fileService      interfaces.FileService // 用于 GetFileURL
	searchTargets    types.SearchTargets    // 会话可见 KB 范围（权限边界）
	tenantID         uint64
}

// NewPushFilesTool creates a new push_files tool.
func NewPushFilesTool(
	knowledgeService interfaces.KnowledgeService,
	fileService interfaces.FileService,
	searchTargets types.SearchTargets,
	tenantID uint64,
) *PushFilesTool {
	return &PushFilesTool{
		BaseTool:         pushFilesTool,
		knowledgeService: knowledgeService,
		fileService:      fileService,
		searchTargets:    searchTargets,
		tenantID:         tenantID,
	}
}

// Execute runs the push_files tool.
func (t *PushFilesTool) Execute(ctx context.Context, args json.RawMessage) (*types.ToolResult, error) {
	logger.Infof(ctx, "[Tool][PushFiles] Execute started")

	var input PushFilesInput
	if err := json.Unmarshal(args, &input); err != nil {
		return &types.ToolResult{
			Success: false,
			Error:   fmt.Sprintf("failed to parse args: %v", err),
		}, nil
	}

	if len(input.KnowledgeIDs) == 0 {
		return &types.ToolResult{Success: false, Error: "knowledge_ids is required"}, nil
	}
	if len(input.KnowledgeIDs) > 10 {
		return &types.ToolResult{Success: false, Error: "at most 10 files can be pushed at once"}, nil
	}

	expiryHours := input.ExpiryHours
	if expiryHours <= 0 {
		expiryHours = 24 // 默认 24h
	}
	if expiryHours > 168 {
		expiryHours = 168
	}

	// 构造可见 KB 集合用于权限校验（与 knowledge_search 同一权限边界）。
	allowedKB := make(map[string]bool)
	for _, target := range t.searchTargets {
		if target != nil && target.KnowledgeBaseID != "" {
			allowedKB[target.KnowledgeBaseID] = true
		}
	}

	cards := make([]PushedFileCard, 0, len(input.KnowledgeIDs))
	searchResults := make([]*types.SearchResult, 0, len(input.KnowledgeIDs)) // 挂载到 ToolResult.SearchResults 以触发引用计数
	var failedIDs []string

	for _, kid := range input.KnowledgeIDs {
		// The model may hand us a wiki page slug (summary/<id>, folder_summary/<id>)
		// instead of the bare knowledge_id. Strip the wiki prefix so the lookup
		// still reaches the underlying document.
		card, sr, ok := t.pushOneFile(ctx, normalizeKnowledgeID(kid), allowedKB, expiryHours)
		cards = append(cards, card)
		if sr != nil {
			searchResults = append(searchResults, sr)
		}
		if !ok {
			failedIDs = append(failedIDs, kid)
		}
	}

	// 构造结构化 Data（前端按 display_type=file_push 渲染卡片）
	data := map[string]interface{}{
		"display_type": "file_push",
		"files":        cards,
		"count":        len(cards),
		"failed_count": len(failedIDs),
	}

	// 人类可读 Output（LLM 可见，用于在最终回答中提及推送的文件）
	var outputParts []string
	outputParts = append(outputParts, fmt.Sprintf("Generated %d download link(s):", len(cards)-len(failedIDs)))
	for _, c := range cards {
		if c.PushFailedReason != "" {
			outputParts = append(outputParts, fmt.Sprintf("- [FAILED] %s (%s): %s", c.Title, c.KnowledgeID, c.PushFailedReason))
		} else {
			outputParts = append(outputParts, fmt.Sprintf("- %s (%s, %s) — expires in %dh — download: %s",
				c.Title, strings.ToUpper(c.FileType), humanFileSize(c.FileSize), c.ExpiresInHours, c.DownloadURL))
		}
	}
	if len(failedIDs) > 0 {
		outputParts = append(outputParts, fmt.Sprintf("Note: %d file(s) could not be pushed (see FAILED entries).", len(failedIDs)))
	}
	outputParts = append(outputParts, "The download cards are already shown to the user. In your final answer, ALSO provide each successfully pushed file as a markdown download link in this exact form: [<file title>](<download url>), one per line, under a heading like \"文件下载\" or \"Download\". Do not invent URLs—use only the download URLs above.")

	pushedIDs := make([]string, 0, len(cards))
	for _, c := range cards {
		if c.PushFailedReason == "" {
			pushedIDs = append(pushedIDs, c.KnowledgeID)
		}
	}

	return &types.ToolResult{
		Success:            len(failedIDs) == 0,
		Output:             strings.Join(outputParts, "\n"),
		Data:               data,
		SearchResults:      searchResults, // ← 触发引用计数（M1 已实现收集）
		PushedKnowledgeIDs: pushedIDs,     // ← M2：标记被推送的文件
	}, nil
}

// pushOneFile resolves one knowledge file, checks permission, generates URL.
// Returns (card, searchResult, ok). searchResult is non-nil only on success.
func (t *PushFilesTool) pushOneFile(
	ctx context.Context,
	knowledgeID string,
	allowedKB map[string]bool,
	expiryHours int,
) (PushedFileCard, *types.SearchResult, bool) {
	// 1. 查询文件元数据（含 KnowledgeBaseID / FilePath / Title / FileSize ...）
	knowledge, err := t.knowledgeService.GetKnowledgeByIDOnly(ctx, knowledgeID)
	if err != nil || knowledge == nil {
		if err == nil {
			err = fmt.Errorf("empty result")
		}
		return PushedFileCard{
			KnowledgeID:      knowledgeID,
			PushFailedReason: fmt.Sprintf("file not found: %v", err),
		}, nil, false
	}

	// 2. 权限校验：文件所属 KB 必须在会话可见范围内
	if !allowedKB[knowledge.KnowledgeBaseID] {
		return PushedFileCard{
			KnowledgeID:      knowledgeID,
			Title:            knowledge.Title,
			KnowledgeBaseID:  knowledge.KnowledgeBaseID,
			PushFailedReason: "file is not in an accessible knowledge base",
		}, nil, false
	}

	// 3. 校验文件为已启用的文件型知识，且有可下载的存储路径
	if knowledge.EnableStatus != "" && knowledge.EnableStatus != "enabled" {
		return PushedFileCard{
			KnowledgeID:      knowledgeID,
			Title:            knowledge.Title,
			KnowledgeBaseID:  knowledge.KnowledgeBaseID,
			PushFailedReason: "file is disabled",
		}, nil, false
	}
	// 3.1 校验文档是否允许推送（push_allowed=false 时拒绝生成下载链接）
	if knowledge.PushAllowed != nil && !*knowledge.PushAllowed {
		return PushedFileCard{
			KnowledgeID:      knowledgeID,
			Title:            knowledge.Title,
			KnowledgeBaseID:  knowledge.KnowledgeBaseID,
			PushFailedReason: "因管理员设置，该文件暂不支持下载",
		}, nil, false
	}
	if knowledge.FilePath == "" {
		return PushedFileCard{
			KnowledgeID:      knowledgeID,
			Title:            knowledge.Title,
			KnowledgeBaseID:  knowledge.KnowledgeBaseID,
			PushFailedReason: "file has no storage path (e.g. manual/url type knowledge)",
		}, nil, false
	}

	// 4. 生成下载 URL（复用 FileService.GetFileURL）
	downloadURL, err := t.generateDownloadURL(ctx, knowledge, expiryHours)
	if err != nil {
		return PushedFileCard{
			KnowledgeID:      knowledgeID,
			Title:            knowledge.Title,
			KnowledgeBaseID:  knowledge.KnowledgeBaseID,
			PushFailedReason: fmt.Sprintf("failed to generate download URL: %v", err),
		}, nil, false
	}

	expiresAt := time.Now().Add(time.Duration(expiryHours) * time.Hour)
	card := PushedFileCard{
		KnowledgeID:     knowledgeID,
		KnowledgeBaseID: knowledge.KnowledgeBaseID,
		Title:           knowledge.Title,
		FileName:        knowledge.FileName,
		FileType:        knowledge.FileType,
		FileSize:        knowledge.FileSize,
		DownloadURL:     downloadURL,
		ExpiresAt:       expiresAt.Format(time.RFC3339),
		ExpiresInHours:  expiryHours,
		StorageProvider: types.ParseProviderScheme(knowledge.FilePath),
	}

	// 构造 SearchResult 挂载（触发 M1 引用追踪）
	sr := &types.SearchResult{
		KnowledgeID:     knowledgeID,
		KnowledgeBaseID: knowledge.KnowledgeBaseID,
		KnowledgeTitle:  knowledge.Title,
	}

	return card, sr, true
}

// generateDownloadURL tries FileService.GetFileURL first; falls back to
// /api/v1/files/presigned HMAC signing for local storage without APP_EXTERNAL_URL.
func (t *PushFilesTool) generateDownloadURL(ctx context.Context, knowledge *types.Knowledge, expiryHours int) (string, error) {
	// 优先用 FileService.GetFileURL（云存储返回原生 presigned HTTP URL）。
	// 注意：local 存储未配置 externalURL 时，GetFileURL 会退回 local://（或经
	// backendScopedFileService 包裹成 storage://）这类内部路径，浏览器无法直接下载，
	// 必须降级到 HMAC 签名 URL，不能原样透出。
	url, err := t.fileService.GetFileURL(ctx, knowledge.FilePath)
	if isHTTPDownloadURL(url, err) {
		return url, nil
	}
	// 回落：local 存储未配 APP_EXTERNAL_URL，直接用 utils.SignFileURL 生成 /api/v1/files/presigned
	// 注意：需要 baseURL，可从环境变量 APP_EXTERNAL_URL 取；若未配置则无法生成 HTTP 链接
	baseURL := os.Getenv("APP_EXTERNAL_URL")
	if baseURL == "" {
		return "", fmt.Errorf("APP_EXTERNAL_URL not configured, cannot generate download URL for local storage")
	}
	// Sign with the file's owning tenant (cross-tenant shared KBs may own the
	// file under a different tenant than the caller's session tenant).
	ownerTenant := knowledge.TenantID
	if ownerTenant == 0 {
		ownerTenant = t.tenantID
	}
	signed, err := utils.SignFileURL(baseURL, knowledge.FilePath, ownerTenant, time.Duration(expiryHours)*time.Hour)
	if err != nil {
		return "", fmt.Errorf("sign file URL failed: %w", err)
	}
	return signed, nil
}

// isHTTPDownloadURL reports whether GetFileURL returned a directly downloadable
// HTTP(S) URL. Internal storage paths such as local:// or storage://backendID/
// are NOT directly downloadable by a browser and must be downgraded to an
// HMAC-signed /api/v1/files/presigned URL instead.
func isHTTPDownloadURL(url string, err error) bool {
	if err != nil || url == "" {
		return false
	}
	return strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")
}

// humanFileSize formats a byte count into a human-readable string.
func humanFileSize(size int64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)
	switch {
	case size >= GB:
		return fmt.Sprintf("%.2f GB", float64(size)/GB)
	case size >= MB:
		return fmt.Sprintf("%.2f MB", float64(size)/MB)
	case size >= KB:
		return fmt.Sprintf("%.2f KB", float64(size)/KB)
	default:
		return fmt.Sprintf("%d B", size)
	}
}
