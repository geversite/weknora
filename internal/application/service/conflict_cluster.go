package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/Tencent/WeKnora/internal/logger"
	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// conflictClusterService implements C4-Lite's deterministic raw conflict →
// DisputedFact projection. It intentionally does not resolve member conflicts
// or write to wiki pages; those side effects are later C4/C7 work.
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
// direct claim row even though each underlying document has exactly one
// unambiguous factual claim. In that narrow case, use document-level claims to
// recover a fuzzy cluster anchor. It never changes candidate generation,
// deterministic rules, or an LLM verdict.
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
			pair.NewChunk == nil || pair.ExistingChunk == nil {
			continue
		}
		if hints := selectFallbackClaimHints(pair.NewClaimEvidence, pair.ExistClaimEvidence); len(hints) > 0 {
			pair.FallbackFactAnchorHints = hints
			continue
		}

		newClaims := loadKnowledgeClaims(pair.NewChunk.KnowledgeID)
		existingClaims := loadKnowledgeClaims(pair.ExistingChunk.KnowledgeID)
		// A document-level source may legitimately contain many unrelated facts.
		// Only promote this scope when exactly one usable claim exists on each
		// side; otherwise leave the row at a conservative chunk_pair anchor.
		if len(newClaims) != 1 || len(existingClaims) != 1 {
			continue
		}
		if hints := selectFallbackClaimHints(newClaims, existingClaims); len(hints) > 0 {
			pair.FallbackFactAnchorHints = hints
		}
	}
	return pairs
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
	if len(hints) == 0 {
		hints = selectFallbackClaimHints(pair.NewClaimEvidence, pair.ExistClaimEvidence)
	}
	if len(hints) > 0 {
		hint := hints[0]
		leftKey := fallbackClaimSlotKey(hint.Newer)
		rightKey := fallbackClaimSlotKey(hint.Older)
		keys := []string{leftKey, rightKey}
		sort.Strings(keys)
		return conflictFactAnchor{
			FactKey:    stableConflictFactKey(types.ConflictFactAnchorFuzzySlot, keys...),
			AnchorKind: types.ConflictFactAnchorFuzzySlot,
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
		types.ConflictFactAnchorChunkPair,
	} {
		if strings.HasPrefix(factKey, kind+":") {
			return kind
		}
	}
	return types.ConflictFactAnchorChunkPair
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
	return fact
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
