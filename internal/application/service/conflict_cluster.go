package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// conflictClusterService implements C4-Lite's deterministic raw conflict →
// DisputedFact projection. C4.5 has a no-disable resolver and C4.7 has one
// explicit global-winner adoption path; neither writes wiki pages.
type conflictClusterService struct {
	conflictRepo interfaces.KnowledgeConflictRepository
	factRepo     interfaces.DisputedFactRepository

	// Rebuild may be invoked by concurrent conflict:detect tasks and by the
	// explicit research endpoint. Serialize the read-group-upsert-assign cycle
	// in this process so orphan pruning cannot race another local rebuild.
	mu sync.Mutex
}

func NewConflictClusterService(
	conflictRepo interfaces.KnowledgeConflictRepository,
	factRepo interfaces.DisputedFactRepository,
) interfaces.ConflictClusterService {
	return &conflictClusterService{conflictRepo: conflictRepo, factRepo: factRepo}
}

func (s *conflictClusterService) ListDisputedFacts(
	ctx context.Context, tenantID uint64, kbID, status string, limit, offset int,
) ([]*types.DisputedFact, int64, error) {
	if s == nil || s.factRepo == nil {
		return nil, 0, errors.New("disputed fact repository not configured")
	}
	if kbID == "" {
		return nil, 0, errors.New("knowledge base id is required")
	}
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	if offset < 0 {
		offset = 0
	}
	facts, err := s.factRepo.ListByKB(ctx, tenantID, kbID, status, limit, offset)
	if err != nil {
		return nil, 0, err
	}
	count, err := s.factRepo.CountByKB(ctx, tenantID, kbID, status)
	if err != nil {
		return nil, 0, err
	}
	return facts, count, nil
}

// ResolveDisputedFact propagates one research-safe resolution to every pending
// raw member of a cluster. Only no-disable resolutions are intentionally
// supported here: a cluster can contain member pairs with opposite A/B
// directions. C4.7 therefore exposes global-winner propagation through a
// separate explicit-proposal path, never as a generic newer/older resolution.
func (s *conflictClusterService) ResolveDisputedFact(
	ctx context.Context,
	tenantID uint64,
	resolverUserID string,
	kbID string,
	req types.DisputedFactResolution,
) (*types.DisputedFactAdjudicationResult, error) {
	if s == nil || s.conflictRepo == nil || s.factRepo == nil {
		return nil, errors.New("conflict cluster repositories not configured")
	}
	if tenantID == 0 || kbID == "" || req.DisputedFactID == "" {
		return nil, errors.New("tenant id, knowledge base id and disputed_fact_id are required")
	}
	if !isSafeDisputedFactResolution(req.Resolution) {
		return nil, fmt.Errorf(
			"unsupported cluster resolution %q: only %s and %s are safe before C3",
			req.Resolution, types.ConflictStatusResolvedKeepBoth, types.ConflictStatusResolvedNotConflict,
		)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	fact, err := s.factRepo.GetByID(ctx, tenantID, kbID, req.DisputedFactID)
	if err != nil {
		return nil, fmt.Errorf("load disputed fact %s: %w", req.DisputedFactID, err)
	}
	if fact == nil {
		return nil, fmt.Errorf("disputed fact %s not found", req.DisputedFactID)
	}
	members, err := s.conflictRepo.ResolvePendingByClusterID(
		ctx, tenantID, kbID, fact.ID, req.Resolution, resolverUserID, req.Note,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve disputed fact %s members: %w", fact.ID, err)
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("disputed fact %s has no pending member conflicts", fact.ID)
	}

	rebuild, err := s.rebuildLocked(ctx, tenantID, kbID)
	if err != nil {
		return nil, fmt.Errorf("resolved %d raw conflicts but rebuild failed: %w", len(members), err)
	}
	result := &types.DisputedFactAdjudicationResult{
		DisputedFactID:      fact.ID,
		Resolution:           req.Resolution,
		UpdatedConflictIDs:   make([]string, 0, len(members)),
		ClearPenaltyChunkIDs: make([]string, 0, 2*len(members)),
		Rebuild:              rebuild,
	}
	clearPenalty := make(map[string]struct{})
	for _, member := range members {
		if member == nil {
			continue
		}
		result.UpdatedConflictIDs = append(result.UpdatedConflictIDs, member.ID)
		for _, chunkID := range []string{member.ChunkIDA, member.ChunkIDB} {
			if chunkID != "" {
				clearPenalty[chunkID] = struct{}{}
			}
		}
	}
	sort.Strings(result.UpdatedConflictIDs)
	result.ClearPenaltyChunkIDs = []string(sortedStringSet(clearPenalty))
	result.UpdatedConflictCount = len(result.UpdatedConflictIDs)
	return result, nil
}

// AdoptDisputedFactWinner is C4.7's only path from an advisory C4.6 proposal
// to side effects. The caller must echo the reviewed winner, proposal version,
// source count, and aggregate updated_at value. The repository re-checks all
// of them inside its transaction and rejects stale/reopened/partial clusters.
//
// On success every member receives direction-free resolved_global_winner, and
// only chunks owned by sources other than the one global winner are disabled.
// This intentionally differs from applying raw resolved_newer_wins or
// resolved_older_wins pair by pair: a member can contain two loser sources and
// neither side is allowed to redefine the cluster winner.
func (s *conflictClusterService) AdoptDisputedFactWinner(
	ctx context.Context,
	tenantID uint64,
	resolverUserID string,
	kbID string,
	req types.DisputedFactWinnerAdoption,
) (*types.DisputedFactWinnerAdoptionResult, error) {
	if s == nil || s.conflictRepo == nil || s.factRepo == nil {
		return nil, errors.New("conflict cluster repositories not configured")
	}
	req.DisputedFactID = strings.TrimSpace(req.DisputedFactID)
	req.ExpectedWinnerKnowledgeID = strings.TrimSpace(req.ExpectedWinnerKnowledgeID)
	req.ExpectedProposalVersion = strings.TrimSpace(req.ExpectedProposalVersion)
	req.Note = strings.TrimSpace(req.Note)
	if tenantID == 0 || kbID == "" || req.DisputedFactID == "" || req.ExpectedWinnerKnowledgeID == "" ||
		req.ExpectedProposalVersion == "" || req.ExpectedProposalUpdatedAt.IsZero() ||
		req.ExpectedProposalSourceCount < 2 {
		return nil, errors.New(
			"tenant id, knowledge base id, disputed_fact_id, expected winner, proposal version, updated_at and source count are required",
		)
	}
	if len([]rune(req.Note)) > 2000 {
		return nil, errors.New("winner adoption note exceeds 2000 runes")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.conflictRepo.AdoptPendingWinnerProposal(ctx, tenantID, kbID, resolverUserID, req)
	if err != nil {
		return nil, fmt.Errorf("adopt disputed fact %s winner proposal: %w", req.DisputedFactID, err)
	}
	if result == nil || result.UpdatedConflictCount == 0 {
		return nil, errors.New("winner adoption returned no updated raw conflicts")
	}

	// The repository transaction has already set the fact to resolved with zero
	// pending members. Rebuild remains the canonical projection convergence step
	// and refreshes aggregate fields / current proposal after this explicit action.
	rebuild, err := s.rebuildLocked(ctx, tenantID, kbID)
	if err != nil {
		return nil, fmt.Errorf(
			"adopted global winner for %d raw conflicts but cluster rebuild failed: %w",
			result.UpdatedConflictCount, err,
		)
	}
	result.Rebuild = rebuild
	return result, nil
}

// ReopenDisputedFactWinner is C4.8's explicit reversal of one currently
// active C4.7 adoption. It accepts neither a replacement winner nor an
// inferred raw-pair direction: the durable adoption ID and current aggregate
// updated_at snapshot must both match under the repository transaction.
func (s *conflictClusterService) ReopenDisputedFactWinner(
	ctx context.Context,
	tenantID uint64,
	resolverUserID string,
	kbID string,
	req types.DisputedFactWinnerRevocation,
) (*types.DisputedFactWinnerRevocationResult, error) {
	if s == nil || s.conflictRepo == nil || s.factRepo == nil {
		return nil, errors.New("conflict cluster repositories not configured")
	}
	req.DisputedFactID = strings.TrimSpace(req.DisputedFactID)
	req.WinnerAdoptionID = strings.TrimSpace(req.WinnerAdoptionID)
	req.Note = strings.TrimSpace(req.Note)
	if tenantID == 0 || kbID == "" || req.DisputedFactID == "" || req.WinnerAdoptionID == "" ||
		req.ExpectedDisputedFactUpdatedAt.IsZero() {
		return nil, errors.New(
			"tenant id, knowledge base id, disputed_fact_id, winner_adoption_id and disputed fact updated_at are required",
		)
	}
	if len([]rune(req.Note)) > 2000 {
		return nil, errors.New("winner reopen note exceeds 2000 runes")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	result, err := s.conflictRepo.ReopenWinnerAdoption(ctx, tenantID, kbID, resolverUserID, req)
	if err != nil {
		return nil, fmt.Errorf("reopen disputed fact %s winner adoption: %w", req.DisputedFactID, err)
	}
	if result == nil || result.ReopenedConflictCount == 0 {
		return nil, errors.New("winner reopen returned no reopened raw conflicts")
	}

	rebuild, err := s.rebuildLocked(ctx, tenantID, kbID)
	if err != nil {
		return nil, fmt.Errorf(
			"reopened global winner adoption for %d raw conflicts but cluster rebuild failed: %w",
			result.ReopenedConflictCount, err,
		)
	}
	result.Rebuild = rebuild
	return result, nil
}

func isSafeDisputedFactResolution(resolution string) bool {
	return resolution == types.ConflictStatusResolvedKeepBoth ||
		resolution == types.ConflictStatusResolvedNotConflict
}

// Rebuild deterministically groups every current conflict row in one KB by
// FactKey and writes each member's ClusterID. It is idempotent: repeated calls
// converge on the unique (tenant, kb, fact_key) DisputedFact row. It also
// backfills pre-C4 rows whose FactKey is empty with a conservative chunk-pair
// anchor, intentionally avoiding unsafe cross-chunk merges for legacy data.
func (s *conflictClusterService) Rebuild(
	ctx context.Context, tenantID uint64, kbID string,
) (*types.DisputedFactRebuildResult, error) {
	if s == nil || s.conflictRepo == nil || s.factRepo == nil {
		return nil, errors.New("conflict cluster repositories not configured")
	}
	if tenantID == 0 || kbID == "" {
		return nil, errors.New("tenant id and knowledge base id are required")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rebuildLocked(ctx, tenantID, kbID)
}

// rebuildLocked is Rebuild's implementation. Callers must hold s.mu.
func (s *conflictClusterService) rebuildLocked(
	ctx context.Context, tenantID uint64, kbID string,
) (*types.DisputedFactRebuildResult, error) {
	conflicts, err := s.conflictRepo.ListByKBForClustering(ctx, tenantID, kbID)
	if err != nil {
		return nil, err
	}
	result := &types.DisputedFactRebuildResult{
		KnowledgeBaseID:  kbID,
		ClustererVersion: types.ConflictClustererVersion,
		AnchorKinds:      make(map[string]int),
	}
	groups := make(map[string][]*types.KnowledgeConflict)
	for _, conflict := range conflicts {
		if conflict == nil {
			continue
		}
		result.RawConflictCount++
		changed := ensurePersistedConflictAnchor(conflict)
		if conflict.FactKey == "" {
			return nil, errors.New("conflict fact key could not be derived")
		}
		// Persist a legacy/backfilled anchor before the aggregate is upserted;
		// this makes a later standalone rebuild stable as well.
		if changed {
			if err := s.conflictRepo.Update(ctx, conflict); err != nil {
				return nil, err
			}
		}
		groups[conflict.FactKey] = append(groups[conflict.FactKey], conflict)
	}

	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		members := groups[key]
		fact := aggregateDisputedFact(tenantID, kbID, key, members)
		stored, err := s.factRepo.UpsertByFactKey(ctx, fact)
		if err != nil {
			return nil, err
		}
		result.DisputedFactCount++
		result.AnchorKinds[stored.AnchorKind]++
		if stored.SuggestedWinnerKnowledgeID != "" {
			result.WinnerProposalCount++
		}
		for _, conflict := range members {
			result.AssignedConflictCount++
			if conflict.FactAnchorKind == types.ConflictFactAnchorChunkPair {
				result.UnanchoredConflictCount++
			}
			if conflict.ClusterID == stored.ID {
				continue
			}
			conflict.ClusterID = stored.ID
			if err := s.conflictRepo.Update(ctx, conflict); err != nil {
				return nil, err
			}
		}
	}
	// A manual rebuild after source/conflict deletion converges the aggregate
	// view as well: rows no longer backed by any raw conflict are removed.
	if err := s.factRepo.DeleteExceptFactKeys(ctx, tenantID, kbID, keys); err != nil {
		return nil, err
	}
	return result, nil
}

// hydrateFallbackFactAnchorHints is deliberately post-verdict and C4-only.
// Raw search candidates can be synthetic child/summary chunks that carry no
// direct claim row. C4 first tries source-level fuzzy hints, then a uniquely
// best document-level pairing, and finally an explicit document_singleton
// anchor when each document has one usable claim. It never changes candidate
// generation, deterministic rules, or an LLM verdict.
func (s *KnowledgeConflictService) hydrateFallbackFactAnchorHints(
	ctx context.Context,
	tenantID uint64,
	pairs []conflictPair,
) []conflictPair {
	if s == nil || s.claimRepo == nil || tenantID == 0 || len(pairs) == 0 {
		return pairs
	}

	cache := make(map[string][]claimEvidence)
	loaded := make(map[string]bool)
	loadKnowledgeClaims := func(knowledgeID string) []claimEvidence {
		knowledgeID = strings.TrimSpace(knowledgeID)
		if knowledgeID == "" {
			return nil
		}
		if loaded[knowledgeID] {
			return cache[knowledgeID]
		}
		loaded[knowledgeID] = true
		claims, err := s.claimRepo.ListByKnowledge(ctx, tenantID, knowledgeID)
		if err != nil {
			logger.GetLogger(ctx).Warnf(
				"[ConflictCluster] ListByKnowledge fallback anchor hints for knowledge %s failed: %v",
				knowledgeID, err,
			)
			return nil
		}
		evidence := make([]claimEvidence, 0, len(claims))
		for _, claim := range claims {
			item := claimEvidenceFromClaim(claim)
			if item.ClaimKey != "" {
				evidence = append(evidence, item)
			}
		}
		cache[knowledgeID] = evidence
		return evidence
	}

	for index := range pairs {
		pair := &pairs[index]
		if pair.ClaimKeyHit != "" || len(pair.FallbackFactAnchorHints) > 0 ||
			pair.FallbackFactAnchorKind != "" || pair.NewChunk == nil || pair.ExistingChunk == nil {
			continue
		}
		if hints := selectFallbackClaimHints(pair.NewClaimEvidence, pair.ExistClaimEvidence); len(hints) > 0 {
			pair.FallbackFactAnchorHints = hints
			pair.FallbackFactAnchorKind = types.ConflictFactAnchorFuzzySlot
			continue
		}

		newClaims := loadKnowledgeClaims(pair.NewChunk.KnowledgeID)
		existingClaims := loadKnowledgeClaims(pair.ExistingChunk.KnowledgeID)
		// If both documents have exactly one usable claim, a final LLM conflict
		// has an unambiguous document-level fact identity even when the two
		// extractor labels have no lexical overlap. This is C4-only and retains
		// a distinct anchor kind rather than pretending the keys matched.
		if len(newClaims) == 1 && len(existingClaims) == 1 && fallbackAnchorValuesDiffer(newClaims[0], existingClaims[0]) {
			pair.FallbackFactAnchorHints = []conflictFallbackClaimHint{{
				Newer: newClaims[0], Older: existingClaims[0],
				Similarity: fallbackClaimSlotSimilarity(newClaims[0], existingClaims[0]),
			}}
			pair.FallbackFactAnchorKind = types.ConflictFactAnchorDocumentSingleton
			continue
		}

		// A document may contain contextual claims (for example a policy's
		// applicability scope) in addition to the conflicting rule. Promote
		// document-level evidence only when there is one unambiguous, high
		// similarity slot pair with different values. Otherwise leave the row
		// at a conservative chunk_pair anchor.
		if hints := selectUnambiguousDocumentFallbackHint(newClaims, existingClaims); len(hints) > 0 {
			pair.FallbackFactAnchorHints = hints
			pair.FallbackFactAnchorKind = types.ConflictFactAnchorFuzzySlot
		}
	}
	return pairs
}

const conflictDocumentFallbackHintMinMargin = 0.10

// selectUnambiguousDocumentFallbackHint is the post-verdict C4 fallback for
// source chunks without claims. Unlike selectFallbackClaimHints (which may
// return two prompt hints), it accepts exactly one top document-level pairing
// only when no competing pairing is within the ambiguity margin.
func selectUnambiguousDocumentFallbackHint(
	newer, older []claimEvidence,
) []conflictFallbackClaimHint {
	all := make([]conflictFallbackClaimHint, 0)
	seen := make(map[string]bool)
	for _, newClaim := range newer {
		for _, oldClaim := range older {
			similarity := fallbackClaimSlotSimilarity(newClaim, oldClaim)
			if similarity < conflictBatchFallbackHintMinSimilarity {
				continue
			}
			key := fallbackClaimHintIdentity(newClaim) + "|" + fallbackClaimHintIdentity(oldClaim)
			if seen[key] {
				continue
			}
			seen[key] = true
			all = append(all, conflictFallbackClaimHint{
				Newer: newClaim, Older: oldClaim, Similarity: similarity,
			})
		}
	}
	if len(all) == 0 {
		return nil
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].Similarity != all[j].Similarity {
			return all[i].Similarity > all[j].Similarity
		}
		left := fallbackClaimHintIdentity(all[i].Newer) + "|" + fallbackClaimHintIdentity(all[i].Older)
		right := fallbackClaimHintIdentity(all[j].Newer) + "|" + fallbackClaimHintIdentity(all[j].Older)
		return left < right
	})
	best := all[0]
	if !fallbackAnchorValuesDiffer(best.Newer, best.Older) {
		return nil
	}
	for _, competitor := range all[1:] {
		if competitor.Similarity >= best.Similarity-conflictDocumentFallbackHintMinMargin {
			return nil
		}
	}
	return []conflictFallbackClaimHint{best}
}

func fallbackAnchorValuesDiffer(left, right claimEvidence) bool {
	leftValue := strings.TrimSpace(left.ValueNorm)
	rightValue := strings.TrimSpace(right.ValueNorm)
	if leftValue != "" && rightValue != "" {
		return leftValue != rightValue
	}
	return normalizeBatchEvidenceText(left.Value) != normalizeBatchEvidenceText(right.Value)
}

type conflictFactAnchor struct {
	FactKey    string
	AnchorKind string
	ClaimKey   string
	Subject    string
	Predicate  string
	ValueA     string
	ValueB     string
}

// conflictFactAnchorForPair runs before a raw conflict row is persisted. Exact
// claim-key evidence is preferred; otherwise C2-B4's non-decisive fuzzy slot
// hints offer a stable fallback anchor. A pair with no usable claims is kept
// singleton-by-chunk-pair rather than risk merging different facts.
func conflictFactAnchorForPair(pair conflictPair) conflictFactAnchor {
	claimKey := strings.TrimSpace(pair.ClaimKeyHit)
	if claimKey != "" {
		newEvidence := claimEvidenceForKey(pair.NewClaimEvidence, claimKey)
		existingEvidence := claimEvidenceForKey(pair.ExistClaimEvidence, claimKey)
		return conflictFactAnchor{
			FactKey:    stableConflictFactKey(types.ConflictFactAnchorClaimKey, claimKey),
			AnchorKind: types.ConflictFactAnchorClaimKey,
			ClaimKey:   claimKey,
			Subject:    firstNonEmptyClaimField(newEvidence, existingEvidence, func(item claimEvidence) string { return item.Subject }),
			Predicate:  firstNonEmptyClaimField(newEvidence, existingEvidence, func(item claimEvidence) string { return item.Predicate }),
			ValueA:     firstNonEmptyClaimField(newEvidence, existingEvidence, func(item claimEvidence) string { return item.Value }),
			ValueB:     firstNonEmptyClaimField(existingEvidence, newEvidence, func(item claimEvidence) string { return item.Value }),
		}
	}

	hints := pair.FallbackFactAnchorHints
	anchorKind := pair.FallbackFactAnchorKind
	if len(hints) == 0 {
		hints = selectFallbackClaimHints(pair.NewClaimEvidence, pair.ExistClaimEvidence)
	}
	if anchorKind == "" {
		anchorKind = types.ConflictFactAnchorFuzzySlot
	}
	if len(hints) > 0 {
		hint := hints[0]
		// A candidate may enter HybridSearch fallback because another claim in
		// its chunk was unmatched, even though this final conflict's selected
		// hint has an exact equal ClaimKey on both sides. Canonicalize it back
		// to the exact cluster key so claim_key:<k> and
		// fuzzy_slot:<k>|<k> never split one logical fact.
		if sharedKey := sharedExactClaimKey(hint.Newer, hint.Older); sharedKey != "" {
			return conflictFactAnchor{
				FactKey:    stableConflictFactKey(types.ConflictFactAnchorClaimKey, sharedKey),
				AnchorKind: types.ConflictFactAnchorClaimKey,
				ClaimKey:   sharedKey,
				Subject:    firstNonEmpty(hint.Newer.Subject, hint.Older.Subject),
				Predicate:  firstNonEmpty(hint.Newer.Predicate, hint.Older.Predicate),
				ValueA:     hint.Newer.Value,
				ValueB:     hint.Older.Value,
			}
		}
		leftKey := fallbackClaimSlotKey(hint.Newer)
		rightKey := fallbackClaimSlotKey(hint.Older)
		keys := []string{leftKey, rightKey}
		sort.Strings(keys)
		return conflictFactAnchor{
			FactKey:    stableConflictFactKey(anchorKind, keys...),
			AnchorKind: anchorKind,
			Subject:    firstNonEmpty(hint.Newer.Subject, hint.Older.Subject),
			Predicate:  firstNonEmpty(hint.Newer.Predicate, hint.Older.Predicate),
			ValueA:     hint.Newer.Value,
			ValueB:     hint.Older.Value,
		}
	}

	return conservativeChunkPairAnchor(pair.NewChunk, pair.ExistingChunk)
}

func ensurePersistedConflictAnchor(conflict *types.KnowledgeConflict) bool {
	if conflict == nil {
		return false
	}
	changed := false
	if strings.TrimSpace(conflict.FactKey) == "" {
		anchor := conservativeChunkPairAnchor(
			&types.Chunk{ID: conflict.ChunkIDA}, &types.Chunk{ID: conflict.ChunkIDB},
		)
		conflict.FactKey = anchor.FactKey
		conflict.FactAnchorKind = anchor.AnchorKind
		changed = true
	}
	if conflict.FactAnchorKind == "" {
		conflict.FactAnchorKind = inferConflictAnchorKind(conflict.FactKey)
		changed = true
	}
	return changed
}

func conservativeChunkPairAnchor(newChunk, existingChunk *types.Chunk) conflictFactAnchor {
	leftID, rightID := "", ""
	if newChunk != nil {
		leftID = strings.TrimSpace(newChunk.ID)
	}
	if existingChunk != nil {
		rightID = strings.TrimSpace(existingChunk.ID)
	}
	ids := []string{leftID, rightID}
	sort.Strings(ids)
	return conflictFactAnchor{
		FactKey:    stableConflictFactKey(types.ConflictFactAnchorChunkPair, ids...),
		AnchorKind: types.ConflictFactAnchorChunkPair,
	}
}

func stableConflictFactKey(anchorKind string, parts ...string) string {
	clean := make([]string, 0, len(parts))
	for _, part := range parts {
		clean = append(clean, strings.TrimSpace(part))
	}
	raw := anchorKind + ":" + strings.Join(clean, "|")
	if len([]rune(raw)) <= 512 {
		return raw
	}
	sum := sha256.Sum256([]byte(raw))
	return anchorKind + ":sha256:" + hex.EncodeToString(sum[:])
}

func inferConflictAnchorKind(factKey string) string {
	for _, kind := range []string{
		types.ConflictFactAnchorClaimKey,
		types.ConflictFactAnchorFuzzySlot,
		types.ConflictFactAnchorDocumentSingleton,
		types.ConflictFactAnchorChunkPair,
	} {
		if strings.HasPrefix(factKey, kind+":") {
			return kind
		}
	}
	return types.ConflictFactAnchorChunkPair
}

func sharedExactClaimKey(left, right claimEvidence) string {
	leftKey := strings.TrimSpace(left.ClaimKey)
	rightKey := strings.TrimSpace(right.ClaimKey)
	if leftKey != "" && leftKey == rightKey {
		return leftKey
	}
	return ""
}

func claimEvidenceForKey(evidence []claimEvidence, claimKey string) []claimEvidence {
	out := make([]claimEvidence, 0, len(evidence))
	for _, item := range evidence {
		if item.ClaimKey == claimKey {
			out = append(out, item)
		}
	}
	return out
}

func firstNonEmptyClaimField(
	primary, fallback []claimEvidence,
	field func(claimEvidence) string,
) string {
	for _, items := range [][]claimEvidence{primary, fallback} {
		for _, item := range items {
			if value := strings.TrimSpace(field(item)); value != "" {
				return value
			}
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func aggregateDisputedFact(
	tenantID uint64,
	kbID, factKey string,
	members []*types.KnowledgeConflict,
) *types.DisputedFact {
	fact := &types.DisputedFact{
		TenantID:         tenantID,
		KnowledgeBaseID:  kbID,
		ClustererVersion: types.ConflictClustererVersion,
		FactKey:          factKey,
		CandidateValues: types.StringArray{},
		SourceRefs:      types.StringArray{},
	}
	values := make(map[string]struct{})
	sources := make(map[string]struct{})
	conflictTypes := make(map[string]struct{})
	activeAdoptionIDs := make(map[string]struct{})
	allMembersFromActiveAdoption := true
	pendingCount := 0
	for _, member := range members {
		if member == nil {
			continue
		}
		if fact.AnchorKind == "" {
			fact.AnchorKind = member.FactAnchorKind
		}
		if fact.ClaimKey == "" {
			fact.ClaimKey = member.ClaimKey
		}
		if fact.Subject == "" {
			fact.Subject = member.FactSubject
		}
		if fact.Predicate == "" {
			fact.Predicate = member.FactPredicate
		}
		for _, value := range []string{member.FactValueA, member.FactValueB} {
			if value = strings.TrimSpace(value); value != "" {
				values[value] = struct{}{}
			}
		}
		for _, ref := range conflictSourceRefs(member) {
			sources[ref] = struct{}{}
		}
		if member.ConflictType != "" {
			conflictTypes[member.ConflictType] = struct{}{}
		}
		if member.Status == types.ConflictStatusPending {
			pendingCount++
		}
		if member.Status == types.ConflictStatusResolvedGlobalWinner && member.WinnerAdoptionID != "" {
			activeAdoptionIDs[member.WinnerAdoptionID] = struct{}{}
		} else {
			allMembersFromActiveAdoption = false
		}
		fact.ConflictCount++
	}
	if fact.AnchorKind == "" {
		fact.AnchorKind = inferConflictAnchorKind(factKey)
	}
	fact.PendingConflictCount = pendingCount
	fact.CandidateValues = sortedStringSet(values)
	fact.CandidateValueCount = len(fact.CandidateValues)
	fact.SourceRefs = sortedStringSet(sources)
	fact.SourceCount = len(fact.SourceRefs)
	fact.ConflictType = aggregateConflictType(conflictTypes)
	if pendingCount > 0 {
		fact.Status = types.DisputedFactStatusPending
	} else {
		fact.Status = types.DisputedFactStatusResolved
	}
	if fact.ConflictCount > 0 && pendingCount == 0 && allMembersFromActiveAdoption && len(activeAdoptionIDs) == 1 {
		for adoptionID := range activeAdoptionIDs {
			fact.ActiveWinnerAdoptionID = adoptionID
		}
	}
	proposal := suggestDisputedFactWinner(members)
	fact.SuggestedWinnerKnowledgeID = proposal.WinnerKnowledgeID
	fact.WinnerProposalReason = proposal.Reason
	fact.WinnerProposalConfidence = proposal.Confidence
	fact.WinnerProposalVersion = proposal.Version
	fact.WinnerProposalSourceCount = proposal.SourceCount
	return fact
}

type disputedFactWinnerProposal struct {
	WinnerKnowledgeID string
	Reason            string
	Confidence        float64
	Version           string
	SourceCount       int
}

type conflictWinnerSource struct {
	KnowledgeID string
	Meta        types.ConflictDocumentMeta
}

// suggestDisputedFactWinner computes a unique global maximum source across
// all member metadata. It is a proposal only: no raw conflict status, chunk,
// or C4.5 resolution changes here. Any missing issuer, metadata disagreement,
// incomparable pair, or non-unique maximum produces no proposal.
func suggestDisputedFactWinner(members []*types.KnowledgeConflict) disputedFactWinnerProposal {
	sources := make(map[string]conflictWinnerSource)
	for _, member := range members {
		if member == nil || !addConflictWinnerSource(sources, member.KnowledgeIDA, member.DocMetaA) ||
			!addConflictWinnerSource(sources, member.KnowledgeIDB, member.DocMetaB) {
			return disputedFactWinnerProposal{}
		}
	}
	if len(sources) < 2 {
		return disputedFactWinnerProposal{}
	}

	ordered := make([]conflictWinnerSource, 0, len(sources))
	issuer := ""
	for _, source := range sources {
		normalizedIssuer := normalizeConflictIssuer(source.Meta.Issuer)
		if normalizedIssuer == "" {
			return disputedFactWinnerProposal{}
		}
		if issuer == "" {
			issuer = normalizedIssuer
		} else if issuer != normalizedIssuer {
			return disputedFactWinnerProposal{}
		}
		ordered = append(ordered, source)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].KnowledgeID < ordered[j].KnowledgeID })

	winners := make([]struct {
		source     conflictWinnerSource
		confidence float64
	}, 0, 1)
	for index, candidate := range ordered {
		valid := true
		confidence := 1.0
		for otherIndex, other := range ordered {
			if index == otherIndex {
				continue
			}
			direction, pairConfidence, ok := compareConflictDocumentRecency(candidate.Meta, other.Meta)
			if !ok || direction <= 0 {
				valid = false
				break
			}
			if pairConfidence < confidence {
				confidence = pairConfidence
			}
		}
		if valid {
			winners = append(winners, struct {
				source     conflictWinnerSource
				confidence float64
			}{source: candidate, confidence: confidence})
		}
	}
	if len(winners) != 1 {
		return disputedFactWinnerProposal{}
	}
	winner := winners[0]
	meta := winner.source.Meta
	display := meta.Title
	if display == "" {
		display = winner.source.KnowledgeID
	}
	reason := fmt.Sprintf(
		"[c3c4:%s] 同发布机构“%s”；在 %d 个来源中，%s 是对其余每个来源均严格较新的唯一最大版本（生效日期=%s，版本=%s）。仅 proposal，不自动裁决。",
		types.DisputedFactWinnerProposalVersion, meta.Issuer, len(ordered), display, meta.EffectiveDate, meta.Version,
	)
	return disputedFactWinnerProposal{
		WinnerKnowledgeID: winner.source.KnowledgeID,
		Reason:            reason,
		Confidence:        winner.confidence,
		Version:           types.DisputedFactWinnerProposalVersion,
		SourceCount:       len(ordered),
	}
}

func addConflictWinnerSource(
	sources map[string]conflictWinnerSource,
	knowledgeID string,
	raw types.JSON,
) bool {
	knowledgeID = strings.TrimSpace(knowledgeID)
	if knowledgeID == "" {
		return false
	}
	meta, ok := conflictDocumentMetaFromJSON(raw)
	if !ok {
		return false
	}
	if meta.KnowledgeID != "" && meta.KnowledgeID != knowledgeID {
		return false
	}
	if existing, found := sources[knowledgeID]; found {
		return conflictWinnerMetaEquivalent(existing.Meta, meta)
	}
	sources[knowledgeID] = conflictWinnerSource{KnowledgeID: knowledgeID, Meta: meta}
	return true
}

func conflictDocumentMetaFromJSON(raw types.JSON) (types.ConflictDocumentMeta, bool) {
	if len(raw) == 0 {
		return types.ConflictDocumentMeta{}, false
	}
	var meta types.ConflictDocumentMeta
	if err := json.Unmarshal(raw, &meta); err != nil || meta.ParserVersion == "" {
		return types.ConflictDocumentMeta{}, false
	}
	return meta, true
}

func conflictWinnerMetaEquivalent(left, right types.ConflictDocumentMeta) bool {
	return normalizeConflictIssuer(left.Issuer) == normalizeConflictIssuer(right.Issuer) &&
		left.EffectiveDate == right.EffectiveDate && left.Version == right.Version
}

func conflictSourceRefs(conflict *types.KnowledgeConflict) []string {
	if conflict == nil {
		return nil
	}
	refs := make([]string, 0, 2)
	if conflict.KnowledgeIDA != "" {
		refs = append(refs, "knowledge:"+conflict.KnowledgeIDA)
	} else if conflict.ChunkIDA != "" {
		refs = append(refs, "chunk:"+conflict.ChunkIDA)
	}
	if conflict.KnowledgeIDB != "" {
		refs = append(refs, "knowledge:"+conflict.KnowledgeIDB)
	} else if conflict.ChunkIDB != "" {
		refs = append(refs, "chunk:"+conflict.ChunkIDB)
	}
	return refs
}

func sortedStringSet(values map[string]struct{}) types.StringArray {
	out := make(types.StringArray, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func aggregateConflictType(typesSeen map[string]struct{}) string {
	if len(typesSeen) == 0 {
		return types.ConflictTypeFactContradiction
	}
	if len(typesSeen) > 1 {
		return types.DisputedFactConflictTypeMixed
	}
	for conflictType := range typesSeen {
		return conflictType
	}
	return types.ConflictTypeFactContradiction
}
