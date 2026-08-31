package service

import (
	"context"
	"fmt"
	"sort"
	"testing"

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

func TestHydrateFallbackFactAnchorHintsUsesSingletonDocumentClaims(t *testing.T) {
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
		},
	}}
	service := &KnowledgeConflictService{claimRepo: repo}
	pairs := []conflictPair{{
		NewChunk:      &types.Chunk{ID: "summary-new", KnowledgeID: "new-doc"},
		ExistingChunk: &types.Chunk{ID: "summary-old", KnowledgeID: "old-doc"},
	}}
	got := service.hydrateFallbackFactAnchorHints(context.Background(), 1, pairs)
	if len(got[0].FallbackFactAnchorHints) != 1 {
		t.Fatalf("singleton document fallback hints = %+v, want one", got[0].FallbackFactAnchorHints)
	}
	anchor := conflictFactAnchorForPair(got[0])
	if anchor.AnchorKind != types.ConflictFactAnchorFuzzySlot || anchor.ValueA != "45 天" || anchor.ValueB != "30 个自然日" {
		t.Fatalf("singleton document fallback anchor = %+v", anchor)
	}
}

func TestHydrateFallbackFactAnchorHintsRejectsAmbiguousDocuments(t *testing.T) {
	repo := &fallbackHintClaimRepo{claimsByKnowledge: map[string][]*types.Claim{
		"new-doc": {
			{ID: "new-1", ClaimKey: "报销单提交时限", Subject: "报销单", Predicate: "提交时限", Value: "45 天"},
			{ID: "new-2", ClaimKey: "病假提报时限", Subject: "病假", Predicate: "提报时限", Value: "24 小时"},
		},
		"old-doc": {
			{ID: "old-1", ClaimKey: "报销申请提交时限", Subject: "报销申请", Predicate: "提交时限", Value: "30 个自然日"},
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
