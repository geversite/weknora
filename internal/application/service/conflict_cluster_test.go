package service

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/Tencent/WeKnora/internal/types"
)

type memoryConflictClusterRepo struct {
	conflicts []*types.KnowledgeConflict
	updates   int
}

func (r *memoryConflictClusterRepo) BatchCreate(_ context.Context, conflicts []*types.KnowledgeConflict) error {
	r.conflicts = append(r.conflicts, conflicts...)
	return nil
}

func (r *memoryConflictClusterRepo) ListByKB(
	_ context.Context, tenantID uint64, kbID, status string, limit, offset int,
) ([]*types.KnowledgeConflict, error) {
	out := make([]*types.KnowledgeConflict, 0)
	for _, conflict := range r.conflicts {
		if conflict == nil || conflict.TenantID != tenantID || conflict.KnowledgeBaseID != kbID {
			continue
		}
		if status != "" && conflict.Status != status {
			continue
		}
		out = append(out, conflict)
	}
	if offset >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *memoryConflictClusterRepo) ListByKBForClustering(
	_ context.Context, tenantID uint64, kbID string,
) ([]*types.KnowledgeConflict, error) {
	out := make([]*types.KnowledgeConflict, 0)
	for _, conflict := range r.conflicts {
		if conflict != nil && conflict.TenantID == tenantID && conflict.KnowledgeBaseID == kbID {
			out = append(out, conflict)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (r *memoryConflictClusterRepo) CountByKB(
	ctx context.Context, tenantID uint64, kbID, status string,
) (int64, error) {
	items, err := r.ListByKB(ctx, tenantID, kbID, status, 0, 0)
	return int64(len(items)), err
}

func (r *memoryConflictClusterRepo) GetByID(_ context.Context, id string) (*types.KnowledgeConflict, error) {
	for _, conflict := range r.conflicts {
		if conflict != nil && conflict.ID == id {
			return conflict, nil
		}
	}
	return nil, fmt.Errorf("conflict %s not found", id)
}

func (r *memoryConflictClusterRepo) Update(_ context.Context, _ *types.KnowledgeConflict) error {
	r.updates++
	return nil
}

func (r *memoryConflictClusterRepo) ResolvePendingByClusterID(
	_ context.Context,
	tenantID uint64,
	kbID, clusterID, status, resolverUserID, note string,
) ([]*types.KnowledgeConflict, error) {
	now := time.Now()
	updated := make([]*types.KnowledgeConflict, 0)
	for _, conflict := range r.conflicts {
		if conflict == nil || conflict.TenantID != tenantID || conflict.KnowledgeBaseID != kbID ||
			conflict.ClusterID != clusterID || conflict.Status != types.ConflictStatusPending {
			continue
		}
		conflict.Status = status
		conflict.ResolvedBy = resolverUserID
		conflict.ResolvedAt = &now
		conflict.ResolutionNote = note
		conflict.UpdatedAt = now
		updated = append(updated, conflict)
	}
	return updated, nil
}

func (r *memoryConflictClusterRepo) AdoptPendingWinnerProposal(
	_ context.Context,
	tenantID uint64,
	kbID, resolverUserID string,
	req types.DisputedFactWinnerAdoption,
) (*types.DisputedFactWinnerAdoptionResult, error) {
	now := time.Now().UTC()
	adoptionID := "adoption-" + req.DisputedFactID
	updated := make([]string, 0)
	disabledChunks := make(map[string]struct{})
	disabledKnowledge := make(map[string]struct{})
	clearPenalty := make(map[string]struct{})
	for _, conflict := range r.conflicts {
		if conflict == nil || conflict.TenantID != tenantID || conflict.KnowledgeBaseID != kbID ||
			conflict.ClusterID != req.DisputedFactID || conflict.Status != types.ConflictStatusPending {
			continue
		}
		updated = append(updated, conflict.ID)
		for _, side := range []struct{ knowledgeID, chunkID string }{
			{conflict.KnowledgeIDA, conflict.ChunkIDA},
			{conflict.KnowledgeIDB, conflict.ChunkIDB},
		} {
			if side.chunkID != "" {
				clearPenalty[side.chunkID] = struct{}{}
			}
			if side.knowledgeID != req.ExpectedWinnerKnowledgeID && side.chunkID != "" {
				disabledChunks[side.chunkID] = struct{}{}
				disabledKnowledge[side.knowledgeID] = struct{}{}
			}
		}
		conflict.Status = types.ConflictStatusResolvedGlobalWinner
		conflict.WinnerAdoptionID = adoptionID
		conflict.ResolvedBy = resolverUserID
		conflict.ResolvedAt = &now
		conflict.AutoResolved = false
		conflict.UpdatedAt = now
	}
	if len(updated) == 0 {
		return nil, fmt.Errorf("no pending member conflicts")
	}
	sort.Strings(updated)
	return &types.DisputedFactWinnerAdoptionResult{
		DisputedFactID:      req.DisputedFactID,
		Resolution:           types.ConflictStatusResolvedGlobalWinner,
		WinnerKnowledgeID:    req.ExpectedWinnerKnowledgeID,
		ProposalVersion:      req.ExpectedProposalVersion,
		ProposalSourceCount:  req.ExpectedProposalSourceCount,
		AdoptionVersion:      types.DisputedFactWinnerAdoptionVersion,
		WinnerAdoptionID:     adoptionID,
		AdoptedAt:            now,
		UpdatedConflictIDs:   updated,
		UpdatedConflictCount: len(updated),
		DisabledChunkIDs:     []string(sortedStringSet(disabledChunks)),
		DisabledKnowledgeIDs: []string(sortedStringSet(disabledKnowledge)),
		ClearPenaltyChunkIDs: []string(sortedStringSet(clearPenalty)),
	}, nil
}

func (r *memoryConflictClusterRepo) ReopenWinnerAdoption(
	_ context.Context,
	tenantID uint64,
	kbID, _ string,
	req types.DisputedFactWinnerRevocation,
) (*types.DisputedFactWinnerRevocationResult, error) {
	now := time.Now().UTC()
	reopened := make([]string, 0)
	for _, conflict := range r.conflicts {
		if conflict == nil || conflict.TenantID != tenantID || conflict.KnowledgeBaseID != kbID ||
			conflict.ClusterID != req.DisputedFactID || conflict.Status != types.ConflictStatusResolvedGlobalWinner ||
			conflict.WinnerAdoptionID != req.WinnerAdoptionID {
			continue
		}
		conflict.Status = types.ConflictStatusPending
		conflict.WinnerAdoptionID = ""
		conflict.ResolvedBy = ""
		conflict.ResolvedAt = nil
		conflict.AutoResolved = false
		conflict.UpdatedAt = now
		reopened = append(reopened, conflict.ID)
	}
	if len(reopened) == 0 {
		return nil, fmt.Errorf("no active winner adoption members")
	}
	sort.Strings(reopened)
	return &types.DisputedFactWinnerRevocationResult{
		DisputedFactID:       req.DisputedFactID,
		WinnerAdoptionID:      req.WinnerAdoptionID,
		ReopenVersion:         types.DisputedFactWinnerReopenVersion,
		RevokedAt:             now,
		ReopenedConflictIDs:   reopened,
		ReopenedConflictCount: len(reopened),
	}, nil
}

func (r *memoryConflictClusterRepo) ListPendingByChunkIDs(_ context.Context, chunkIDs []string) ([]*types.KnowledgeConflict, error) {
	wanted := make(map[string]bool)
	for _, id := range chunkIDs {
		wanted[id] = true
	}
	out := make([]*types.KnowledgeConflict, 0)
	for _, conflict := range r.conflicts {
		if conflict != nil && conflict.Status == types.ConflictStatusPending &&
			(wanted[conflict.ChunkIDA] || wanted[conflict.ChunkIDB]) {
			out = append(out, conflict)
		}
	}
	return out, nil
}

func (r *memoryConflictClusterRepo) ListPendingByKnowledgeID(_ context.Context, knowledgeID string) ([]*types.KnowledgeConflict, error) {
	out := make([]*types.KnowledgeConflict, 0)
	for _, conflict := range r.conflicts {
		if conflict != nil && conflict.Status == types.ConflictStatusPending &&
			(conflict.KnowledgeIDA == knowledgeID || conflict.KnowledgeIDB == knowledgeID) {
			out = append(out, conflict)
		}
	}
	return out, nil
}

func (r *memoryConflictClusterRepo) HasPendingByChunkPair(_ context.Context, left, right string) (bool, error) {
	for _, conflict := range r.conflicts {
		if conflict == nil || conflict.Status != types.ConflictStatusPending {
			continue
		}
		if (conflict.ChunkIDA == left && conflict.ChunkIDB == right) ||
			(conflict.ChunkIDA == right && conflict.ChunkIDB == left) {
			return true, nil
		}
	}
	return false, nil
}

func (r *memoryConflictClusterRepo) DeleteByKnowledge(_ context.Context, knowledgeID string) error {
	out := r.conflicts[:0]
	for _, conflict := range r.conflicts {
		if conflict == nil || (conflict.KnowledgeIDA != knowledgeID && conflict.KnowledgeIDB != knowledgeID) {
			out = append(out, conflict)
		}
	}
	r.conflicts = out
	return nil
}

func (r *memoryConflictClusterRepo) DeleteByKB(_ context.Context, kbID string) error {
	out := r.conflicts[:0]
	for _, conflict := range r.conflicts {
		if conflict == nil || conflict.KnowledgeBaseID != kbID {
			out = append(out, conflict)
		}
	}
	r.conflicts = out
	return nil
}

type memoryDisputedFactRepo struct {
	facts map[string]*types.DisputedFact
	next  int
}

func disputedFactMemoryKey(tenantID uint64, kbID, factKey string) string {
	return fmt.Sprintf("%d/%s/%s", tenantID, kbID, factKey)
}

func (r *memoryDisputedFactRepo) UpsertByFactKey(_ context.Context, fact *types.DisputedFact) (*types.DisputedFact, error) {
	if r.facts == nil {
		r.facts = make(map[string]*types.DisputedFact)
	}
	key := disputedFactMemoryKey(fact.TenantID, fact.KnowledgeBaseID, fact.FactKey)
	stored, ok := r.facts[key]
	if !ok {
		r.next++
		stored = &types.DisputedFact{ID: fmt.Sprintf("fact-%d", r.next)}
		r.facts[key] = stored
	}
	id := stored.ID
	*stored = *fact
	stored.ID = id
	return stored, nil
}

func (r *memoryDisputedFactRepo) GetByID(_ context.Context, tenantID uint64, kbID, factID string) (*types.DisputedFact, error) {
	for _, fact := range r.facts {
		if fact.ID == factID && fact.TenantID == tenantID && fact.KnowledgeBaseID == kbID {
			return fact, nil
		}
	}
	return nil, fmt.Errorf("disputed fact %s not found", factID)
}

func (r *memoryDisputedFactRepo) ListByKB(
	_ context.Context, tenantID uint64, kbID, status string, limit, offset int,
) ([]*types.DisputedFact, error) {
	out := make([]*types.DisputedFact, 0)
	for _, fact := range r.facts {
		if fact.TenantID != tenantID || fact.KnowledgeBaseID != kbID {
			continue
		}
		if status != "" && fact.Status != status {
			continue
		}
		out = append(out, fact)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if offset >= len(out) {
		return nil, nil
	}
	out = out[offset:]
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (r *memoryDisputedFactRepo) CountByKB(ctx context.Context, tenantID uint64, kbID, status string) (int64, error) {
	facts, err := r.ListByKB(ctx, tenantID, kbID, status, 0, 0)
	return int64(len(facts)), err
}

func (r *memoryDisputedFactRepo) DeleteExceptFactKeys(_ context.Context, tenantID uint64, kbID string, factKeys []string) error {
	keep := make(map[string]bool)
	for _, key := range factKeys {
		keep[key] = true
	}
	for key, fact := range r.facts {
		if fact.TenantID == tenantID && fact.KnowledgeBaseID == kbID && !keep[fact.FactKey] {
			delete(r.facts, key)
		}
	}
	return nil
}

func (r *memoryDisputedFactRepo) DeleteByKB(_ context.Context, tenantID uint64, kbID string) error {
	for key, fact := range r.facts {
		if fact.TenantID == tenantID && fact.KnowledgeBaseID == kbID {
			delete(r.facts, key)
		}
	}
	return nil
}

func TestHydrateFallbackFactAnchorHintsUsesUniqueDocumentSlot(t *testing.T) {
	repo := &fallbackHintClaimRepo{claimsByKnowledge: map[string][]*types.Claim{
		"new-doc": {
			{
				ID: "new-claim", ClaimKey: "报销单提交时限", Subject: "报销单", Predicate: "提交时限",
				Value: "45 天", ValueNorm: "45|天", ValueKind: types.ClaimValueKindNumber,
			},
		},
		"old-doc": {
			{
				ID: "old-claim", ClaimKey: "报销申请提交时限", Subject: "报销申请", Predicate: "提交时限",
				Value: "30 个自然日", ValueNorm: "30|天", ValueKind: types.ClaimValueKindNumber,
			},
			{
				ID: "old-context", ClaimKey: "差旅报销规定适用范围", Subject: "差旅报销规定", Predicate: "适用范围",
				Value: "全体员工", ValueNorm: "全体员工", ValueKind: types.ClaimValueKindText,
			},
		},
	}}
	service := &KnowledgeConflictService{claimRepo: repo}
	pairs := []conflictPair{{
		NewChunk:      &types.Chunk{ID: "summary-new", KnowledgeID: "new-doc"},
		ExistingChunk: &types.Chunk{ID: "summary-old", KnowledgeID: "old-doc"},
	}}
	got := service.hydrateFallbackFactAnchorHints(context.Background(), 1, pairs)
	if len(got[0].FallbackFactAnchorHints) != 1 {
		t.Fatalf("unique document fallback hints = %+v, want one", got[0].FallbackFactAnchorHints)
	}
	anchor := conflictFactAnchorForPair(got[0])
	if anchor.AnchorKind != types.ConflictFactAnchorFuzzySlot || anchor.ValueA != "45 天" || anchor.ValueB != "30 个自然日" {
		t.Fatalf("unique document fallback anchor = %+v", anchor)
	}
}

func TestHydrateFallbackFactAnchorHintsUsesDocumentSingletonForExtremeSchemaDrift(t *testing.T) {
	repo := &fallbackHintClaimRepo{claimsByKnowledge: map[string][]*types.Claim{
		"new-doc": {
			{ID: "new-claim", ClaimKey: "报销单最晚提交日期", Subject: "报销单", Predicate: "最晚提交日期", Value: "45 天", ValueNorm: "45|天"},
		},
		"old-doc": {
			{ID: "old-claim", ClaimKey: "费用申请受理期限", Subject: "费用申请", Predicate: "受理期限", Value: "30 个自然日", ValueNorm: "30|天"},
		},
	}}
	service := &KnowledgeConflictService{claimRepo: repo}
	pairs := []conflictPair{{
		NewChunk:      &types.Chunk{ID: "summary-new", KnowledgeID: "new-doc"},
		ExistingChunk: &types.Chunk{ID: "summary-old", KnowledgeID: "old-doc"},
	}}
	got := service.hydrateFallbackFactAnchorHints(context.Background(), 1, pairs)
	if got[0].FallbackFactAnchorKind != types.ConflictFactAnchorDocumentSingleton || len(got[0].FallbackFactAnchorHints) != 1 {
		t.Fatalf("document singleton fallback = %+v", got[0])
	}
	if anchor := conflictFactAnchorForPair(got[0]); anchor.AnchorKind != types.ConflictFactAnchorDocumentSingleton {
		t.Fatalf("document singleton anchor = %+v", anchor)
	}
}

func TestHydrateFallbackFactAnchorHintsRejectsAmbiguousDocuments(t *testing.T) {
	repo := &fallbackHintClaimRepo{claimsByKnowledge: map[string][]*types.Claim{
		"new-doc": {
			{ID: "new-1", ClaimKey: "差旅报销提交时限", Subject: "差旅报销", Predicate: "提交时限", Value: "45 天"},
		},
		"old-doc": {
			{ID: "old-1", ClaimKey: "报销申请提交时限", Subject: "报销申请", Predicate: "提交时限", Value: "30 个自然日"},
			{ID: "old-2", ClaimKey: "报销单提交时限", Subject: "报销单", Predicate: "提交时限", Value: "20 天"},
		},
	}}
	service := &KnowledgeConflictService{claimRepo: repo}
	pairs := []conflictPair{{
		NewChunk:      &types.Chunk{ID: "summary-new", KnowledgeID: "new-doc"},
		ExistingChunk: &types.Chunk{ID: "summary-old", KnowledgeID: "old-doc"},
	}}
	got := service.hydrateFallbackFactAnchorHints(context.Background(), 1, pairs)
	if len(got[0].FallbackFactAnchorHints) != 0 {
		t.Fatalf("ambiguous multi-claim document must remain unanchored: %+v", got[0].FallbackFactAnchorHints)
	}
	if anchor := conflictFactAnchorForPair(got[0]); anchor.AnchorKind != types.ConflictFactAnchorChunkPair {
		t.Fatalf("ambiguous document anchor = %+v, want chunk_pair", anchor)
	}
}

func TestConflictFactAnchorPrefersExactClaimKey(t *testing.T) {
	pair := conflictPair{
		ClaimKeyHit: "差旅餐补每日标准",
		NewClaimEvidence: []claimEvidence{{
			ClaimKey: "差旅餐补每日标准", Subject: "国内出差餐费补贴", Predicate: "每日标准", Value: "150 元",
		}},
		ExistClaimEvidence: []claimEvidence{{
			ClaimKey: "差旅餐补每日标准", Subject: "国内出差餐费补贴", Predicate: "每日标准", Value: "100 元",
		}},
	}
	anchor := conflictFactAnchorForPair(pair)
	if anchor.AnchorKind != types.ConflictFactAnchorClaimKey || anchor.ClaimKey != "差旅餐补每日标准" {
		t.Fatalf("exact anchor = %+v", anchor)
	}
	if anchor.FactKey != "claim_key:差旅餐补每日标准" || anchor.ValueA != "150 元" || anchor.ValueB != "100 元" {
		t.Fatalf("unexpected exact anchor details: %+v", anchor)
	}
}

func TestConflictFactAnchorUsesCanonicalFuzzySlot(t *testing.T) {
	forward := conflictPair{
		NewChunk:      &types.Chunk{ID: "new"},
		ExistingChunk: &types.Chunk{ID: "old"},
		NewClaimEvidence: []claimEvidence{{
			ID: "new", ClaimKey: "幽能引擎原型机测试计划开始时间", Subject: "幽能引擎原型机测试", Predicate: "计划开始时间", Value: "2153 年",
		}},
		ExistClaimEvidence: []claimEvidence{{
			ID: "old", ClaimKey: "幽能引擎原型机测试时间", Subject: "幽能引擎原型机", Predicate: "测试时间", Value: "2150 年前",
		}},
	}
	reverse := conflictPair{
		NewChunk:      &types.Chunk{ID: "old"},
		ExistingChunk: &types.Chunk{ID: "new"},
		NewClaimEvidence: forward.ExistClaimEvidence,
		ExistClaimEvidence: forward.NewClaimEvidence,
	}
	left := conflictFactAnchorForPair(forward)
	right := conflictFactAnchorForPair(reverse)
	if left.AnchorKind != types.ConflictFactAnchorFuzzySlot || left.FactKey != right.FactKey {
		t.Fatalf("fuzzy anchors must be direction-independent: left=%+v right=%+v", left, right)
	}
	if left.ValueA != "2153 年" || left.ValueB != "2150 年前" {
		t.Fatalf("fuzzy values = %+v", left)
	}
}

func TestConflictFactAnchorCanonicalizesEqualFallbackClaimKeys(t *testing.T) {
	pair := conflictPair{
		FallbackFactAnchorHints: []conflictFallbackClaimHint{{
			Newer: claimEvidence{
				ClaimKey: "国内出差餐费补贴每日标准", Subject: "国内出差餐费补贴", Predicate: "每日标准", Value: "150 元",
			},
			Older: claimEvidence{
				ClaimKey: "国内出差餐费补贴每日标准", Subject: "国内出差餐费补贴", Predicate: "每日标准", Value: "100 元",
			},
			Similarity: 1,
		}},
	}
	anchor := conflictFactAnchorForPair(pair)
	if anchor.AnchorKind != types.ConflictFactAnchorClaimKey ||
		anchor.FactKey != "claim_key:国内出差餐费补贴每日标准" ||
		anchor.ClaimKey != "国内出差餐费补贴每日标准" {
		t.Fatalf("equal fallback claim keys must canonicalize to claim_key: %+v", anchor)
	}
}

func TestConflictClusterRebuildAggregatesMembersAndBackfillsLegacy(t *testing.T) {
	const (
		tenantID = uint64(7)
		kbID     = "kb"
		factKey  = "claim_key:国内出差餐费补贴每日标准"
	)
	conflicts := &memoryConflictClusterRepo{conflicts: []*types.KnowledgeConflict{
		{
			ID: "c1", TenantID: tenantID, KnowledgeBaseID: kbID,
			KnowledgeIDA: "doc-b", KnowledgeIDB: "doc-a", ChunkIDA: "chunk-b", ChunkIDB: "chunk-a",
			FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey,
			ClaimKey: "国内出差餐费补贴每日标准", FactSubject: "国内出差餐费补贴", FactPredicate: "每日标准",
			FactValueA: "150 元", FactValueB: "100 元", ConflictType: types.ConflictTypeFactContradiction,
			Status: types.ConflictStatusPending,
		},
		{
			ID: "c2", TenantID: tenantID, KnowledgeBaseID: kbID,
			KnowledgeIDA: "doc-c", KnowledgeIDB: "doc-a", ChunkIDA: "chunk-c", ChunkIDB: "chunk-a",
			FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey,
			ClaimKey: "国内出差餐费补贴每日标准", FactSubject: "国内出差餐费补贴", FactPredicate: "每日标准",
			FactValueA: "200 元", FactValueB: "100 元", ConflictType: types.ConflictTypeFactContradiction,
			Status: types.ConflictStatusPending,
		},
		{
			ID: "c3", TenantID: tenantID, KnowledgeBaseID: kbID,
			KnowledgeIDA: "doc-c", KnowledgeIDB: "doc-b", ChunkIDA: "chunk-c", ChunkIDB: "chunk-b",
			FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey,
			ClaimKey: "国内出差餐费补贴每日标准", FactSubject: "国内出差餐费补贴", FactPredicate: "每日标准",
			FactValueA: "200 元", FactValueB: "150 元", ConflictType: types.ConflictTypeFactContradiction,
			Status: types.ConflictStatusPending,
		},
		{
			ID: "legacy", TenantID: tenantID, KnowledgeBaseID: kbID,
			KnowledgeIDA: "doc-x", KnowledgeIDB: "doc-y", ChunkIDA: "chunk-x", ChunkIDB: "chunk-y",
			ConflictType: types.ConflictTypeFactContradiction, Status: types.ConflictStatusPending,
		},
	}}
	facts := &memoryDisputedFactRepo{}
	service := &conflictClusterService{conflictRepo: conflicts, factRepo: facts}

	result, err := service.Rebuild(context.Background(), tenantID, kbID)
	if err != nil {
		t.Fatalf("Rebuild: %v", err)
	}
	if result.RawConflictCount != 4 || result.DisputedFactCount != 2 || result.AssignedConflictCount != 4 || result.UnanchoredConflictCount != 1 {
		t.Fatalf("unexpected rebuild result: %+v", result)
	}
	if result.AnchorKinds[types.ConflictFactAnchorClaimKey] != 1 || result.AnchorKinds[types.ConflictFactAnchorChunkPair] != 1 {
		t.Fatalf("anchor kinds = %+v", result.AnchorKinds)
	}

	clusterID := conflicts.conflicts[0].ClusterID
	if clusterID == "" || conflicts.conflicts[1].ClusterID != clusterID || conflicts.conflicts[2].ClusterID != clusterID {
		t.Fatalf("same fact members were not assigned one stable cluster: %+v", conflicts.conflicts)
	}
	if conflicts.conflicts[3].FactAnchorKind != types.ConflictFactAnchorChunkPair || conflicts.conflicts[3].ClusterID == "" {
		t.Fatalf("legacy row was not conservatively backfilled: %+v", conflicts.conflicts[3])
	}

	var exact *types.DisputedFact
	for _, fact := range facts.facts {
		if fact.FactKey == factKey {
			exact = fact
			break
		}
	}
	if exact == nil {
		t.Fatal("missing exact disputed fact")
	}
	if exact.ConflictCount != 3 || exact.PendingConflictCount != 3 || exact.SourceCount != 3 || exact.CandidateValueCount != 3 {
		t.Fatalf("aggregate = %+v", exact)
	}
	if got := []string(exact.CandidateValues); fmt.Sprint(got) != "[100 元 150 元 200 元]" {
		t.Fatalf("candidate values = %v", got)
	}

	second, err := service.Rebuild(context.Background(), tenantID, kbID)
	if err != nil || second.DisputedFactCount != 2 {
		t.Fatalf("idempotent rebuild failed: result=%+v err=%v", second, err)
	}
	if conflicts.conflicts[0].ClusterID != clusterID {
		t.Fatalf("cluster ID changed after rebuild: %q -> %q", clusterID, conflicts.conflicts[0].ClusterID)
	}
}

func TestDisputedFactWinnerProposalSelectsUniqueGlobalMaximum(t *testing.T) {
	const (
		tenantID = uint64(9)
		kbID     = "kb-winner"
		factKey  = "claim_key:餐补每日标准"
	)
	metaV1 := conflictDocumentMetaJSON(types.ConflictDocumentMeta{
		ParserVersion: types.ConflictVersionSuggestionVersion, KnowledgeID: "doc-v1", Title: "V1",
		Issuer: "天穹财团", EffectiveDate: "2148-01-01", Version: "1.0",
	})
	metaV2 := conflictDocumentMetaJSON(types.ConflictDocumentMeta{
		ParserVersion: types.ConflictVersionSuggestionVersion, KnowledgeID: "doc-v2", Title: "V2",
		Issuer: "天穹财团", EffectiveDate: "2148-06-01", Version: "2.0",
	})
	metaV3 := conflictDocumentMetaJSON(types.ConflictDocumentMeta{
		ParserVersion: types.ConflictVersionSuggestionVersion, KnowledgeID: "doc-v3", Title: "V3",
		Issuer: "天穹财团", EffectiveDate: "2149-01-01", Version: "3.0",
	})
	conflicts := &memoryConflictClusterRepo{conflicts: []*types.KnowledgeConflict{
		{ID: "v2-v1", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: "doc-v2", KnowledgeIDB: "doc-v1", ChunkIDA: "c2", ChunkIDB: "c1", FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey, DocMetaA: metaV2, DocMetaB: metaV1, Status: types.ConflictStatusPending},
		{ID: "v3-v1", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: "doc-v3", KnowledgeIDB: "doc-v1", ChunkIDA: "c3", ChunkIDB: "c1", FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey, DocMetaA: metaV3, DocMetaB: metaV1, Status: types.ConflictStatusPending},
		{ID: "v3-v2", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: "doc-v3", KnowledgeIDB: "doc-v2", ChunkIDA: "c3", ChunkIDB: "c2", FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey, DocMetaA: metaV3, DocMetaB: metaV2, Status: types.ConflictStatusPending},
	}}
	facts := &memoryDisputedFactRepo{}
	service := &conflictClusterService{conflictRepo: conflicts, factRepo: facts}
	result, err := service.Rebuild(context.Background(), tenantID, kbID)
	if err != nil {
		t.Fatalf("Rebuild winner proposal: %v", err)
	}
	if result.WinnerProposalCount != 1 || len(facts.facts) != 1 {
		t.Fatalf("winner proposal aggregate = %+v facts=%+v", result, facts.facts)
	}
	for _, fact := range facts.facts {
		if fact.SuggestedWinnerKnowledgeID != "doc-v3" || fact.WinnerProposalConfidence < 0.95 ||
			fact.WinnerProposalSourceCount != 3 || fact.WinnerProposalVersion != types.DisputedFactWinnerProposalVersion {
			t.Fatalf("global winner proposal = %+v", fact)
		}
	}
}

func TestAdoptDisputedFactWinnerPropagatesOnlyExplicitGlobalWinner(t *testing.T) {
	const (
		tenantID = uint64(10)
		kbID     = "kb-winner-adopt"
		factKey  = "claim_key:餐补每日标准"
	)
	metaV1 := conflictDocumentMetaJSON(types.ConflictDocumentMeta{
		ParserVersion: types.ConflictVersionSuggestionVersion, KnowledgeID: "doc-v1",
		Issuer: "天穹财团", EffectiveDate: "2148-01-01", Version: "1.0",
	})
	metaV2 := conflictDocumentMetaJSON(types.ConflictDocumentMeta{
		ParserVersion: types.ConflictVersionSuggestionVersion, KnowledgeID: "doc-v2",
		Issuer: "天穹财团", EffectiveDate: "2148-06-01", Version: "2.0",
	})
	metaV3 := conflictDocumentMetaJSON(types.ConflictDocumentMeta{
		ParserVersion: types.ConflictVersionSuggestionVersion, KnowledgeID: "doc-v3",
		Issuer: "天穹财团", EffectiveDate: "2149-01-01", Version: "3.0",
	})
	conflicts := &memoryConflictClusterRepo{conflicts: []*types.KnowledgeConflict{
		// This member has no doc-v3 side. The global decision must still resolve
		// it without treating doc-v2 as the adopted winner.
		{ID: "v2-v1", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: "doc-v2", KnowledgeIDB: "doc-v1", ChunkIDA: "c2", ChunkIDB: "c1", FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey, DocMetaA: metaV2, DocMetaB: metaV1, Status: types.ConflictStatusPending},
		{ID: "v3-v1", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: "doc-v3", KnowledgeIDB: "doc-v1", ChunkIDA: "c3", ChunkIDB: "c1", FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey, DocMetaA: metaV3, DocMetaB: metaV1, Status: types.ConflictStatusPending},
		{ID: "v3-v2", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: "doc-v3", KnowledgeIDB: "doc-v2", ChunkIDA: "c3", ChunkIDB: "c2", FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey, DocMetaA: metaV3, DocMetaB: metaV2, Status: types.ConflictStatusPending},
	}}
	facts := &memoryDisputedFactRepo{}
	service := &conflictClusterService{conflictRepo: conflicts, factRepo: facts}
	if _, err := service.Rebuild(context.Background(), tenantID, kbID); err != nil {
		t.Fatalf("initial Rebuild: %v", err)
	}
	var fact *types.DisputedFact
	for _, item := range facts.facts {
		fact = item
	}
	if fact == nil || fact.SuggestedWinnerKnowledgeID != "doc-v3" {
		t.Fatalf("initial global winner fact = %+v", fact)
	}
	// The real repository obtains this from the public GET response. Give the
	// in-memory fake the same non-zero optimistic token for service validation.
	fact.UpdatedAt = time.Now().UTC()

	result, err := service.AdoptDisputedFactWinner(context.Background(), tenantID, "reviewer-1", kbID,
		types.DisputedFactWinnerAdoption{
			DisputedFactID:            fact.ID,
			ExpectedWinnerKnowledgeID:  "doc-v3",
			ExpectedProposalVersion:    types.DisputedFactWinnerProposalVersion,
			ExpectedProposalUpdatedAt:  fact.UpdatedAt,
			ExpectedProposalSourceCount: 3,
			Note:                       "explicit reviewer action",
		},
	)
	if err != nil {
		t.Fatalf("AdoptDisputedFactWinner: %v", err)
	}
	if result.Resolution != types.ConflictStatusResolvedGlobalWinner || result.WinnerKnowledgeID != "doc-v3" ||
		result.WinnerAdoptionID == "" || result.UpdatedConflictCount != 3 || result.Rebuild == nil {
		t.Fatalf("winner adoption result = %+v", result)
	}
	for _, conflict := range conflicts.conflicts {
		if conflict.Status != types.ConflictStatusResolvedGlobalWinner || conflict.AutoResolved {
			t.Fatalf("global winner was not propagated safely: %+v", conflict)
		}
	}
	stored, err := facts.GetByID(context.Background(), tenantID, kbID, fact.ID)
	if err != nil || stored.Status != types.DisputedFactStatusResolved || stored.PendingConflictCount != 0 ||
		stored.ActiveWinnerAdoptionID != result.WinnerAdoptionID {
		t.Fatalf("rebuilt fact after global adoption = %+v err=%v", stored, err)
	}
}

func TestReopenDisputedFactWinnerRestoresPendingProjection(t *testing.T) {
	const (
		tenantID = uint64(11)
		kbID     = "kb-winner-reopen"
		factKey  = "claim_key:餐补每日标准"
		adoption = "adoption-fact-1"
	)
	conflicts := &memoryConflictClusterRepo{conflicts: []*types.KnowledgeConflict{
		{ID: "v3-v1", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: "doc-v3", KnowledgeIDB: "doc-v1", ChunkIDA: "c3", ChunkIDB: "c1", FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey, Status: types.ConflictStatusResolvedGlobalWinner, WinnerAdoptionID: adoption},
		{ID: "v3-v2", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: "doc-v3", KnowledgeIDB: "doc-v2", ChunkIDA: "c3", ChunkIDB: "c2", FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey, Status: types.ConflictStatusResolvedGlobalWinner, WinnerAdoptionID: adoption},
	}}
	facts := &memoryDisputedFactRepo{}
	service := &conflictClusterService{conflictRepo: conflicts, factRepo: facts}
	if _, err := service.Rebuild(context.Background(), tenantID, kbID); err != nil {
		t.Fatalf("initial Rebuild: %v", err)
	}
	var fact *types.DisputedFact
	for _, item := range facts.facts {
		fact = item
	}
	if fact == nil || fact.Status != types.DisputedFactStatusResolved || fact.ActiveWinnerAdoptionID != adoption {
		t.Fatalf("initial active adoption fact = %+v", fact)
	}
	fact.UpdatedAt = time.Now().UTC()

	result, err := service.ReopenDisputedFactWinner(context.Background(), tenantID, "reviewer-2", kbID,
		types.DisputedFactWinnerRevocation{
			DisputedFactID:               fact.ID,
			WinnerAdoptionID:              adoption,
			ExpectedDisputedFactUpdatedAt: fact.UpdatedAt,
			Note:                          "new evidence requires review",
		},
	)
	if err != nil {
		t.Fatalf("ReopenDisputedFactWinner: %v", err)
	}
	if result.ReopenedConflictCount != 2 || result.Rebuild == nil || result.ReopenVersion != types.DisputedFactWinnerReopenVersion {
		t.Fatalf("winner reopen result = %+v", result)
	}
	for _, conflict := range conflicts.conflicts {
		if conflict.Status != types.ConflictStatusPending || conflict.WinnerAdoptionID != "" || conflict.AutoResolved {
			t.Fatalf("member was not reopened safely: %+v", conflict)
		}
	}
	stored, err := facts.GetByID(context.Background(), tenantID, kbID, fact.ID)
	if err != nil || stored.Status != types.DisputedFactStatusPending || stored.PendingConflictCount != 2 || stored.ActiveWinnerAdoptionID != "" {
		t.Fatalf("rebuilt fact after winner reopen = %+v err=%v", stored, err)
	}
}

func TestDisputedFactWinnerProposalRejectsIssuerMismatch(t *testing.T) {
	members := []*types.KnowledgeConflict{
		{
			KnowledgeIDA: "doc-a", KnowledgeIDB: "doc-b",
			DocMetaA: conflictDocumentMetaJSON(types.ConflictDocumentMeta{ParserVersion: "c3-v1", KnowledgeID: "doc-a", Issuer: "天穹财团", EffectiveDate: "2149-01-01", Version: "2"}),
			DocMetaB: conflictDocumentMetaJSON(types.ConflictDocumentMeta{ParserVersion: "c3-v1", KnowledgeID: "doc-b", Issuer: "新弦工业", EffectiveDate: "2148-01-01", Version: "1"}),
		},
	}
	if proposal := suggestDisputedFactWinner(members); proposal.WinnerKnowledgeID != "" {
		t.Fatalf("issuer mismatch must not yield a global winner: %+v", proposal)
	}
}

func TestSafeDisputedFactResolutionAllowsOnlyNoDisableStatuses(t *testing.T) {
	if !isSafeDisputedFactResolution(types.ConflictStatusResolvedKeepBoth) ||
		!isSafeDisputedFactResolution(types.ConflictStatusResolvedNotConflict) {
		t.Fatal("safe cluster resolutions should be allowed")
	}
	for _, resolution := range []string{
		types.ConflictStatusResolvedNewer,
		types.ConflictStatusResolvedOlder,
		types.ConflictStatusResolvedGlobalWinner,
		"pending",
		"",
	} {
		if isSafeDisputedFactResolution(resolution) {
			t.Fatalf("unsafe cluster resolution %q was allowed", resolution)
		}
	}
}

func TestResolveDisputedFactPropagatesSafeResolution(t *testing.T) {
	const (
		tenantID = uint64(8)
		kbID     = "kb-resolve"
		factKey  = "claim_key:同一事实"
	)
	conflicts := &memoryConflictClusterRepo{conflicts: []*types.KnowledgeConflict{
		{
			ID: "c1", TenantID: tenantID, KnowledgeBaseID: kbID,
			KnowledgeIDA: "doc-b", KnowledgeIDB: "doc-a", ChunkIDA: "chunk-b", ChunkIDB: "chunk-a",
			FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey, ClaimKey: "同一事实",
			FactValueA: "150", FactValueB: "100", ConflictType: types.ConflictTypeFactContradiction,
			Status: types.ConflictStatusPending,
		},
		{
			ID: "c2", TenantID: tenantID, KnowledgeBaseID: kbID,
			KnowledgeIDA: "doc-c", KnowledgeIDB: "doc-a", ChunkIDA: "chunk-c", ChunkIDB: "chunk-a",
			FactKey: factKey, FactAnchorKind: types.ConflictFactAnchorClaimKey, ClaimKey: "同一事实",
			FactValueA: "200", FactValueB: "100", ConflictType: types.ConflictTypeFactContradiction,
			Status: types.ConflictStatusPending,
		},
	}}
	facts := &memoryDisputedFactRepo{}
	service := &conflictClusterService{conflictRepo: conflicts, factRepo: facts}
	if _, err := service.Rebuild(context.Background(), tenantID, kbID); err != nil {
		t.Fatalf("initial Rebuild: %v", err)
	}
	clusterID := conflicts.conflicts[0].ClusterID
	if clusterID == "" || conflicts.conflicts[1].ClusterID != clusterID {
		t.Fatalf("initial cluster IDs = %q / %q", clusterID, conflicts.conflicts[1].ClusterID)
	}

	if _, err := service.ResolveDisputedFact(context.Background(), tenantID, "reviewer-1", kbID, types.DisputedFactResolution{
		DisputedFactID: clusterID,
		Resolution:      types.ConflictStatusResolvedNewer,
	}); err == nil {
		t.Fatal("generic cluster resolver must reject newer_wins outside explicit C4.7 adoption")
	}
	if conflicts.conflicts[0].Status != types.ConflictStatusPending {
		t.Fatal("unsupported cluster resolution must not mutate member conflicts")
	}

	result, err := service.ResolveDisputedFact(context.Background(), tenantID, "reviewer-1", kbID, types.DisputedFactResolution{
		DisputedFactID: clusterID,
		Resolution:      types.ConflictStatusResolvedKeepBoth,
		Note:            "同一事实保留多个来源",
	})
	if err != nil {
		t.Fatalf("ResolveDisputedFact: %v", err)
	}
	if result.UpdatedConflictCount != 2 || len(result.UpdatedConflictIDs) != 2 || len(result.ClearPenaltyChunkIDs) != 3 {
		t.Fatalf("unexpected cluster resolution result: %+v", result)
	}
	for _, conflict := range conflicts.conflicts {
		if conflict.Status != types.ConflictStatusResolvedKeepBoth || conflict.ResolvedBy != "reviewer-1" ||
			conflict.ResolutionNote != "同一事实保留多个来源" {
			t.Fatalf("member was not propagated safely: %+v", conflict)
		}
	}
	fact, err := facts.GetByID(context.Background(), tenantID, kbID, clusterID)
	if err != nil {
		t.Fatalf("GetByID after cluster resolve: %v", err)
	}
	if fact.Status != types.DisputedFactStatusResolved || fact.PendingConflictCount != 0 || fact.ConflictCount != 2 {
		t.Fatalf("rebuilt fact status = %+v", fact)
	}
}
