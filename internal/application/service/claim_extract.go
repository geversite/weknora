package service

// claim_extract.go implements the C1 claim extraction service (Conflict V2).
// It turns freshly-ingested document chunks and user-edited wiki pages into
// atomic claims persisted in the claims table, which power claim-key based
// conflict candidate pairing (knowledge_conflict.go) and later corpus sweeps
// (C6). Everything here is best-effort and gated by the per-KB
// IndexingStrategy.ClaimExtractEnabled toggle: failures are logged, retried
// by asynq, and never block ingestion or wiki editing.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/models/chat"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
	"github.com/google/uuid"
	"github.com/hibiken/asynq"
)

const (
	// claimExtractLLMBatchChunks is how many chunks are packed per LLM call.
	claimExtractLLMBatchChunks = 4
	// claimExtractMaxClaimsPerChunk caps per-chunk claim volume (over-limit
	// output is truncated and counted).
	claimExtractMaxClaimsPerChunk = 30
	// claimExtractChunkContentMaxRunes caps the per-chunk content sent to the
	// LLM (aligned with conflictContentMaxRunes experience value).
	claimExtractChunkContentMaxRunes = 6000
	// claimExtractWikiWindowRunes / Overlap chunk overlong wiki pages.
	claimExtractWikiWindowRunes   = 8000
	claimExtractWikiWindowOverlap = 500
	// claimExtractWikiDebounce delays wiki-edit triggered extraction so rapid
	// consecutive saves collapse into one run (asynq TaskID dedup + delay).
	claimExtractWikiDebounce = 2 * time.Minute
	// claimExtractTemperature / MaxTokens bounds.
	claimExtractTemperature  = 0.1
	claimExtractMinMaxTokens = 800
	claimExtractMaxMaxTokens = 4000
)

// claimExtractService implements interfaces.ClaimExtractService.
type claimExtractService struct {
	kbService    interfaces.KnowledgeBaseService
	chunkRepo    interfaces.ChunkRepository
	wikiRepo     interfaces.WikiPageRepository
	claimRepo    interfaces.ClaimRepository
	modelService interfaces.ModelService
	taskEnqueuer interfaces.TaskEnqueuer
}

// NewClaimExtractService creates the claim extraction service (C1).
func NewClaimExtractService(
	kbService interfaces.KnowledgeBaseService,
	chunkRepo interfaces.ChunkRepository,
	wikiRepo interfaces.WikiPageRepository,
	claimRepo interfaces.ClaimRepository,
	modelService interfaces.ModelService,
	taskEnqueuer interfaces.TaskEnqueuer,
) interfaces.ClaimExtractService {
	return &claimExtractService{
		kbService:    kbService,
		chunkRepo:    chunkRepo,
		wikiRepo:     wikiRepo,
		claimRepo:    claimRepo,
		modelService: modelService,
		taskEnqueuer: taskEnqueuer,
	}
}

// ---------------------------------------------------------------------------
// Enqueue helpers
// ---------------------------------------------------------------------------

// EnqueueForKnowledge queues claim extraction for a knowledge file. TaskID
// dedup keeps concurrent re-triggers of the same file from stacking up.
func (s *claimExtractService) EnqueueForKnowledge(
	ctx context.Context, tenantID uint64, kbID, knowledgeID string, reason string,
) error {
	payload := types.ClaimExtractPayload{
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		SourceType:      types.ClaimSourceChunk,
		KnowledgeID:     knowledgeID,
		Reason:          reason,
	}
	return s.enqueue(ctx, payload,
		asynq.TaskID(fmt.Sprintf("claim:knowledge:%s", knowledgeID)))
}

// EnqueueForWikiPage queues debounced claim extraction for one wiki page:
// the TaskID pins one pending task per page and ProcessIn delays execution,
// so edits inside the window collapse — the task reads the latest content
// when it finally runs (design §6.2, no debounce table needed).
func (s *claimExtractService) EnqueueForWikiPage(
	ctx context.Context, tenantID uint64, kbID, pageID string, reason string,
) error {
	payload := types.ClaimExtractPayload{
		TenantID:        tenantID,
		KnowledgeBaseID: kbID,
		SourceType:      types.ClaimSourceWikiPage,
		WikiPageID:      pageID,
		Reason:          reason,
	}
	return s.enqueue(ctx, payload,
		asynq.TaskID(fmt.Sprintf("claim:wiki:%s", pageID)),
		asynq.ProcessIn(claimExtractWikiDebounce))
}

func (s *claimExtractService) enqueue(
	ctx context.Context, payload types.ClaimExtractPayload, extra ...asynq.Option,
) error {
	if s.taskEnqueuer == nil {
		return nil
	}
	bytes, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal claim extract payload: %w", err)
	}
	opts := append([]asynq.Option{
		asynq.Queue(types.QueueConflict),
		asynq.MaxRetry(2),
		asynq.Timeout(10 * time.Minute),
	}, extra...)
	task := asynq.NewTask(types.TypeClaimExtract, bytes, opts...)
	if _, err := s.taskEnqueuer.Enqueue(task); err != nil {
		if errors.Is(err, asynq.ErrTaskIDConflict) || errors.Is(err, asynq.ErrDuplicateTask) {
			// Debounce hit: an extraction for this source is already queued.
			return nil
		}
		return err
	}
	logger.GetLogger(ctx).Infof("[ClaimExtract] Enqueued %s extraction (reason=%s, kb=%s)",
		payload.SourceType, payload.Reason, payload.KnowledgeBaseID)
	return nil
}

// ---------------------------------------------------------------------------
// Task handler
// ---------------------------------------------------------------------------

// Handle is the asynq entry point for claim:extract.
func (s *claimExtractService) Handle(ctx context.Context, task *asynq.Task) error {
	var payload types.ClaimExtractPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("unmarshal claim extract payload: %w", err)
	}
	if payload.KnowledgeBaseID == "" {
		return errors.New("claim extract payload missing kb id")
	}

	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, payload.KnowledgeBaseID)
	if err != nil {
		return fmt.Errorf("load knowledge base %s: %w", payload.KnowledgeBaseID, err)
	}
	if kb == nil || !kb.IsClaimExtractEnabled() {
		logger.GetLogger(ctx).Infof("[ClaimExtract] Disabled for kb %s, skip", payload.KnowledgeBaseID)
		return nil
	}

	// Background task: derive a tenant-injected ctx once (HybridSearch /
	// GetChatModel are request-tenant aware; same pattern as M3 Handle).
	extractCtx := withTenantContext(ctx, payload.TenantID)

	chatModel, err := s.chatModelForKB(extractCtx, kb)
	if err != nil || chatModel == nil {
		// No usable model: report nothing rather than persisting junk.
		logger.GetLogger(ctx).Infof("[ClaimExtract] KB %s has no usable chat model, skip: %v", kb.ID, err)
		return nil
	}

	switch payload.SourceType {
	case types.ClaimSourceChunk:
		if payload.KnowledgeID == "" {
			return errors.New("claim extract payload missing knowledge id")
		}
		return s.extractForKnowledge(extractCtx, kb, chatModel, payload.TenantID, payload.KnowledgeID)
	case types.ClaimSourceWikiPage:
		if payload.WikiPageID == "" {
			return errors.New("claim extract payload missing wiki page id")
		}
		return s.extractForWikiPage(extractCtx, kb, chatModel, payload.TenantID, payload.WikiPageID)
	default:
		return fmt.Errorf("unknown claim source type %q", payload.SourceType)
	}
}

func (s *claimExtractService) chatModelForKB(ctx context.Context, kb *types.KnowledgeBase) (chat.Chat, error) {
	if kb.SummaryModelID == "" {
		return nil, nil
	}
	return s.modelService.GetChatModel(ctx, kb.SummaryModelID)
}

// ---------------------------------------------------------------------------
// Chunk source
// ---------------------------------------------------------------------------

func (s *claimExtractService) extractForKnowledge(
	ctx context.Context, kb *types.KnowledgeBase, chatModel chat.Chat,
	tenantID uint64, knowledgeID string,
) error {
	chunks, err := s.chunkRepo.ListChunksByKnowledgeID(ctx, tenantID, knowledgeID)
	if err != nil {
		return fmt.Errorf("load chunks for knowledge %s: %w", knowledgeID, err)
	}
	enabled := filterEnabledChunks(chunks)
	if len(enabled) == 0 {
		logger.GetLogger(ctx).Infof("[ClaimExtract] Knowledge %s has no enabled text chunks, skip", knowledgeID)
		return nil
	}

	batchID := uuid.New().String()
	var failedBatches int
	for start := 0; start < len(enabled); start += claimExtractLLMBatchChunks {
		end := start + claimExtractLLMBatchChunks
		if end > len(enabled) {
			end = len(enabled)
		}
		batch := enabled[start:end]
		parsed, err := s.extractBatch(ctx, chatModel, batch)
		if err != nil {
			// Partial-failure policy (design §5.4): keep old claims for the
			// chunks of a failed batch; the task error triggers asynq retry.
			failedBatches++
			logger.GetLogger(ctx).Warnf("[ClaimExtract] Batch %d-%d for knowledge %s failed: %v",
				start, end, knowledgeID, err)
			continue
		}
		for idx, chunk := range batch {
			claims := s.buildChunkClaims(ctx, kb, tenantID, knowledgeID, chunk, parsed[idx])
			if err := s.claimRepo.ReplaceBySource(ctx, types.ClaimSourceChunk, chunk.ID, batchID, claims); err != nil {
				logger.GetLogger(ctx).Warnf("[ClaimExtract] Persist claims for chunk %s failed: %v", chunk.ID, err)
			}
		}
	}
	logger.GetLogger(ctx).Infof("[ClaimExtract] Knowledge %s done: %d chunks, %d failed batches",
		knowledgeID, len(enabled), failedBatches)
	if failedBatches > 0 {
		return fmt.Errorf("claim extract: %d/%d batches failed for knowledge %s",
			failedBatches, (len(enabled)+claimExtractLLMBatchChunks-1)/claimExtractLLMBatchChunks, knowledgeID)
	}
	return nil
}

func (s *claimExtractService) buildChunkClaims(
	ctx context.Context, kb *types.KnowledgeBase, tenantID uint64,
	knowledgeID string, chunk *types.Chunk, extracted []extractedClaim,
) []*types.Claim {
	if len(extracted) > claimExtractMaxClaimsPerChunk {
		logger.GetLogger(ctx).Warnf("[ClaimExtract] Chunk %s produced %d claims, truncating to %d",
			chunk.ID, len(extracted), claimExtractMaxClaimsPerChunk)
		extracted = extracted[:claimExtractMaxClaimsPerChunk]
	}
	out := make([]*types.Claim, 0, len(extracted))
	for _, e := range extracted {
		c := s.newClaim(ctx, kb, tenantID, e)
		if c == nil {
			continue
		}
		c.SourceType = types.ClaimSourceChunk
		c.SourceID = chunk.ID
		c.KnowledgeID = knowledgeID
		c.SpanStart, c.SpanEnd = locateQuote(chunk.Content, e.Quote)
		out = append(out, c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Wiki source
// ---------------------------------------------------------------------------

func (s *claimExtractService) extractForWikiPage(
	ctx context.Context, kb *types.KnowledgeBase, chatModel chat.Chat,
	tenantID uint64, pageID string,
) error {
	if s.wikiRepo == nil {
		return nil
	}
	page, err := s.wikiRepo.GetByID(ctx, pageID)
	if err != nil {
		return fmt.Errorf("load wiki page %s: %w", pageID, err)
	}
	if page == nil || page.KnowledgeBaseID != kb.ID {
		logger.GetLogger(ctx).Infof("[ClaimExtract] Wiki page %s not found in kb %s, skip", pageID, kb.ID)
		return nil
	}

	// Loop-breaker gate 2: machine-managed blocks never enter extraction.
	stripped, blocks, mapper := StripMachineManagedBlocks(page.Content)
	for _, b := range blocks {
		if b.Unclosed {
			logger.GetLogger(ctx).Warnf("[ClaimExtract] strip_unclosed_block page=%s start=%d", page.ID, b.Start)
		}
	}
	if strings.TrimSpace(stripped) == "" {
		// Nothing left: clear stale claims for this page.
		return s.claimRepo.DeleteBySource(ctx, types.ClaimSourceWikiPage, page.ID)
	}

	batchID := uuid.New().String()
	windows := splitRuneWindows(stripped, claimExtractWikiWindowRunes, claimExtractWikiWindowOverlap)
	var all []*types.Claim
	var failed int
	for _, w := range windows {
		parsed, err := s.extractSingle(ctx, chatModel, page.Title, w.text)
		if err != nil {
			failed++
			logger.GetLogger(ctx).Warnf("[ClaimExtract] Wiki window for page %s failed: %v", page.ID, err)
			continue
		}
		for _, e := range parsed {
			c := s.newClaim(ctx, kb, tenantID, e)
			if c == nil {
				continue
			}
			c.SourceType = types.ClaimSourceWikiPage
			c.SourceID = page.ID
			// Locate in the window, shift to stripped offsets, then map back
			// to ORIGINAL page offsets (design §5.5 span restoration).
			ws, we := locateQuote(w.text, e.Quote)
			if we > ws {
				if os, oe, ok := mapper.ToOriginal(w.start+ws, w.start+we); ok {
					c.SpanStart, c.SpanEnd = os, oe
				}
			}
			all = append(all, c)
		}
	}
	if failed > 0 {
		// Keep the previous claim set intact when any window failed.
		return fmt.Errorf("claim extract: %d/%d windows failed for wiki page %s", failed, len(windows), page.ID)
	}
	if err := s.claimRepo.ReplaceBySource(ctx, types.ClaimSourceWikiPage, page.ID, batchID, all); err != nil {
		return fmt.Errorf("persist claims for wiki page %s: %w", page.ID, err)
	}
	logger.GetLogger(ctx).Infof("[ClaimExtract] Wiki page %s done: %d claims", page.ID, len(all))
	return nil
}

// ---------------------------------------------------------------------------
// Shared claim construction
// ---------------------------------------------------------------------------

func (s *claimExtractService) newClaim(
	ctx context.Context, kb *types.KnowledgeBase, tenantID uint64, e extractedClaim,
) *types.Claim {
	subject := strings.TrimSpace(e.Subject)
	predicate := strings.TrimSpace(e.Predicate)
	value := strings.TrimSpace(e.Value)
	if subject == "" || predicate == "" || value == "" {
		return nil
	}
	valueNorm, valueKind := NormalizeClaimValue(value, strings.TrimSpace(e.ValueKind))
	var qualifiers types.JSON
	if len(e.Qualifiers) > 0 {
		if b, err := json.Marshal(e.Qualifiers); err == nil {
			qualifiers = types.JSON(b)
		}
	}
	return &types.Claim{
		ID:               uuid.New().String(),
		TenantID:         tenantID,
		KnowledgeBaseID:  kb.ID,
		Subject:          subject,
		Predicate:        predicate,
		Value:            value,
		Qualifiers:       qualifiers,
		ClaimKey:         FusedClaimKey(subject, predicate),
		ValueNorm:        valueNorm,
		ValueKind:        valueKind,
		ExtractorVersion: types.ClaimExtractorVersion,
		CreatedAt:        time.Now(),
	}
}

// ---------------------------------------------------------------------------
// LLM invocation & parsing
// ---------------------------------------------------------------------------

// extractedClaim mirrors one element of the extractor's JSON protocol
// (design §5.3 / appendix A).
type extractedClaim struct {
	ChunkIndex int               `json:"chunk_index"`
	Subject    string            `json:"subject"`
	Predicate  string            `json:"predicate"`
	Value      string            `json:"value"`
	Qualifiers map[string]string `json:"qualifiers"`
	ValueKind  string            `json:"value_kind"`
	Quote      string            `json:"quote"`
}

type extractedClaimsEnvelope struct {
	Claims []extractedClaim `json:"claims"`
}

const claimExtractSystemPrompt = `你是知识库声明抽取器。从给定文本中抽取"原子事实声明"，每条为一个四元组：主体(subject)、属性(predicate)、取值(value)、限定词(qualifiers)。` +
	`规则：` +
	`1. 只抽客观事实断言（数值、日期、状态、归属、定义）；不抽主观评价、操作步骤、示例、假设；` +
	`2. 每条声明必须自含：主体是明确的名词短语，不得是代词或省略；无法确定主体则放弃该条；` +
	`3. value 保留原文表述；value_kind 标注 number/enum/date/text；` +
	`4. qualifiers 尽量填 time(时效)/scope(适用范围)/unit(单位)/condition(前提条件)，没有则省略；` +
	`5. quote 必须是原文逐字连续片段（用于定位），不得改写；` +
	`6. 单段文本最多 30 条，超出时保留信息量最大的；` +
	`7. 仅输出 JSON，格式：{"claims":[{"chunk_index":int,"subject":str,"predicate":str,"value":str,"qualifiers":obj,"value_kind":str,"quote":str}]}`

// extractBatch packs several chunks into one LLM call and returns per-chunk
// claim slices (index-aligned with the input batch).
func (s *claimExtractService) extractBatch(
	ctx context.Context, chatModel chat.Chat, batch []*types.Chunk,
) ([][]extractedClaim, error) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "以下是同一文档的 %d 个片段，chunk_index 从 0 开始：\n", len(batch))
	totalRunes := 0
	for i, c := range batch {
		content := conflictTruncateRunes(c.Content, claimExtractChunkContentMaxRunes)
		totalRunes += len([]rune(content))
		fmt.Fprintf(&sb, "--- 片段 %d ---\n%s\n", i, content)
	}
	parsed, err := s.callExtractor(ctx, chatModel, sb.String(), totalRunes)
	if err != nil {
		return nil, err
	}
	out := make([][]extractedClaim, len(batch))
	for _, e := range parsed {
		if e.ChunkIndex < 0 || e.ChunkIndex >= len(batch) {
			continue
		}
		out[e.ChunkIndex] = append(out[e.ChunkIndex], e)
	}
	return out, nil
}

// extractSingle sends one text (wiki window) as chunk_index 0.
func (s *claimExtractService) extractSingle(
	ctx context.Context, chatModel chat.Chat, title, text string,
) ([]extractedClaim, error) {
	var sb strings.Builder
	sb.WriteString("以下是同一文档的 1 个片段，chunk_index 从 0 开始：\n")
	if title != "" {
		fmt.Fprintf(&sb, "（文档标题：%s）\n", title)
	}
	fmt.Fprintf(&sb, "--- 片段 0 ---\n%s\n", text)
	return s.callExtractor(ctx, chatModel, sb.String(), len([]rune(text)))
}

func (s *claimExtractService) callExtractor(
	ctx context.Context, chatModel chat.Chat, userPrompt string, inputRunes int,
) ([]extractedClaim, error) {
	maxTokens := inputRunes / 3
	if maxTokens < claimExtractMinMaxTokens {
		maxTokens = claimExtractMinMaxTokens
	}
	if maxTokens > claimExtractMaxMaxTokens {
		maxTokens = claimExtractMaxMaxTokens
	}
	messages := []chat.Message{
		{Role: "system", Content: claimExtractSystemPrompt},
		{Role: "user", Content: userPrompt},
	}
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		resp, err := chatModel.Chat(ctx, messages, &chat.ChatOptions{
			Temperature: claimExtractTemperature,
			MaxTokens:   maxTokens,
		})
		if err != nil {
			lastErr = err
			continue
		}
		if resp == nil || strings.TrimSpace(resp.Content) == "" {
			lastErr = errors.New("empty chat response")
			continue
		}
		var envelope extractedClaimsEnvelope
		if err := json.Unmarshal([]byte(stripJSONFences(resp.Content)), &envelope); err != nil {
			lastErr = fmt.Errorf("parse extractor reply: %w", err)
			continue
		}
		return envelope.Claims, nil
	}
	return nil, lastErr
}

// ---------------------------------------------------------------------------
// Quote location & windowing helpers
// ---------------------------------------------------------------------------

// locateQuote finds the verbatim quote in content and returns rune offsets.
// Exact match first, then whitespace-folded match (design §5.3). (0, 0) means
// "location failed" — callers persist the claim anyway and count the miss.
func locateQuote(content, quote string) (int, int) {
	quote = strings.TrimSpace(quote)
	if quote == "" {
		return 0, 0
	}
	if idx := strings.Index(content, quote); idx >= 0 {
		start := len([]rune(content[:idx]))
		return start, start + len([]rune(quote))
	}
	// Whitespace-folded secondary match: fold both sides while tracking the
	// mapping from folded offsets back to original rune offsets.
	contentRunes := []rune(content)
	folded := make([]rune, 0, len(contentRunes))
	foldedToOrig := make([]int, 0, len(contentRunes))
	for i, r := range contentRunes {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		folded = append(folded, r)
		foldedToOrig = append(foldedToOrig, i)
	}
	foldedQuote := strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			return -1
		}
		return r
	}, quote)
	if foldedQuote == "" {
		return 0, 0
	}
	idx := strings.Index(string(folded), foldedQuote)
	if idx < 0 {
		return 0, 0
	}
	startFolded := len([]rune(string(folded)[:idx]))
	endFolded := startFolded + len([]rune(foldedQuote))
	if endFolded > len(foldedToOrig) {
		return 0, 0
	}
	return foldedToOrig[startFolded], foldedToOrig[endFolded-1] + 1
}

// runeWindow is one extraction window over a long text.
type runeWindow struct {
	start int // rune offset of the window in the source text
	text  string
}

// splitRuneWindows slices text into overlapping windows of at most size
// runes. size must be > overlap.
func splitRuneWindows(text string, size, overlap int) []runeWindow {
	runes := []rune(text)
	if len(runes) <= size {
		return []runeWindow{{start: 0, text: text}}
	}
	var out []runeWindow
	step := size - overlap
	for start := 0; start < len(runes); start += step {
		end := start + size
		if end > len(runes) {
			end = len(runes)
		}
		out = append(out, runeWindow{start: start, text: string(runes[start:end])})
		if end == len(runes) {
			break
		}
	}
	return out
}
