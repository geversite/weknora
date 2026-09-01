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
	pendingRepo  interfaces.TaskPendingOpsRepository
	claimRepo       interfaces.ClaimRepository              // C1: claim-key pairing channel (nil-safe)
	wikiRepo        interfaces.WikiPageRepository           // C1: wiki counterpart resolution (nil-safe)
	runRepo         interfaces.ConflictDetectionRunRepository // C2: aggregate experiment metrics (nil-safe)
	clusterService  interfaces.ConflictClusterService       // C4-Lite: deterministic raw-row aggregation (nil-safe)
}

// NewKnowledgeConflictService constructs the conflict detect + adjudicate service.
func NewKnowledgeConflictService(
	conflictRepo interfaces.KnowledgeConflictRepository,
	kbService interfaces.KnowledgeBaseService,
	knowledgeSvc interfaces.KnowledgeService,
	chunkRepo interfaces.ChunkRepository,
	modelService interfaces.ModelService,
	taskEnqueuer interfaces.TaskEnqueuer,
	pendingRepo interfaces.TaskPendingOpsRepository,
	claimRepo interfaces.ClaimRepository,
	wikiRepo interfaces.WikiPageRepository,
	runRepo interfaces.ConflictDetectionRunRepository,
	clusterService interfaces.ConflictClusterService,
) *KnowledgeConflictService {
	return &KnowledgeConflictService{
		conflictRepo: conflictRepo,
		kbService:    kbService,
		knowledgeSvc: knowledgeSvc,
		chunkRepo:    chunkRepo,
		modelService: modelService,
		taskEnqueuer: taskEnqueuer,
		pendingRepo: pendingRepo,
		claimRepo:      claimRepo,
		wikiRepo:       wikiRepo,
		runRepo:        runRepo,
		clusterService: clusterService,
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
func (s *KnowledgeConflictService) Handle(ctx context.Context, task *asynq.Task) (retErr error) {
	var payload types.ConflictDetectPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		return fmt.Errorf("unmarshal conflict detect payload: %w", err)
	}
	if payload.KnowledgeID == "" || payload.KnowledgeBaseID == "" {
		return errors.New("conflict detect payload missing knowledge/kb id")
	}
	startedAt := time.Now()
	run := &types.ConflictDetectionRun{
		ID:              uuid.NewString(),
		TenantID:        payload.TenantID,
		KnowledgeBaseID: payload.KnowledgeBaseID,
		KnowledgeID:     payload.KnowledgeID,
		CascadeMode:     types.ConflictCascadeModeLegacy,
		DetectorVersion: types.ConflictDetectorVersion,
		Status:          types.ConflictDetectionRunStatusCompleted,
		CreatedAt:       startedAt,
	}
	defer s.finishConflictDetectionRun(ctx, run, startedAt, &retErr)

	logger.GetLogger(ctx).Infof("[ConflictDetect] Start detection for knowledge %s in kb %s",
		payload.KnowledgeID, payload.KnowledgeBaseID)

	// 1. Confirm conflict detection is enabled on the KB.
	kb, err := s.kbService.GetKnowledgeBaseByID(ctx, payload.KnowledgeBaseID)
	if err != nil {
		return fmt.Errorf("load knowledge base %s: %w", payload.KnowledgeBaseID, err)
	}
	if kb == nil || !kb.IsConflictDetectEnabled() {
		run.Status = types.ConflictDetectionRunStatusSkipped
		logger.GetLogger(ctx).Infof("[ConflictDetect] Conflict detection disabled for kb %s, skip", payload.KnowledgeBaseID)
		return nil
	}
	run.CascadeMode = kb.EffectiveConflictCascadeMode()

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
		run.Status = types.ConflictDetectionRunStatusSkipped
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

	// 3. Coarse candidate generation, dual channel (C1):
	//    main channel — exact claim-key pairing over the claims index;
	//    fallback     — semantic HybridSearch for every chunk that still has
	//                    an unmatched claim (or no usable claim). This is
	//                    essential for subject synonyms / boundary drift such
	//                    as "报销申请" vs "报销单".
	pairs, claimCoveredChunkIDs := s.coarseFilterByClaims(
		detectCtx, payload.TenantID, payload.KnowledgeBaseID, payload.KnowledgeID, newTitle, enabledNew)
	fallbackChunks := make([]*types.Chunk, 0, len(enabledNew))
	for _, c := range enabledNew {
		if !claimCoveredChunkIDs[c.ID] {
			fallbackChunks = append(fallbackChunks, c)
		}
	}
	if len(fallbackChunks) > 0 {
		pairs = append(pairs, s.coarseFilterBySearch(
			detectCtx, kb, payload.TenantID, payload.KnowledgeBaseID, payload.KnowledgeID, newTitle, fallbackChunks)...)
	}
	// A chunk can legitimately enter both channels when it contains one exact
	// key hit and another unmatched fact. Collapse same chunk pairs before LLM
	// adjudication so this improves recall without multiplying cost.
	pairs = dedupeConflictCandidatePairs(pairs)
	claimPairCount, fallbackPairCount := conflictCandidateChannelCounts(pairs)
	run.CandidateClaimPairs = claimPairCount
	run.CandidateFallbackPairs = fallbackPairCount
	run.CandidateAfterDedupe = len(pairs)
	logger.GetLogger(ctx).Infof(
		"[ConflictDetect] Coarse candidates for knowledge %s: %d pairs (claim-pairs=%d, fallback-pairs=%d, claim-covered chunks=%d, fallback chunks=%d)",
		payload.KnowledgeID, len(pairs), claimPairCount, fallbackPairCount, len(claimCoveredChunkIDs), len(fallbackChunks))
	if len(pairs) == 0 {
		run.Status = types.ConflictDetectionRunStatusSkipped
		logger.GetLogger(ctx).Infof("[ConflictDetect] No coarse candidates for knowledge %s", payload.KnowledgeID)
		return nil
	}

	// 4. De-duplicate against already-pending conflicts for the same pairs.
	pairs = s.dedupePending(detectCtx, pairs)
	run.CandidatesSubmitted = len(pairs)
	if len(pairs) == 0 {
		run.Status = types.ConflictDetectionRunStatusSkipped
		logger.GetLogger(ctx).Infof("[ConflictDetect] No new conflict pairs for knowledge %s", payload.KnowledgeID)
		return nil
	}

	// 5. C1 legacy or C2 cascade fine verification. The cascade stats are
	// persisted even if no final conflict is produced, because no-conflict rule
	// decisions and LLM cost are first-class research observations.
	adjudicated, cascadeStats := s.fineAdjudicate(detectCtx, kb, pairs)
	run.RuleNoConflict = cascadeStats.RuleNoConflict
	run.RuleDirectConflict = cascadeStats.RuleDirectConflict
	run.RuleNeedsLLM = cascadeStats.RuleNeedsLLM
	run.LLMPairCount = cascadeStats.LLMPairCount
	run.LLMBatchCallCount = cascadeStats.LLMBatchCallCount
	run.LLMSingleCallCount = cascadeStats.LLMSingleCallCount
	run.LLMSingleFallbackCount = cascadeStats.LLMSingleFallbackCount
	run.LLMPromptTokens = cascadeStats.LLMPromptTokens
	run.LLMCompletionTokens = cascadeStats.LLMCompletionTokens

	// C4-Lite anchors every final raw conflict before persistence. C2-B may
	// already have hydrated fallback claim hints for its batch prompt; C1/C2-A
	// fallbacks are hydrated here solely to derive a conservative fact anchor.
	adjudicated = s.hydrateFallbackClaimEvidence(detectCtx, adjudicated)
	adjudicated = s.hydrateFallbackFactAnchorHints(detectCtx, payload.TenantID, adjudicated)
	versionMetadata := newConflictVersionMetadataResolver(detectCtx, s.knowledgeSvc)

	conflicts := make([]*types.KnowledgeConflict, 0, len(adjudicated))
	for _, p := range adjudicated {
		anchor := conflictFactAnchorForPair(p)
		metaA := versionMetadata.metadataFor(p.NewChunk.KnowledgeID, p.NewTitle, p.NewChunk.Content)
		metaB := versionMetadata.metadataFor(p.ExistingChunk.KnowledgeID, p.ExistingTitle, p.ExistingChunk.Content)
		suggestion := types.ConflictVersionSuggestion{}
		if conflictAnchorSupportsVersionSuggestion(anchor) {
			suggestion = suggestConflictVersionResolution(metaA, metaB)
		}
		reason := p.Reason
		if p.ExistWikiSlug != "" {
			// C1 transitional wiki marker: the counterpart is a wiki page
			// (KnowledgeIDB stays empty; ChunkIDB carries the page ID). The
			// C4 migration replaces this with first-class wiki columns.
			reason = "[wiki:" + p.ExistWikiSlug + "] " + reason
		}
		if suggestion.Resolution != "" {
			logger.GetLogger(ctx).Infof(
				"[ConflictVersion] Suggested %s for fact_key=%q (confidence=%.2f)",
				suggestion.Resolution, anchor.FactKey, suggestion.Confidence,
			)
		}
		conflicts = append(conflicts, &types.KnowledgeConflict{
			ID:              uuid.New().String(),
			TenantID:        payload.TenantID,
			KnowledgeBaseID: payload.KnowledgeBaseID,
			KnowledgeIDA:    payload.KnowledgeID,
			KnowledgeIDB:    p.ExistingChunk.KnowledgeID,
			ChunkIDA:        p.NewChunk.ID,
			ChunkIDB:        p.ExistingChunk.ID,
			FactKey:         anchor.FactKey,
			FactAnchorKind:  anchor.AnchorKind,
			ClaimKey:        anchor.ClaimKey,
			FactSubject:          anchor.Subject,
			FactPredicate:        anchor.Predicate,
			FactValueA:           anchor.ValueA,
			FactValueB:           anchor.ValueB,
			DocMetaA:              conflictDocumentMetaJSON(metaA),
			DocMetaB:              conflictDocumentMetaJSON(metaB),
			SuggestedResolution:   suggestion.Resolution,
			SuggestionReason:      conflictTruncateRunes(suggestion.Reason, conflictReasonMaxRunes),
			SuggestionConfidence:  suggestion.Confidence,
			SuggestionVersion:     types.ConflictVersionSuggestionVersion,
			AutoResolved:          false,
			ContentA:              conflictTruncateRunes(p.NewChunk.Content, conflictContentMaxRunes),
			ContentB:        conflictTruncateRunes(p.ExistingChunk.Content, conflictContentMaxRunes),
			ConflictType:    p.ConflictType,
			LLMReason:       conflictTruncateRunes(reason, conflictReasonMaxRunes),
			Status:          types.ConflictStatusPending,
			DetectedBy:      types.ConflictDetectedByUpload,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		})
	}
	run.FinalConflictCount = len(conflicts)
	if len(conflicts) == 0 {
		return nil
	}
	if err := s.conflictRepo.BatchCreate(ctx, conflicts); err != nil {
		return fmt.Errorf("persist conflicts: %w", err)
	}
	// C4-Lite aggregation is best-effort with respect to the proven detector:
	// a transient cluster write must not discard valid raw conflicts or trigger
	// a duplicate conflict-detect retry. The explicit rebuild endpoint/script
	// can deterministically converge the aggregate afterward.
	if s.clusterService != nil {
		if result, err := s.clusterService.Rebuild(detectCtx, payload.TenantID, payload.KnowledgeBaseID); err != nil {
			logger.GetLogger(ctx).Warnf("[ConflictCluster] Rebuild after detection for KB %s failed: %v", payload.KnowledgeBaseID, err)
		} else {
			logger.GetLogger(ctx).Infof(
				"[ConflictCluster] Rebuilt KB %s: raw_conflicts=%d disputed_facts=%d assigned=%d",
				payload.KnowledgeBaseID, result.RawConflictCount, result.DisputedFactCount, result.AssignedConflictCount,
			)
		}
	}
	logger.GetLogger(ctx).Infof("[ConflictDetect] Persisted %d pending conflicts for knowledge %s", len(conflicts), payload.KnowledgeID)
	return nil
}

// finishConflictDetectionRun writes C1/C2 aggregate observability best-effort.
// Metric persistence must never alter task success semantics: a metrics-table
// outage should not discard a valid conflict result or trigger duplicate work.
func (s *KnowledgeConflictService) finishConflictDetectionRun(
	ctx context.Context,
	run *types.ConflictDetectionRun,
	startedAt time.Time,
	retErr *error,
) {
	if run == nil {
		return
	}
	now := time.Now()
	run.DurationMs = now.Sub(startedAt).Milliseconds()
	run.FinishedAt = now
	if retErr != nil && *retErr != nil {
		run.Status = types.ConflictDetectionRunStatusFailed
		run.ErrorMessage = conflictTruncateRunes((*retErr).Error(), 2000)
	}
	if s.runRepo == nil {
		return
	}
	if err := s.runRepo.Create(ctx, run); err != nil {
		logger.GetLogger(ctx).WithField("error", err).Warnf(
			"[ConflictCascade] persist detection metrics failed for knowledge %s", run.KnowledgeID,
		)
	}
}

// claimEvidence is the compact, claim-level evidence passed to the fine LLM
// only for exact claim-key candidates. It keeps adjudication anchored to the
// factual slot that caused pairing instead of asking the model to rediscover
// the relevant assertion from two potentially large chunks.
type claimEvidence struct {
	ID        string
	ClaimKey  string
	Subject   string
	Predicate string
	Value     string
	ValueNorm string
	ValueKind string
	Qualifiers string
}

func claimEvidenceFromClaim(claim *types.Claim) claimEvidence {
	if claim == nil {
		return claimEvidence{}
	}
	qualifiers := strings.TrimSpace(claim.Qualifiers.ToString())
	if qualifiers == "" {
		qualifiers = "{}"
	}
	return claimEvidence{
		ID:         claim.ID,
		ClaimKey:   claim.ClaimKey,
		Subject:    claim.Subject,
		Predicate:  claim.Predicate,
		Value:      claim.Value,
		ValueNorm:  claim.ValueNorm,
		ValueKind:  claim.ValueKind,
		Qualifiers: qualifiers,
	}
}

func appendClaimEvidenceUnique(items []claimEvidence, item claimEvidence) []claimEvidence {
	if item.ID == "" && item.Subject == "" && item.Predicate == "" && item.Value == "" {
		return items
	}
	for _, existing := range items {
		if item.ID != "" && existing.ID == item.ID {
			return items
		}
		if item.ID == "" && existing.Subject == item.Subject && existing.Predicate == item.Predicate && existing.Value == item.Value {
			return items
		}
	}
	return append(items, item)
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
	// C1 claim-channel provenance. When ClaimKeyHit is non-empty, the evidence
	// is an exact claim-key pairing anchor. For C2-B fallback pairs the same
	// evidence fields may carry non-decisive, fuzzy-slot hints solely for the
	// batch prompt; deterministic rules always require ClaimKeyHit and cannot
	// treat those hints as an exact match.
	ClaimKeyHit        string
	NewClaimIDs        []string
	ExistClaimIDs      []string
	NewClaimEvidence   []claimEvidence
	ExistClaimEvidence []claimEvidence
	// FallbackFactAnchorHints are C4-only, post-verdict hints. They may come
	// from an unambiguous document-level claim set when raw candidate chunks
	// are synthetic summary/child chunks with no directly attached claim.
	FallbackFactAnchorHints []conflictFallbackClaimHint
	FallbackFactAnchorKind  string
	// ExistWikiSlug is set when the counterpart is a wiki page (C1: pseudo
	// chunk, no disable side-effects; formalized by the C4 migration).
	ExistWikiSlug string
}

// coarseFilterByClaims is the C1 main candidate channel: pair new-file claims
// with same-claim-key claims elsewhere in the KB (documents AND wiki pages).
// It returns exact-key pairs plus chunks fully covered by that channel. A chunk
// is covered only when EVERY usable claim in it found a live exact-key
// counterpart. Chunks with no claims, an unmatched key, or a disabled/missing
// counterpart deliberately remain uncovered and enter HybridSearch fallback.
// This preserves recall for subject synonyms and predicate boundary drift.
func (s *KnowledgeConflictService) coarseFilterByClaims(
	ctx context.Context,
	tenantID uint64,
	kbID, newKnowledgeID, newTitle string,
	newChunks []*types.Chunk,
) ([]conflictPair, map[string]bool) {
	covered := make(map[string]bool)
	if s.claimRepo == nil {
		return nil, covered
	}
	newClaims, err := s.claimRepo.ListByKnowledge(ctx, tenantID, newKnowledgeID)
	if err != nil {
		logger.GetLogger(ctx).Warnf("[ConflictDetect] ListByKnowledge claims for %s failed: %v", newKnowledgeID, err)
		return nil, covered
	}
	if len(newClaims) == 0 {
		return nil, covered
	}

	chunkByID := make(map[string]*types.Chunk, len(newChunks))
	for _, c := range newChunks {
		chunkByID[c.ID] = c
	}
	// Group claims by normalized key and by source chunk. Claims whose source
	// is not part of this detection pass are intentionally ignored.
	newByKey := make(map[string][]*types.Claim)
	claimsByChunk := make(map[string][]*types.Claim)
	for _, c := range newClaims {
		if c == nil || c.ClaimKey == "" {
			continue
		}
		if _, ok := chunkByID[c.SourceID]; !ok {
			continue
		}
		newByKey[c.ClaimKey] = append(newByKey[c.ClaimKey], c)
		claimsByChunk[c.SourceID] = append(claimsByChunk[c.SourceID], c)
	}
	if len(newByKey) == 0 {
		return nil, covered
	}
	keys := make([]string, 0, len(newByKey))
	for k := range newByKey {
		keys = append(keys, k)
	}

	existing, err := s.claimRepo.ListByKeys(ctx, tenantID, kbID, keys, "", newKnowledgeID)
	if err != nil {
		logger.GetLogger(ctx).Warnf("[ConflictDetect] ListByKeys claims for %s failed: %v", newKnowledgeID, err)
		return nil, covered
	}
	if len(existing) == 0 {
		return nil, covered
	}

	// Merge hits into at most one pair per (new chunk, counterpart source):
	// multiple key hits between the same pair of sources enrich the claim ID
	// lists instead of spawning duplicate LLM adjudications.
	type pairKey struct{ newChunkID, existSourceID string }
	merged := make(map[pairKey]*conflictPair)
	matchedKeys := make(map[string]bool)
	titleCache := make(map[string]string) // knowledgeID -> title
	var order []pairKey

	for _, ex := range existing {
		if ex == nil || ex.ClaimKey == "" {
			continue
		}
		newSide := newByKey[ex.ClaimKey]
		if len(newSide) == 0 {
			continue
		}
		counterpart, existTitle, wikiSlug := s.resolveClaimCounterpart(ctx, tenantID, ex, titleCache)
		if counterpart == nil {
			continue
		}
		for _, nc := range newSide {
			newChunk := chunkByID[nc.SourceID]
			if newChunk == nil || counterpart.ID == newChunk.ID {
				continue
			}
			matchedKeys[ex.ClaimKey] = true
			pk := pairKey{newChunkID: newChunk.ID, existSourceID: counterpart.ID}
			if p, ok := merged[pk]; ok {
				p.NewClaimIDs = appendUnique(p.NewClaimIDs, nc.ID)
				p.ExistClaimIDs = appendUnique(p.ExistClaimIDs, ex.ID)
				p.NewClaimEvidence = appendClaimEvidenceUnique(p.NewClaimEvidence, claimEvidenceFromClaim(nc))
				p.ExistClaimEvidence = appendClaimEvidenceUnique(p.ExistClaimEvidence, claimEvidenceFromClaim(ex))
				continue
			}
			merged[pk] = &conflictPair{
				NewChunk:           newChunk,
				ExistingChunk:      counterpart,
				NewTitle:           newTitle,
				ExistingTitle:      existTitle,
				ClaimKeyHit:        ex.ClaimKey,
				NewClaimIDs:        []string{nc.ID},
				ExistClaimIDs:      []string{ex.ID},
				NewClaimEvidence:   []claimEvidence{claimEvidenceFromClaim(nc)},
				ExistClaimEvidence: []claimEvidence{claimEvidenceFromClaim(ex)},
				ExistWikiSlug:      wikiSlug,
			}
			order = append(order, pk)
		}
	}

	pairs := make([]conflictPair, 0, len(order))
	for _, pk := range order {
		pairs = append(pairs, *merged[pk])
	}
	return pairs, claimChannelCoveredChunks(claimsByChunk, matchedKeys)
}

// claimChannelCoveredChunks returns only chunks whose every usable claim has a
// valid exact-key counterpart. Returning partial chunks as uncovered is
// intentional: HybridSearch sees the whole chunk and can recover a second
// fact expressed with synonyms even when the chunk also contains one exact hit.
func claimChannelCoveredChunks(
	claimsByChunk map[string][]*types.Claim, matchedKeys map[string]bool,
) map[string]bool {
	covered := make(map[string]bool)
	for chunkID, claims := range claimsByChunk {
		if chunkID == "" || len(claims) == 0 {
			continue
		}
		allMatched := true
		for _, claim := range claims {
			if claim == nil || claim.ClaimKey == "" || !matchedKeys[claim.ClaimKey] {
				allMatched = false
				break
			}
		}
		if allMatched {
			covered[chunkID] = true
		}
	}
	return covered
}

// resolveClaimCounterpart loads the counterpart source of an existing claim:
// a real chunk for chunk sources, or a pseudo chunk carved from the claim's
// span neighbourhood for wiki pages (C1 transitional representation — the
// pseudo chunk's KnowledgeID stays empty so no disable side-effects apply).
func (s *KnowledgeConflictService) resolveClaimCounterpart(
	ctx context.Context, tenantID uint64, ex *types.Claim, titleCache map[string]string,
) (chunk *types.Chunk, title string, wikiSlug string) {
	switch ex.SourceType {
	case types.ClaimSourceChunk:
		c, err := s.chunkRepo.GetChunkByID(ctx, tenantID, ex.SourceID)
		if err != nil || c == nil || !c.IsEnabled {
			return nil, "", ""
		}
		t, ok := titleCache[c.KnowledgeID]
		if !ok {
			if k, err := s.knowledgeSvc.GetKnowledgeByID(ctx, c.KnowledgeID); err == nil && k != nil {
				t = k.Title
			}
			titleCache[c.KnowledgeID] = t
		}
		return c, t, ""
	case types.ClaimSourceWikiPage:
		if s.wikiRepo == nil {
			return nil, "", ""
		}
		page, err := s.wikiRepo.GetByID(ctx, ex.SourceID)
		if err != nil || page == nil {
			return nil, "", ""
		}
		return &types.Chunk{
			ID:              page.ID,
			TenantID:        page.TenantID,
			KnowledgeID:     "", // empty on purpose: no chunk-disable side effects
			KnowledgeBaseID: page.KnowledgeBaseID,
			Content:         claimSpanNeighborhood(page.Content, ex.SpanStart, ex.SpanEnd, 500),
			IsEnabled:       true,
		}, page.Title, page.Slug
	default:
		return nil, "", ""
	}
}

// claimSpanNeighborhood cuts the ±radius rune neighbourhood around a span;
// a zero span (location failed) falls back to the content head.
func claimSpanNeighborhood(content string, start, end, radius int) string {
	runes := []rune(content)
	if len(runes) == 0 {
		return ""
	}
	if end <= start { // unlocated span
		start, end = 0, 0
	}
	from := start - radius
	if from < 0 {
		from = 0
	}
	to := end + radius
	if to > len(runes) {
		to = len(runes)
	}
	if to <= from {
		to = min(len(runes), from+2*radius)
	}
	return string(runes[from:to])
}

// appendUnique is defined in wiki_ingest.go (types.StringArray); []string
// arguments assign transparently because StringArray's underlying type is
// []string.

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

// conflictCandidateChannelCounts is intentionally small and log-oriented: C1.5
// experiments need to distinguish exact claim-key candidates from semantic
// fallback candidates without inferring their source from final conflict rows.
func conflictCandidateChannelCounts(pairs []conflictPair) (claimPairs, fallbackPairs int) {
	for _, pair := range pairs {
		if pair.ClaimKeyHit == "" {
			fallbackPairs++
		} else {
			claimPairs++
		}
	}
	return claimPairs, fallbackPairs
}

func conflictPairChannel(pair conflictPair) string {
	if pair.ClaimKeyHit == "" {
		return "fallback"
	}
	return "claim_key"
}

// dedupeConflictCandidatePairs removes in-memory duplicates created when a
// partially covered chunk enters both claim-key and semantic fallback paths.
// It prefers claim provenance when either candidate has it, while preserving
// the fallback candidate's source/title only when no exact-key pair exists.
func dedupeConflictCandidatePairs(pairs []conflictPair) []conflictPair {
	type pairKey struct{ newChunkID, existChunkID string }
	out := make([]conflictPair, 0, len(pairs))
	seen := make(map[pairKey]int, len(pairs))
	for _, pair := range pairs {
		if pair.NewChunk == nil || pair.ExistingChunk == nil ||
			pair.NewChunk.ID == "" || pair.ExistingChunk.ID == "" {
			continue
		}
		key := pairKey{newChunkID: pair.NewChunk.ID, existChunkID: pair.ExistingChunk.ID}
		if idx, ok := seen[key]; ok {
			existing := &out[idx]
			if existing.ClaimKeyHit == "" && pair.ClaimKeyHit != "" {
				existing.ClaimKeyHit = pair.ClaimKeyHit
				existing.NewClaimIDs = append(existing.NewClaimIDs, pair.NewClaimIDs...)
				existing.ExistClaimIDs = append(existing.ExistClaimIDs, pair.ExistClaimIDs...)
				for _, evidence := range pair.NewClaimEvidence {
					existing.NewClaimEvidence = appendClaimEvidenceUnique(existing.NewClaimEvidence, evidence)
				}
				for _, evidence := range pair.ExistClaimEvidence {
					existing.ExistClaimEvidence = appendClaimEvidenceUnique(existing.ExistClaimEvidence, evidence)
				}
			}
			continue
		}
		seen[key] = len(out)
		out = append(out, pair)
	}
	return out
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

// fineAdjudicate dispatches C1 legacy or C2 cascade verification according to
// the KB-scoped experiment mode. Rules are deliberately evaluated before model
// lookup: a high-confidence numeric contradiction remains reportable even when
// the chat provider is temporarily unavailable.
func (s *KnowledgeConflictService) fineAdjudicate(
	ctx context.Context,
	kb *types.KnowledgeBase,
	pairs []conflictPair,
) ([]conflictPair, conflictCascadeExecutionStats) {
	var stats conflictCascadeExecutionStats
	if len(pairs) == 0 {
		return nil, stats
	}
	if kb == nil || kb.EffectiveConflictCascadeMode() == types.ConflictCascadeModeLegacy {
		return s.fineAdjudicateSingle(ctx, kb, pairs, &stats, false, true), stats
	}

	direct, unresolved, ruleStats := applyConflictRules(pairs)
	stats.addRuleStats(ruleStats)
	logger.GetLogger(ctx).Infof(
		"[ConflictCascade] mode=%s candidates=%d rule_no_conflict=%d rule_direct_conflict=%d rule_needs_llm=%d",
		kb.EffectiveConflictCascadeMode(), len(pairs), ruleStats.NoConflict, ruleStats.DirectConflict, ruleStats.NeedsLLM,
	)
	if len(unresolved) == 0 {
		return direct, stats
	}

	var adjudicated []conflictPair
	if kb.EffectiveConflictCascadeMode() == types.ConflictCascadeModeRulesBatch {
		// Exact claim-key evidence is already attached by C1. For semantic
		// fallback pairs, add only non-decisive fuzzy-slot hints so the batch
		// model can recognize schema drift such as "测试时间" vs
		// "计划开始时间" without weakening the deterministic rule gate.
		unresolved = s.hydrateFallbackClaimEvidence(ctx, unresolved)
		adjudicated = s.fineAdjudicateBatch(ctx, kb, unresolved, &stats)
	} else {
		// C2-A routes remaining pairs through C1's evidence-conditioned
		// per-pair adjudicator; C2-B uses batch calls above.
		adjudicated = s.fineAdjudicateSingle(ctx, kb, unresolved, &stats, false, true)
	}
	return append(direct, adjudicated...), stats
}

// fineAdjudicateSingle is C1's evidence-conditioned per-pair LLM verifier.
// It remains the legacy implementation and C2-A fallback for semantic gray
// areas (dates/version updates, textual relations, fallback candidates, etc.).
func (s *KnowledgeConflictService) fineAdjudicateSingle(
	ctx context.Context,
	kb *types.KnowledgeBase,
	pairs []conflictPair,
	stats *conflictCascadeExecutionStats,
	singleFallback bool,
	countLogicalPairs bool,
) []conflictPair {
	if kb == nil || len(pairs) == 0 {
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
	if s.modelService == nil {
		logger.GetLogger(ctx).Warnf("[ConflictDetect] ModelService is nil, report nothing")
		return nil
	}
	chatModel, err := s.modelService.GetChatModel(ctx, modelID)
	if err != nil || chatModel == nil {
		logger.GetLogger(ctx).Warnf("[ConflictDetect] GetChatModel %s failed: %v, report nothing", modelID, err)
		return nil
	}
	if stats != nil && countLogicalPairs {
		stats.LLMPairCount += len(pairs)
	}

	out := make([]conflictPair, 0, len(pairs))
	for _, p := range pairs {
		verdict, reason, err := s.adjudicatePair(ctx, chatModel, p, stats, singleFallback)
		if err != nil {
			// Strict mode: on adjudication failure we report nothing rather than
			// persisting an unverified conflict. Log it for auditing.
			logger.GetLogger(ctx).Warnf("[ConflictDetect] Adjudicate chunk pair (%s,%s) FAILED, skipped: %v",
				p.NewChunk.ID, p.ExistingChunk.ID, err)
			continue
		}
		if verdict == "" {
			// LLM explicitly judged this as "not a conflict" — do not report.
			logger.GetLogger(ctx).Infof(
				"[ConflictDetect] Fine verdict new_knowledge=%s existing_knowledge=%s channel=%s claim_key=%q verdict=not_conflict",
				p.NewChunk.KnowledgeID, p.ExistingChunk.KnowledgeID, conflictPairChannel(p), p.ClaimKeyHit,
			)
			continue
		}
		logger.GetLogger(ctx).Infof(
			"[ConflictDetect] Fine verdict new_knowledge=%s existing_knowledge=%s channel=%s claim_key=%q verdict=%s",
			p.NewChunk.KnowledgeID, p.ExistingChunk.KnowledgeID, conflictPairChannel(p), p.ClaimKeyHit, verdict,
		)
		out = append(out, conflictPairWithVerdict(p, verdict, reason))
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
	stats *conflictCascadeExecutionStats,
	singleFallback bool,
) (string, string, error) {
	prompt := buildConflictAdjudicationPrompt(p)
	messages := []chat.Message{
		{Role: "system", Content: conflictAdjudicationSystemPrompt},
		{Role: "user", Content: prompt},
	}
	var lastErr error
	for attempt := 0; attempt <= conflictFineMaxRetries; attempt++ {
		resp, err := chatModel.Chat(ctx, messages, &chat.ChatOptions{Temperature: 0.1, MaxTokens: 500})
		if stats != nil {
			stats.addLLMResponse(resp, false, singleFallback)
		}
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
	`1. 若两段属于不同主体/不同对象（例如不同银行、不同产品线、不同客户、不同部门），属于正常差异，conflict 必须为 false。` +
	`2. 若两段仅是措辞不同但含义一致，或内容互补、话题不同，conflict 必须为 false。` +
	`3. 当输入包含"候选声明证据"时，它是本次候选配对的直接事实锚点：若两侧为同一声明槽位、限定词不明显互斥且 value/value_norm 不同，不能仅因片段中还有其他上下文、主谓措辞不同或存在时间/版本词就判 false。应在 fact_contradiction 与 version_update 之间选择。` +
	`4. 若候选声明中的较晚日期、修订、推迟、替代等明确表明新旧版本关系，判 version_update；若没有明确替代关系但同槽位取值互斥，判 fact_contradiction。只有候选证据本身显示适用范围明确不相交时才判 false。` +
	`5. 只有非常确定不存在同一事实关系时才返回 conflict=false；模糊时优先根据候选声明证据作出有类型的判断。` +
	`仅返回一个 JSON 对象，字段为：{"conflict": boolean, "type": "fact_contradiction"|"partial_contradiction"|"version_update", "reason": "中文说明"}` +
	`其中 type 仅在 conflict=true 时才有意义：fact_contradiction=对同一事实给出互斥描述；partial_contradiction=大体一致但个别点冲突；version_update=一份是另一份的更新/替代版。` +
	`reason 必须用中文，明确指出矛盾点：片段A说什么、片段B说什么、为何冲突（一句话）。`

// buildConflictAdjudicationPrompt renders claim-level evidence when available,
// followed by the source chunks needed to validate that evidence. Fallback
// candidates intentionally have no evidence section and retain the original
// conservative chunk-only behaviour.
func buildConflictAdjudicationPrompt(pair conflictPair) string {
	evidence := renderClaimEvidence(pair)
	return fmt.Sprintf(`%s
片段 A（新上传文件，标题：「%s」）：
"""
%s
"""

片段 B（知识库中已有文件，标题：「%s」）：
"""
%s
"""

请严格判断它们是否矛盾。若包含候选声明证据，请优先围绕该证据的同一事实槽位判断；若没有候选声明证据，则按片段整体语义保守判断。请按指定 JSON 格式作答，reason 使用中文。`,
		evidence,
		pair.NewTitle, conflictTruncateRunes(pair.NewChunk.Content, 3000),
		pair.ExistingTitle, conflictTruncateRunes(pair.ExistingChunk.Content, 3000))
}

func renderClaimEvidence(pair conflictPair) string {
	if pair.ClaimKeyHit == "" || len(pair.NewClaimEvidence) == 0 || len(pair.ExistClaimEvidence) == 0 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "候选声明证据（本次配对的直接事实锚点，声明键：%q）：\n", pair.ClaimKeyHit)
	b.WriteString("新上传文件的声明：\n")
	for _, evidence := range pair.NewClaimEvidence {
		fmt.Fprintf(&b, "- subject=%q; predicate=%q; value=%q; value_norm=%q; qualifiers=%s\n",
			evidence.Subject, evidence.Predicate, evidence.Value, evidence.ValueNorm, evidence.Qualifiers)
	}
	b.WriteString("已有文件的声明：\n")
	for _, evidence := range pair.ExistClaimEvidence {
		fmt.Fprintf(&b, "- subject=%q; predicate=%q; value=%q; value_norm=%q; qualifiers=%s\n",
			evidence.Subject, evidence.Predicate, evidence.Value, evidence.ValueNorm, evidence.Qualifiers)
	}
	b.WriteString("请先判断上述同一声明槽位的取值是否互斥或构成版本更新，再用下方原文片段核验。\n\n")
	return b.String()
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

	// [C1] Wiki counterpart guard: when the B side is a wiki page (pseudo
	// chunk, KnowledgeIDB empty), ChunkIDB is a wiki page ID — never a
	// disable target. Narrative integration is C7's job.
	if conflict.KnowledgeIDB == "" {
		filtered := disabledChunkIDs[:0]
		for _, id := range disabledChunkIDs {
			if id != conflict.ChunkIDB {
				filtered = append(filtered, id)
			}
		}
		disabledChunkIDs = filtered
	}

	// Apply chunk disablement via the chunk repository (soft delete from retrieval).
	if err := s.disableChunks(ctx, conflict.TenantID, disabledChunkIDs); err != nil {
		return nil, fmt.Errorf("disable adjudicated chunks: %w", err)
	}

	// When chunks are disabled by adjudication (newer_wins / older_wins), the
	// wiki pages that were previously generated from those chunks are now
	// stale — they still contain the adjudicated-as-wrong content. Trigger a
	// best-effort wiki re-ingest for the affected knowledge(s) so wiki syncs
	// to the adjudication outcome. ListEnabledChunksByKnowledgeID (used by
	// wiki ingest) will automatically exclude the freshly-disabled chunks.
	//
	// keep_both / not_conflict disable nothing, so there is nothing to sync.
	if len(disabledChunkIDs) > 0 {
		s.triggerWikiReingestForDisabledChunks(ctx, conflict, disabledChunkIDs)
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
	// Keep C4-Lite aggregate status/counts convergent after a raw member is
	// adjudicated. A rebuild failure must not undo an already-applied legacy
	// resolution side effect; the explicit rebuild endpoint remains available.
	if s.clusterService != nil {
		if _, err := s.clusterService.Rebuild(ctx, conflict.TenantID, conflict.KnowledgeBaseID); err != nil {
			logger.GetLogger(ctx).Warnf("[ConflictCluster] Rebuild after resolving conflict %s failed: %v", conflict.ID, err)
		}
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

// triggerWikiReingestForDisabledChunks enqueues a best-effort wiki re-ingest
// for each knowledge that owns a disabled chunk. The wiki ingest pipeline uses
// ListEnabledChunksByKnowledgeID, so the re-generated wiki pages will exclude
// the adjudicated-as-wrong content.
//
// This is best-effort: failures are logged but never returned to the caller,
// since the adjudication itself (chunk disable + conflict status update) has
// already succeeded by the time we get here.
func (s *KnowledgeConflictService) triggerWikiReingestForDisabledChunks(
	ctx context.Context,
	conflict *types.KnowledgeConflict,
	disabledChunkIDs []string,
) {
	if s.pendingRepo == nil || s.taskEnqueuer == nil {
		return
	}

	// Map each disabled chunk ID to the knowledge it belongs to. The conflict
	// record tells us ChunkIDA→KnowledgeIDA and ChunkIDB→KnowledgeIDB, so we
	// can resolve without an extra DB round-trip.
	chunkToKnowledge := map[string]string{
		conflict.ChunkIDA: conflict.KnowledgeIDA,
		conflict.ChunkIDB: conflict.KnowledgeIDB,
	}
	affectedKnowledgeIDs := make(map[string]struct{})
	for _, chunkID := range disabledChunkIDs {
		if kid, ok := chunkToKnowledge[chunkID]; ok && kid != "" {
			affectedKnowledgeIDs[kid] = struct{}{}
		}
	}

	for knowledgeID := range affectedKnowledgeIDs {
		accepted, err := EnqueueWikiIngest(
			ctx, s.taskEnqueuer, s.pendingRepo,
			conflict.TenantID, conflict.KnowledgeBaseID, knowledgeID,
		)
		if err != nil {
			logger.GetLogger(ctx).Warnf(
				"[ConflictResolve] Failed to enqueue wiki re-ingest for knowledge %s (kb %s): %v",
				knowledgeID, conflict.KnowledgeBaseID, err)
			continue
		}
		if !accepted {
			logger.GetLogger(ctx).Infof(
				"[ConflictResolve] Wiki re-ingest skipped (KB %s may be deleted) for knowledge %s",
				conflict.KnowledgeBaseID, knowledgeID)
			continue
		}
		logger.GetLogger(ctx).Infof(
			"[ConflictResolve] Enqueued wiki re-ingest for knowledge %s (kb %s) after disabling chunk(s) %v",
			knowledgeID, conflict.KnowledgeBaseID, disabledChunkIDs)
	}
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
