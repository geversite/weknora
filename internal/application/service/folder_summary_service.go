package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/application/service/retriever"
	"github.com/Tencent/WeKnora/internal/config"
	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"
)

// folderSummaryService implements FolderSummaryTaskService.
type folderSummaryService struct {
	config             *config.Config
	repo               interfaces.KnowledgeRepository
	folderRepo         interfaces.KnowledgeFolderRepository
	summaryRepo        interfaces.FolderSummaryRepository
	kbService          interfaces.KnowledgeBaseService
	tenantRepo         interfaces.TenantRepository
	modelService       interfaces.ModelService
	chunkService       interfaces.ChunkService
	chunkRepo          interfaces.ChunkRepository
	referenceEventRepo interfaces.ReferenceEventRepository
	retrieveEngine     interfaces.RetrieveEngineRegistry
	ownership          retriever.TenantStoreOwnership
	task               interfaces.TaskEnqueuer
}

// NewFolderSummaryService builds the folder summary service. config is optional
// (used for max token limits and prompts); it is read defensively.
func NewFolderSummaryService(
	config *config.Config,
	repo interfaces.KnowledgeRepository,
	folderRepo interfaces.KnowledgeFolderRepository,
	summaryRepo interfaces.FolderSummaryRepository,
	kbService interfaces.KnowledgeBaseService,
	tenantRepo interfaces.TenantRepository,
	modelService interfaces.ModelService,
	chunkService interfaces.ChunkService,
	chunkRepo interfaces.ChunkRepository,
	referenceEventRepo interfaces.ReferenceEventRepository,
	retrieveEngine interfaces.RetrieveEngineRegistry,
	ownership retriever.TenantStoreOwnership,
	task interfaces.TaskEnqueuer,
) *folderSummaryService {
	return &folderSummaryService{
		config:             config,
		repo:               repo,
		folderRepo:         folderRepo,
		summaryRepo:        summaryRepo,
		kbService:          kbService,
		tenantRepo:         tenantRepo,
		modelService:       modelService,
		chunkService:       chunkService,
		chunkRepo:          chunkRepo,
		referenceEventRepo: referenceEventRepo,
		retrieveEngine:     retrieveEngine,
		ownership:          ownership,
		task:               task,
	}
}

func (s *folderSummaryService) Get(ctx context.Context, kbID, folderID string) (*types.FolderSummary, error) {
	summary, err := s.summaryRepo.GetByFolder(ctx, folderID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrFolderSummaryNotReady
		}
		return nil, err
	}
	// ownership guard: folder must belong to the KB
	if summary.KnowledgeBaseID != kbID {
		return nil, ErrFolderSummaryNotReady
	}
	return summary, nil
}

// ListSummariesByKB returns all folder summaries for a KB.
func (s *folderSummaryService) ListSummariesByKB(ctx context.Context, tenantID uint64, kbID string) ([]*types.FolderSummary, error) {
	return s.summaryRepo.ListByKB(ctx, tenantID, kbID)
}

func (s *folderSummaryService) Generate(ctx context.Context, kbID, folderID string) error {
	folder, err := s.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		return err
	}
	if folder.KnowledgeBaseID != kbID {
		return ErrFolderNotFound
	}
	s.ScheduleRefresh(ctx, folder, false)
	return nil
}

func (s *folderSummaryService) Refresh(ctx context.Context, kbID, folderID string) error {
	folder, err := s.folderRepo.GetByID(ctx, folderID)
	if err != nil {
		return err
	}
	if folder.KnowledgeBaseID != kbID {
		return ErrFolderNotFound
	}
	s.ScheduleRefresh(ctx, folder, true)
	return nil
}

// ScheduleRefresh implements debounced refresh: mark pending (coalescing) and
// enqueue a 30s-delayed task. If already pending/processing, skip to merge
// concurrent changes into one task.
func (s *folderSummaryService) ScheduleRefresh(ctx context.Context, folder *types.KnowledgeFolder, forceRefresh bool) {
	if folder == nil || s.task == nil {
		return
	}
	// 防抖：已在 pending/processing 则跳过
	if folder.SummaryStatus == types.FolderSummaryStatusPending ||
		folder.SummaryStatus == types.FolderSummaryStatusProcessing {
		return
	}
	// 人工编辑保护
	if !forceRefresh {
		existing, err := s.summaryRepo.GetByFolder(ctx, folder.ID)
		if err == nil && existing != nil && existing.IsManualEdit {
			logger.Infof(ctx, "folder %s summary is manually edited; skipping debounced refresh", folder.ID)
			return
		}
	}
	// 标记 pending（防抖标记）
	if err := s.folderRepo.UpdateStatus(ctx, folder.ID, types.FolderSummaryStatusPending); err != nil {
		logger.Warnf(ctx, "failed to mark folder %s pending: %v", folder.ID, err)
	}
	if err := s.enqueue(ctx, folder.TenantID, folder.KnowledgeBaseID, folder.ID, forceRefresh); err != nil {
		logger.Warnf(ctx, "failed to enqueue folder summary refresh for %s: %v", folder.ID, err)
	}
}

// ScheduleRefreshForFolderAndAncestors triggers debounced refresh for the folder
// and its entire ancestor chain (parents' "subfolder guide" depends on child themes).
func (s *folderSummaryService) ScheduleRefreshForFolderAndAncestors(ctx context.Context, folder *types.KnowledgeFolder) {
	if folder == nil {
		return
	}
	s.ScheduleRefresh(ctx, folder, false)
	current := folder
	for current.ParentID != "" {
		parent, err := s.folderRepo.GetByID(ctx, current.ParentID)
		if err != nil || parent == nil {
			break
		}
		s.ScheduleRefresh(ctx, parent, false)
		current = parent
	}
}

func (s *folderSummaryService) Edit(ctx context.Context, kbID, folderID string, req *types.FolderSummaryEditRequest) (*types.FolderSummary, error) {
	tenantID := types.MustTenantIDFromContext(ctx)
	now := time.Now()
	summary, err := s.summaryRepo.GetByFolder(ctx, folderID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			summary = &types.FolderSummary{
				ID:              uuid.New().String(),
				TenantID:        tenantID,
				KnowledgeBaseID: kbID,
				FolderID:        folderID,
			}
		} else {
			return nil, err
		}
	}
	if summary.KnowledgeBaseID != kbID {
		return nil, ErrFolderSummaryNotReady
	}
	summary.Content = req.Content
	if req.ContentFormat != "" {
		summary.ContentFormat = req.ContentFormat
	}
	summary.IsManualEdit = true
	summary.SummaryVersion = summary.SummaryVersion + 1
	summary.EditedAt = &now
	summary.GeneratedAt = &now
	if err := s.summaryRepo.Upsert(ctx, summary); err != nil {
		return nil, err
	}
	// 人工编辑后的摘要仍应向量化更新，确保检索时效性（设计 7.9）
	if folder, ferr := s.folderRepo.GetByID(ctx, folderID); ferr == nil {
		if kb, kerr := s.kbService.GetKnowledgeBaseByID(ctx, folder.KnowledgeBaseID); kerr == nil {
			if verr := s.vectorizeFolderSummary(ctx, kb, folder, summary); verr != nil {
				logger.Warnf(ctx, "failed to vectorize manually-edited folder summary for %s: %v", folderID, verr)
			}
		}
	}
	return summary, nil
}

func (s *folderSummaryService) IsStale(ctx context.Context, kbID, folderID string) (bool, error) {
	summary, err := s.summaryRepo.GetByFolder(ctx, folderID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return true, nil
		}
		return false, err
	}
	if summary.IsManualEdit {
		return false, nil
	}
	if summary.GeneratedAt == nil {
		return true, nil
	}
	snapshot := &types.FolderSummaryInputSnapshot{}
	if summary.InputSnapshot != nil {
		_ = json.Unmarshal(summary.InputSnapshot, snapshot)
	}
	// current membership of the folder (direct files)
	ids, err := s.repo.ListKnowledgeIDsByFolderID(ctx, kbID, folderID)
	if err != nil {
		return false, err
	}
	currentCount := len(ids)
	if snapshot.FileCount != currentCount {
		return true, nil
	}
	return false, nil
}

func (s *folderSummaryService) enqueue(ctx context.Context, tenantID uint64, kbID, folderID string, refresh bool) error {
	payload := types.FolderSummaryGenerationPayload{
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		FolderID:        folderID,
		Refresh:         refresh,
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	task := asynq.NewTask(types.TypeFolderSummaryGeneration, bytes, folderSummaryTaskOptions()...)
	if _, err := s.task.Enqueue(task); err != nil {
		return fmt.Errorf("enqueue folder summary task: %w", err)
	}
	logger.Infof(ctx, "[FolderSummary] Enqueued generation for folder %s (kb %s)", folderID, kbID)
	return nil
}

// Handle processes a TypeFolderSummaryGeneration task.
func (s *folderSummaryService) Handle(ctx context.Context, t *asynq.Task) error {
	var payload types.FolderSummaryGenerationPayload
	if err := json.Unmarshal(t.Payload(), &payload); err != nil {
		return nil // no retry on unmarshal error
	}
	ctx = context.WithValue(ctx, types.TenantIDContextKey, payload.TenantID)

	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, payload.KnowledgeBaseID)
	if err != nil {
		logger.Errorf(ctx, "folder summary: kb %s not found: %v", payload.KnowledgeBaseID, err)
		return nil
	}
	if !kb.IsFolderGovernanceEnabled() {
		logger.Infof(ctx, "folder summary: folder governance disabled for kb %s; skipping", payload.KnowledgeBaseID)
		return nil
	}

	folder, err := s.folderRepo.GetByID(ctx, payload.FolderID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // folder deleted; nothing to do
		}
		return err
	}
	if folder.KnowledgeBaseID != payload.KnowledgeBaseID {
		return nil
	}

	// 防抖幂等：仅处理 status==pending 的任务；已被其他任务处理则跳过
	if folder.SummaryStatus != types.FolderSummaryStatusPending {
		logger.Infof(ctx, "folder %s summary status is %s, skipping", folder.ID, folder.SummaryStatus)
		return nil
	}
	// respect manual-edit protection unless an explicit refresh was requested
	if !payload.Refresh {
		existing, err := s.summaryRepo.GetByFolder(ctx, payload.FolderID)
		if err == nil && existing.IsManualEdit {
			logger.Infof(ctx, "folder %s summary is manually edited; skipping", payload.FolderID)
			return nil
		}
	}

	if err := s.folderRepo.UpdateStatus(ctx, folder.ID, types.FolderSummaryStatusProcessing); err != nil {
		logger.Warnf(ctx, "folder summary: failed to set processing status: %v", err)
	}

	content, snapshot, err := s.generateContent(ctx, payload, folder, kb)
	if err != nil {
		s.setFolderStatus(ctx, folder.ID, types.FolderSummaryStatusFailed)
		return err
	}

	now := time.Now()
	summary := &types.FolderSummary{
		ID:              uuid.New().String(),
		TenantID:        payload.TenantID,
		KnowledgeBaseID: payload.KnowledgeBaseID,
		FolderID:        folder.ID,
		Content:         content,
		ContentFormat:   "markdown",
		IsManualEdit:    false,
		SummaryVersion:  0,
		GeneratedAt:     &now,
	}
	snapJSON, err := json.Marshal(snapshot)
	if err == nil {
		summary.InputSnapshot = snapJSON
	}
	if existing, e := s.summaryRepo.GetByFolder(ctx, folder.ID); e == nil {
		summary.SummaryVersion = existing.SummaryVersion + 1
	}
	if err := s.summaryRepo.Upsert(ctx, summary); err != nil {
		s.setFolderStatus(ctx, folder.ID, types.FolderSummaryStatusFailed)
		return err
	}

	// (re)index the folder summary chunk into the vector store
	if err := s.vectorizeFolderSummary(ctx, kb, folder, summary); err != nil {
		logger.Errorf(ctx, "folder summary: vectorization failed for folder %s: %v", folder.ID, err)
	}

	s.setFolderStatus(ctx, folder.ID, types.FolderSummaryStatusCompleted)
	logger.Infof(ctx, "folder summary: generated for folder %s (kb %s)", folder.ID, payload.KnowledgeBaseID)
	return nil
}

// generateContent builds the folder-level summary from its knowledge entries.
func (s *folderSummaryService) generateContent(ctx context.Context, payload types.FolderSummaryGenerationPayload, folder *types.KnowledgeFolder, kb *types.KnowledgeBase) (string, *types.FolderSummaryInputSnapshot, error) {
	// gather direct files of the folder
	files, err := s.repo.ListByFolderIDs(ctx, payload.KnowledgeBaseID, []string{folder.ID})
	if err != nil {
		return "", nil, err
	}
	if len(files) == 0 {
		return "", &types.FolderSummaryInputSnapshot{FileCount: 0}, nil
	}

	// citation counts enrich the prompt with usage signals
	citationCounts, err := s.referenceEventRepo.CountByKnowledge(ctx, payload.TenantID, payload.KnowledgeBaseID, nil)
	if err != nil {
		citationCounts = nil
	}

	// build the document listing (title + summary if present)
	var titles []string
	var body strings.Builder
	for _, f := range files {
		titles = append(titles, f.FileName)
		fmt.Fprintf(&body, "- %s\n", f.FileName)
		if strings.TrimSpace(f.Description) != "" {
			fmt.Fprintf(&body, "  描述: %s\n", f.Description)
		}
		if c, ok := citationCounts[f.ID]; ok && c > 0 {
			fmt.Fprintf(&body, "  引用次数: %d\n", c)
		}
	}

	modelID := kb.SummaryModelID
	if modelID == "" {
		return "", nil, fmt.Errorf("summary model is not configured for kb %s", payload.KnowledgeBaseID)
	}
	chatModel, err := s.modelService.GetChatModel(ctx, modelID)
	if err != nil {
		return "", nil, err
	}

	modelCtx := types.WithLLMCallMetadata(ctx, "folder_summary", "")
	prompt := s.folderSummaryPrompt(modelCtx)
	content, err := chatModel.Chat(modelCtx, []chat.Message{
		{Role: "system", Content: prompt},
		{Role: "user", Content: body.String()},
	}, &chat.ChatOptions{Temperature: 0.3})
	if err != nil {
		return "", nil, err
	}

	snapshot := &types.FolderSummaryInputSnapshot{
		FileCount:      len(files),
		FileTitles:     titles,
		CitationCounts: citationCounts,
		GeneratedAt:    time.Now(),
	}
	return content.Content, snapshot, nil
}

func (s *folderSummaryService) folderSummaryPrompt(ctx context.Context) string {
	lang := types.LanguageNameFromContext(ctx)
	if s.config != nil && s.config.Conversation != nil && strings.TrimSpace(s.config.Conversation.GenerateSummaryPrompt) != "" {
		return types.RenderPromptPlaceholders(s.config.Conversation.GenerateSummaryPrompt, types.PlaceholderValues{
			"language": lang,
		})
	}
	return "你是一名知识库文件夹内容管理员。请根据以下文件夹中的文件列表与摘要，用 " + lang + " 生成一段精炼的结构化概述（Markdown），概括该文件夹覆盖的主题、知识要点与适用场景。不要编造列表中不存在的信息。"
}

func (s *folderSummaryService) setFolderStatus(ctx context.Context, folderID, status string) {
	if err := s.folderRepo.UpdateStatus(ctx, folderID, status); err != nil {
		logger.Warnf(ctx, "folder summary: failed to update status to %s: %v", status, err)
	}
}

// vectorizeFolderSummary (re)indexes the folder_summary chunk into the vector store.
func (s *folderSummaryService) vectorizeFolderSummary(ctx context.Context, kb *types.KnowledgeBase, folder *types.KnowledgeFolder, summary *types.FolderSummary) error {
	if !kb.NeedsEmbeddingModel() || strings.TrimSpace(summary.Content) == "" {
		return nil
	}
	// drop any previous folder summary chunk (no duplicate index rows)
	if err := s.chunkService.DeleteByFolderAndType(ctx, folder.ID, types.ChunkTypeFolderSummary); err != nil {
		return err
	}

	tenantInfo, err := s.tenantRepo.GetTenantByID(ctx, kb.TenantID)
	if err != nil {
		return err
	}
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, tenantInfo)

	retrieveEngine, err := retriever.CreateRetrieveEngineForKB(ctx, s.retrieveEngine, s.ownership, tenantInfo.ID, kb.VectorStoreID)
	if err != nil {
		return err
	}
	embeddingModel, err := s.modelService.GetEmbeddingModel(ctx, kb.EmbeddingModelID)
	if err != nil {
		return err
	}

	chunk := &types.Chunk{
		ID:              uuid.New().String(),
		TenantID:        kb.TenantID,
		KnowledgeBaseID: kb.ID,
		FolderID:        folder.ID,
		Content:         fmt.Sprintf("# 文件夹概述: %s\n%s", folder.Name, summary.Content),
		IsEnabled:       true,
		ChunkType:       types.ChunkTypeFolderSummary,
		CreatedAt:       time.Now(),
		UpdatedAt:       time.Now(),
	}
	if err := s.chunkService.CreateChunks(ctx, []*types.Chunk{chunk}); err != nil {
		return err
	}

	indexInfo := []*types.IndexInfo{{
		Content:         chunk.Content,
		SourceID:        chunk.ID,
		SourceType:      types.ChunkSourceType,
		ChunkID:         chunk.ID,
		KnowledgeBaseID: kb.ID,
		IsEnabled:       true,
	}}
	if err := retrieveEngine.BatchIndex(ctx, embeddingModel, indexInfo); err != nil {
		return err
	}
	return nil
}

// folderSummaryTaskOptions returns the asynq options for folder summary tasks.
// ProcessIn(30s) debounces short bursts of folder mutations into one task.
func folderSummaryTaskOptions() []asynq.Option {
	return []asynq.Option{
		asynq.TaskID(fmt.Sprintf("folder-summary:%s", uuid.New().String())),
		asynq.Queue(types.QueueFolderSummary),
		asynq.MaxRetry(3),
		asynq.Timeout(10 * time.Minute),
		asynq.ProcessIn(30 * time.Second),
	}
}
