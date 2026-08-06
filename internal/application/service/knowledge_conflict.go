package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hibiken/asynq"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// conflictDetectMinChunks is the minimum number of enabled chunks a newly-uploaded
// file must have before conflict detection runs.
const conflictDetectMinChunks = 1

// conflictCoarseTopK is how many semantically-similar existing chunks are
// retrieved per new chunk in the coarse filter stage.
const conflictCoarseTopK = 8

// conflictFineMaxPairs caps how many chunk pairs are sent to the LLM for fine
// adjudication in a single task run, preventing runaway model calls on large files.
const conflictFineMaxPairs = 50

// conflictFineMaxRetries bounds LLM adjudication retries.
const conflictFineMaxRetries = 2

// conflictMaxNewChunks caps how many new chunks participate in detection to
// bound runtime on very large uploads.
const conflictMaxNewChunks = 200

// conflictReasonMaxRunes caps the stored LLM reason length.
const conflictReasonMaxRunes = 2000

// conflictContentMaxRunes caps the stored content snapshot length.
const conflictContentMaxRunes = 6000

// KnowledgeConflictService performs post-upload file-level content conflict
// detection (coarse semantic filter + LLM fine adjudication) and exposes the
// adjudication queue for Owner/Admin resolution (M3).
type KnowledgeConflictService struct {
	conflictRepo interfaces.KnowledgeConflictRepository
	kbService    interfaces.KnowledgeBaseService
	knowledgeSvc interfaces.KnowledgeService
	chunkRepo    interfaces.ChunkRepository
	modelService interfaces.ModelService
	taskEnqueuer interfaces.TaskEnqueuer
}

// NewKnowledgeConflictService constructs the conflict detect + adjudicate service.
func NewKnowledgeConflictService(
	conflictRepo interfaces.KnowledgeConflictRepository,
	kbService interfaces.KnowledgeBaseService,
	knowledgeSvc interfaces.KnowledgeService,
	chunkRepo interfaces.ChunkRepository,
	modelService interfaces.ModelService,
	taskEnqueuer interfaces.TaskEnqueuer,
) *KnowledgeConflictService {
	return &KnowledgeConflictService{
		conflictRepo: conflictRepo,
		kbService:    kbService,
		knowledgeSvc: knowledgeSvc,
		chunkRepo:    chunkRepo,
		modelService: modelService,
		taskEnqueuer: taskEnqueuer,
	}
}

// ---------------------------------------------------------------------------
// ConflictDetectService
// ---------------------------------------------------------------------------

// Enqueue queues a conflict detection task for a freshly-uploaded knowledge.
func (s *KnowledgeConflictService) Enqueue(ctx context.Context, knowledgeID, kbID string, tenantID uint64) error {
	if s.taskEnqueuer == nil {
		return errors.New("conflict detect task enqueuer not configured")
	}
	payload := types.ConflictDetectPayload{
		TenantID:        tenantID,
		KnowledgeID:     knowledgeID,
		KnowledgeBaseID: kbID,
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal conflict detect payload: %w", err)
	}
	task := asynq.NewTask(types.TypeConflictDetect, bytes, conflictDetectTaskOptions()...)
	if _, err := s.taskEnqueuer.Enqueue(task); err != nil {
		return fmt.Errorf("enqueue conflict detect task: %w", err)
	}
	logger.GetLogger(ctx).Infof("[ConflictDetect] Enqueued detection task for knowledge %s (kb %s)", knowledgeID, kbID)
	return nil
}

// Handle implements the asynq handler for TypeConflictDetect.
func (s *KnowledgeConflictService) Handle(ctx context.Context, task *asynq.Task) error {
	var payload types.ConflictDetectPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("unmarshal conflict detect payload: %w", err)
	}
	if payload.KnowledgeID == "" || payload.KnowledgeBaseID == "" {
		return errors.New("conflict detect payload missing knowledge/kb id")
	}
	logger.GetLogger(ctx).Infof("[ConflictDetect] Start detection for knowledge %s in kb %s",
		payload.KnowledgeID, payload.KnowledgeBaseID)

	// 1. Confirm conflict detection is enabled on the KB.
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, payload.KnowledgeBaseID)
	if err != nil {
		return fmt.Errorf("load knowledge base %s: %w", payload.KnowledgeBaseID, err)
	}
	if kb == nil || !kb.IsConflictDetectEnabled() {
		logger.GetLogger(ctx).Infof("[ConflictDetect] Conflict detection disabled for kb %s, skip", payload.KnowledgeBaseID)
		return nil
	}

	// 2. Load the newly-uploaded knowledge's enabled chunks.
	newChunks, err := s.chunkRepo.ListChunksByKnowledgeID(ctx, payload.TenantID, payload.KnowledgeID)
	if err != nil {
		return fmt.Errorf("load chunks for knowledge %s: %w", payload.KnowledgeID, err)
	}
	enabledNew := filterEnabledChunks(newChunks)
	if len(enabledNew) > conflictMaxNewChunks {
		enabledNew = enabledNew[:conflictMaxNewChunks]
	}
	if len(enabledNew) < conflictDetectMinChunks {
		logger.GetLogger(ctx).Infof("[ConflictDetect] Knowledge %s has no enabled text chunks, skip", payload.KnowledgeID)
		return nil
	}

	// This is a background task whose ctx has no request principal, but several
	// downstream services are request-tenant aware (HybridSearch, GetChatModel —
	// both call MustTenantIDFromContext and panic when absent). Derive a single
	// tenant-injected ctx up front and use it for every KB-scoped call.
	detectCtx := withTenantContext(ctx, payload.TenantID)

	// Resolve the new file's title (used as context for LLM adjudication so it
	// can tell whether the two passages describe the same subject).
	newTitle := ""
	if newKb, err := s.knowledgeSvc.GetKnowledgeByID(detectCtx, payload.KnowledgeID); err == nil && newKb != nil {
		newTitle = newKb.Title
	}

	// 3. Coarse filter: semantic search for similar existing chunks in the KB.
	pairs := s.coarseFilterBySearch(detectCtx, kb, payload.TenantID, payload.KnowledgeBaseID, payload.KnowledgeID, newTitle, enabledNew)
	if len(pairs) == 0 {
		logger.GetLogger(ctx).Infof("[ConflictDetect] No coarse candidates for knowledge %s", payload.KnowledgeID)
		return nil
	}

	// 4. De-duplicate against already-pending conflicts for the same pairs.
	pairs = s.dedupePending(detectCtx, pairs)
	if len(pairs) == 0 {
		logger.GetLogger(ctx).Infof("[ConflictDetect] No new conflict pairs for knowledge %s", payload.KnowledgeID)
		return nil
	}

	// 5. Fine adjudication with LLM (best effort). On failure, coarse pairs are
	//    kept as pending fact_contradictions so the user can still adjudicate.
	adjudicated := s.fineAdjudicate(detectCtx, kb, pairs)

	conflicts := make([]*types.KnowledgeConflict, 0, len(adjudicated))
	for _, p := range adjudicated {
		conflicts = append(conflicts, &types.KnowledgeConflict{
			ID:              uuid.New().String(),
			TenantID:        payload.TenantID,
			KnowledgeBaseID: payload.KnowledgeBaseID,
			KnowledgeIDA:    payload.KnowledgeID,
			KnowledgeIDB:    p.ExistingChunk.KnowledgeID,
			ChunkIDA:        p.NewChunk.ID,
			ChunkIDB:        p.ExistingChunk.ID,
			ContentA:        conflictTruncateRunes(p.NewChunk.Content, conflictContentMaxRunes),
			ContentB:        conflictTruncateRunes(p.ExistingChunk.Content, conflictContentMaxRunes),
			ConflictType:    p.ConflictType,
			LLMReason:       conflictTruncateRunes(p.Reason, conflictReasonMaxRunes),
			Status:          types.ConflictStatusPending,
			DetectedBy:      types.ConflictDetectedByUpload,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
	}
	if len(conflicts) == 0 {
		return nil
	}
	if err := s.conflictRepo.BatchCreate(ctx, conflicts); err != nil {
		return fmt.Errorf("persist conflicts: %w", err)
	}
	logger.GetLogger(ctx).Infof("[ConflictDetect] Persisted %d pending conflicts for knowledge %s", len(conflicts), payload.KnowledgeID)
	return nil
}

// conflictPair links a new chunk to an existing chunk candidate with an optional
// LLM adjudication verdict.
type conflictPair struct {
	NewChunk      *types.Chunk
	ExistingChunk *types.Chunk
	NewTitle      string // 新上传文件标题（用于上下文判断主体）
	ExistingTitle string // 候选文件标题（用于上下文判断主体）
	ConflictType  string
	Reason        string
}

// coarseFilterBySearch retrieves semantically-similar existing chunks for each
// new chunk using the KB's hybrid search, excluding the new file's own chunks.
func (s *KnowledgeConflictService) coarseFilterBySearch(
	ctx context.Context,
	kb *types.KnowledgeBase,
	tenantID uint64,
	kbID, newKnowledgeID, newTitle string,
	newChunks []*types.Chunk,
) []conflictPair {
	var pairs []conflictPair
	for _, chunk := range newChunks {
		params := types.SearchParams{
			QueryText:             chunk.Content,
			VectorThreshold:       0.4, // 严格化：提高相似度门槛，减少误召回
			KeywordThreshold:      0.4,
			MatchCount:            conflictCoarseTopK,
			SkipContextEnrichment: true,
		}
		results, err := s.kbService.HybridSearch(ctx, kbID, params)
		if err != nil {
			logger.GetLogger(ctx).Warnf("[ConflictDetect] HybridSearch for chunk %s failed: %v", chunk.ID, err)
			continue
		}
		for _, r := range results {
			if r == nil || r.KnowledgeID == newKnowledgeID || r.ID == "" {
				continue
			}
			pairs = append(pairs, conflictPair{
				NewChunk:      chunk,
				ExistingChunk: toExistingChunk(r),
				NewTitle:      newTitle,
				ExistingTitle: r.KnowledgeTitle,
			})
		}
	}
	return pairs
}

// toExistingChunk adapts a SearchResult to a minimal Chunk snapshot for the
// conflict pair (only fields used during detection are needed).
func toExistingChunk(r *types.SearchResult) *types.Chunk {
	return &types.Chunk{
		ID:          r.ID,
		KnowledgeID: r.KnowledgeID,
		Content:     r.Content,
		IsEnabled:   true,
	}
}

// dedupePending filters out pairs that already have a pending conflict recorded,
// in either orientation.
func (s *KnowledgeConflictService) dedupePending(ctx context.Context, pairs []conflictPair) []conflictPair {
	out := make([]conflictPair, 0, len(pairs))
	for _, p := range pairs {
		if p.NewChunk == nil || p.ExistingChunk == nil {
			continue
		}
		exists, err := s.conflictRepo.HasPendingByChunkPair(ctx, p.NewChunk.ID, p.ExistingChunk.ID)
		if err != nil {
			logger.GetLogger(ctx).Warnf("[ConflictDetect] HasPendingByChunkPair(%s,%s) failed: %v", p.NewChunk.ID, p.ExistingChunk.ID, err)
			continue
		}
		if exists {
			continue
		}
		out = append(out, p)
	}
	return out
}

// fineAdjudicate sends up to conflictFineMaxPairs to the KB chat model to decide
// whether the pair is a real contradiction and what type. Best-effort: on any
// error, pairs are kept with a default fact_contradiction type and no reason.
func (s *KnowledgeConflictService) fineAdjudicate(
	ctx context.Context,
	kb *types.KnowledgeBase,
	pairs []conflictPair,
) []conflictPair {
	if len(pairs) == 0 {
		return nil
	}
	if len(pairs) > conflictFineMaxPairs {
		pairs = pairs[:conflictFineMaxPairs]
	}
	modelID := kb.SummaryModelID
	if modelID == "" {
		// No chat model configured: we cannot adjudicate confidently, so we
		// intentionally report nothing rather than emitting unverified conflicts.
		logger.GetLogger(ctx).Infof("[ConflictDetect] KB %s has no summary model, skip LLM adjudication (report nothing)", kb.ID)
		return nil
	}
	chatModel, err := s.modelService.GetChatModel(ctx, modelID)
	if err != nil {
		logger.GetLogger(ctx).Warnf("[ConflictDetect] GetChatModel %s failed: %v, report nothing", modelID, err)
		return nil
	}

	out := make([]conflictPair, 0, len(pairs))
	for _, p := range pairs {
		verdict, reason, err := s.adjudicatePair(ctx, chatModel, p)
		if err != nil {
			// Strict mode: on adjudication failure we report nothing rather than
			// persisting an unverified conflict. Log it for auditing.
			logger.GetLogger(ctx).Warnf("[ConflictDetect] Adjudicate chunk pair (%s,%s) FAILED, skipped: %v",
				p.NewChunk.ID, p.ExistingChunk.ID, err)
			continue
		}
		if verdict == "" {
			// LLM explicitly judged this as "not a conflict" — do not report.
			continue
		}
		out = append(out, conflictPair{
			NewChunk:      p.NewChunk,
			ExistingChunk: p.ExistingChunk,
			ConflictType:  verdict,
			Reason:        reason,
		})
	}
	return out
}

// adjudicatePair asks the LLM whether chunkA and chunkB contradict each other.
// Returns (conflictType, reason, error). An empty conflictType means "not a real
// conflict" and the caller should skip persisting it.
func (s *KnowledgeConflictService) adjudicatePair(
	ctx context.Context,
	chatModel chat.Chat,
	p conflictPair,
) (string, string, error) {
	prompt := buildConflictAdjudicationPrompt(p.NewChunk.Content, p.ExistingChunk.Content, p.NewTitle, p.ExistingTitle)
	messages := []chat.Message{
		{Role: "system", Content: conflictAdjudicationSystemPrompt},
		{Role: "user", Content: prompt},
	}
	var lastErr error
	for attempt := 0; attempt <= conflictFineMaxRetries; attempt++ {
		resp, err := chatModel.Chat(ctx, messages, &chat.ChatOptions{Temperature: 0.1, MaxTokens: 500})
		if err != nil {
			lastErr = err
			continue
		}
		if resp == nil {
			lastErr = errors.New("empty chat response")
			continue
		}
		verdict, reason := parseConflictVerdict(resp.Content)
		if verdict == "" {
			// Explicitly declared "not a conflict" — skip this pair.
			return "", reason, nil
		}
		return verdict, reason, nil
	}
	return "", "", lastErr
}

const conflictAdjudicationSystemPrompt = `你是知识库一致性审查助手。你会收到两份内容片段，它们来自同一知识库中独立上传的不同文件，并会附带各自所属文件的标题。` +
	`请严格判断：只有它们描述"同一主体 + 同一事实维度"且给出互斥的数值、结论或状态时，才算冲突。` +
	`必须避免误报（false positive），遵守以下规则：` +
	`1. 若两段属于不同主体/不同对象（例如不同银行、不同产品线、不同客户、不同部门、不同时期的不同规定），属于正常差异，conflict 必须为 false。` +
	`2. 若两段仅是措辞不同但含义一致，或内容互补、话题不同，conflict 必须为 false。` +
	`3. 只有非常确定存在矛盾时才报告；模棱两可、信息不足时一律 conflict=false。` +
	`仅返回一个 JSON 对象，字段为：{"conflict": boolean, "type": "fact_contradiction"|"partial_contradiction"|"version_update", "reason": "中文说明"}` +
	`其中 type 仅在 conflict=true 时才有意义：fact_contradiction=对同一事实给出互斥描述；partial_contradiction=大体一致但个别点冲突；version_update=一份是另一份的更新/替代版。` +
	`reason 必须用中文，明确指出矛盾点：片段A说什么、片段B说什么、为何冲突（一句话）。`

// buildConflictAdjudicationPrompt renders the two chunks plus their file titles
// for the LLM.
func buildConflictAdjudicationPrompt(contentA, contentB, titleA, titleB string) string {
	return fmt.Sprintf(`片段 A（新上传文件，标题：「%s」）：
"""
%s
"""

片段 B（知识库中已有文件，标题：「%s」）：
"""
%s
"""

请严格判断它们是否矛盾。若属于不同主体/不同对象的政策差异（例如不同银行），必须判定为不冲突。请按指定 JSON 格式作答，reason 使用中文。`,
		titleA, conflictTruncateRunes(contentA, 3000), titleB, conflictTruncateRunes(contentB, 3000))
}

// parseConflictVerdict extracts the verdict from the LLM JSON reply.
func parseConflictVerdict(reply string) (verdictType, reason string) {
	reply = strings.TrimSpace(reply)
	reply = stripJSONFences(reply)
	var parsed struct {
		Conflict bool   `json:"conflict"`
		Type     string `json:"type"`
		Reason   string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(reply), &parsed); err != nil {
		// Fall back to a loose textual scan of "conflict":true/false.
		falseRe := regexp.MustCompile(`"conflict"\s*:\s*false`)
		trueRe := regexp.MustCompile(`"conflict"\s*:\s*true`)
		if falseRe.MatchString(reply) {
			return "", ""
		}
		if trueRe.MatchString(reply) {
			return types.ConflictTypeFactContradiction, conflictTruncateRunes(reply, conflictReasonMaxRunes)
		}
		return types.ConflictTypeFactContradiction, ""
	}
	if !parsed.Conflict {
		return "", ""
	}
	if parsed.Type == "" {
		parsed.Type = types.ConflictTypeFactContradiction
	}
	return parsed.Type, parsed.Reason
}

func stripJSONFences(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		s = strings.TrimPrefix(s, "```")
		s = strings.TrimPrefix(s, "json")
		s = strings.TrimSpace(s)
		if strings.HasSuffix(s, "```") {
			s = strings.TrimSuffix(s, "```")
		}
	}
	return strings.TrimSpace(s)
}

// ---------------------------------------------------------------------------
// ConflictAdjudicateService
// ---------------------------------------------------------------------------

// ListConflicts returns a page of conflicts for a KB, ordered newest-first.
func (s *KnowledgeConflictService) ListConflicts(
	ctx context.Context,
	tenantID uint64,
	kbID, status string,
	limit, offset int,
) ([]*types.KnowledgeConflict, int64, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	conflicts, err := s.conflictRepo.ListByKB(ctx, tenantID, kbID, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	total, err := s.conflictRepo.CountByKB(ctx, tenantID, kbID, status)
	if err != nil {
		return nil, 0, err
	}
	return conflicts, total, nil
}

// GetConflictStats returns pending/resolved counts for a KB.
func (s *KnowledgeConflictService) GetConflictStats(ctx context.Context, tenantID uint64, kbID string) (map[string]int64, error) {
	pending, err := s.conflictRepo.CountByKB(ctx, tenantID, kbID, types.ConflictStatusPending)
	if err != nil {
		return nil, err
	}
	keepBoth, err := s.conflictRepo.CountByKB(ctx, tenantID, kbID, types.ConflictStatusResolvedKeepBoth)
	if err != nil {
		return nil, err
	}
	newer, err := s.conflictRepo.CountByKB(ctx, tenantID, kbID, types.ConflictStatusResolvedNewer)
	if err != nil {
		return nil, err
	}
	older, err := s.conflictRepo.CountByKB(ctx, tenantID, kbID, types.ConflictStatusResolvedOlder)
	if err != nil {
		return nil, err
	}
	notConflict, err := s.conflictRepo.CountByKB(ctx, tenantID, kbID, types.ConflictStatusResolvedNotConflict)
	if err != nil {
		return nil, err
	}
	return map[string]int64{
		"pending":      pending,
		"keep_both":    keepBoth,
		"newer_wins":   newer,
		"older_wins":   older,
		"not_conflict": notConflict,
	}, nil
}

// Resolve adjudicates a conflict, applying the disable/penalty side-effects.
func (s *KnowledgeConflictService) Resolve(
	ctx context.Context,
	resolverUserID string,
	req types.ConflictResolution,
) (*types.ConflictAdjudicationResult, error) {
	if req.ConflictID == "" {
		return nil, errors.New("conflict_id is required")
	}
	conflict, err := s.conflictRepo.GetByID(ctx, req.ConflictID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("conflict %s not found", req.ConflictID)
		}
		return nil, err
	}
	if !conflict.IsPending() {
		return nil, fmt.Errorf("conflict %s is already resolved (status=%s)", req.ConflictID, conflict.Status)
	}

	var newStatus string
	var disabledChunkIDs []string
	var demotedKnowledgeIDs []string
	var clearPenaltyChunkIDs []string

	switch req.Resolution {
	case types.ConflictStatusResolvedKeepBoth:
		// Keep both sources active. All involved chunks are cleared from penalty.
		newStatus = types.ConflictStatusResolvedKeepBoth
		clearPenaltyChunkIDs = []string{conflict.ChunkIDA, conflict.ChunkIDB}
	case types.ConflictStatusResolvedNewer:
		// Newly-uploaded file (A) is authoritative → disable B's conflicting chunk.
		newStatus = types.ConflictStatusResolvedNewer
		disabledChunkIDs = []string{conflict.ChunkIDB}
		clearPenaltyChunkIDs = []string{conflict.ChunkIDA}
	case types.ConflictStatusResolvedOlder:
		// Pre-existing file (B) is authoritative → disable A's conflicting chunk.
		newStatus = types.ConflictStatusResolvedOlder
		disabledChunkIDs = []string{conflict.ChunkIDA}
		clearPenaltyChunkIDs = []string{conflict.ChunkIDB}
	case types.ConflictStatusResolvedNotConflict:
		// False positive → keep both, clear penalty.
		newStatus = types.ConflictStatusResolvedNotConflict
		clearPenaltyChunkIDs = []string{conflict.ChunkIDA, conflict.ChunkIDB}
	default:
		return nil, fmt.Errorf("unsupported resolution: %s", req.Resolution)
	}

	// Apply chunk disablement via the chunk repository (soft delete from retrieval).
	if err := s.disableChunks(ctx, conflict.TenantID, disabledChunkIDs); err != nil {
		return nil, fmt.Errorf("disable adjudicated chunks: %w", err)
	}

	now := time.Now()
	conflict.Status = newStatus
	conflict.ResolvedBy = resolverUserID
	conflict.ResolvedAt = &now
	conflict.ResolutionNote = req.Note
	conflict.UpdatedAt = now
	if err := s.conflictRepo.Update(ctx, conflict); err != nil {
		return nil, err
	}

	return &types.ConflictAdjudicationResult{
		ConflictID:           req.ConflictID,
		DisabledChunkIDs:     disabledChunkIDs,
		DemotedKnowledgeIDs:  demotedKnowledgeIDs,
		ClearPenaltyChunkIDs: clearPenaltyChunkIDs,
	}, nil
}

// disableChunks soft-disables the given chunks by setting IsEnabled=false and
// IndexStatus to a disabled-like state so they are excluded from retrieval.
func (s *KnowledgeConflictService) disableChunks(ctx context.Context, tenantID uint64, chunkIDs []string) error {
	if len(chunkIDs) == 0 {
		return nil
	}
	chunks, err := s.chunkRepo.ListChunksByID(ctx, tenantID, chunkIDs)
	if err != nil {
		return err
	}
	if len(chunks) == 0 {
		return nil
	}
	for _, c := range chunks {
		if c == nil {
			continue
		}
		c.IsEnabled = false
	}
	return s.chunkRepo.UpdateChunks(ctx, chunks)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func filterEnabledChunks(chunks []*types.Chunk) []*types.Chunk {
	if len(chunks) == 0 {
		return nil
	}
	out := make([]*types.Chunk, 0, len(chunks))
	for _, c := range chunks {
		if c == nil || !c.IsEnabled {
			continue
		}
		if strings.TrimSpace(c.Content) == "" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func conflictTruncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n…[truncated]"
}

func conflictDetectTaskOptions() []asynq.Option {
	return []asynq.Option{
		asynq.TaskID(fmt.Sprintf("conflict:%s", uuid.New().String())),
		asynq.Queue(types.QueueConflict),
		asynq.MaxRetry(3),
		asynq.Timeout(10 * time.Minute),
	}
}

// withTenantContext returns a ctx carrying the given tenant ID (and a minimal
// *Tenant stub) so request-tenant-aware services can run inside a background
// worker. HybridSearch calls MustTenantIDFromContext and panics otherwise.
func withTenantContext(ctx context.Context, tenantID uint64) context.Context {
	ctx = context.WithValue(ctx, types.TenantIDContextKey, tenantID)
	ctx = context.WithValue(ctx, types.TenantInfoContextKey, &types.Tenant{ID: tenantID})
	return ctx
}
