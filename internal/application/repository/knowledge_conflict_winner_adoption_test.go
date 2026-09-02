package repository

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type winnerAdoptionFixture struct {
	db       *gorm.DB
	repo     interfaces.KnowledgeConflictRepository
	tenantID uint64
	kbID     string
	factID   string
	winnerID string
	req      types.DisputedFactWinnerAdoption
}

func setupWinnerAdoptionFixture(t *testing.T) winnerAdoptionFixture {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(
		&types.KnowledgeConflict{},
		&types.DisputedFact{},
		&types.DisputedFactWinnerAdoptionRecord{},
		&types.Chunk{},
	))

	const (
		tenantID = uint64(41)
		kbID     = "kb-winner-adoption"
		factID   = "fact-winner-adoption"
		docV1    = "doc-v1"
		docV2    = "doc-v2"
		docV3    = "doc-v3"
		chunkV1  = "chunk-v1"
		chunkV2  = "chunk-v2"
		chunkV3  = "chunk-v3"
	)
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	fact := &types.DisputedFact{
		ID:                         factID,
		TenantID:                   tenantID,
		KnowledgeBaseID:            kbID,
		ClustererVersion:           types.ConflictClustererVersion,
		FactKey:                    "claim_key:国内出差餐费补贴每日标准",
		AnchorKind:                 types.ConflictFactAnchorClaimKey,
		ClaimKey:                   "国内出差餐费补贴每日标准",
		ConflictType:               types.DisputedFactConflictTypeMixed,
		Status:                     types.DisputedFactStatusPending,
		SuggestedWinnerKnowledgeID: docV3,
		WinnerProposalReason:       "test unique global winner",
		WinnerProposalConfidence:   0.99,
		WinnerProposalVersion:      types.DisputedFactWinnerProposalVersion,
		WinnerProposalSourceCount:  3,
		ConflictCount:              3,
		PendingConflictCount:       3,
		SourceCount:                3,
		CandidateValueCount:        3,
		CandidateValues:            types.StringArray{"100 元", "150 元", "200 元"},
		SourceRefs:                 types.StringArray{"knowledge:" + docV1, "knowledge:" + docV2, "knowledge:" + docV3},
		CreatedAt:                  now,
		UpdatedAt:                  now,
	}
	require.NoError(t, db.Create(fact).Error)

	chunks := []*types.Chunk{
		{ID: chunkV1, SeqID: 1, TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeID: docV1, Content: "100 元", ChunkType: types.ChunkTypeText, IsEnabled: true},
		{ID: chunkV2, SeqID: 2, TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeID: docV2, Content: "150 元", ChunkType: types.ChunkTypeText, IsEnabled: true},
		{ID: chunkV3, SeqID: 3, TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeID: docV3, Content: "200 元", ChunkType: types.ChunkTypeText, IsEnabled: true},
	}
	require.NoError(t, db.Create(chunks).Error)

	// The first row intentionally contains no global winner. C4.7 must still
	// resolve it as resolved_global_winner and disable both locally-losing
	// sources, rather than choosing one based on raw A/B orientation.
	conflicts := []*types.KnowledgeConflict{
		{ID: "c-v2-v1", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: docV2, KnowledgeIDB: docV1, ChunkIDA: chunkV2, ChunkIDB: chunkV1, ClusterID: factID, FactKey: fact.FactKey, FactAnchorKind: fact.AnchorKind, Status: types.ConflictStatusPending, CreatedAt: now, UpdatedAt: now},
		{ID: "c-v3-v1", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: docV3, KnowledgeIDB: docV1, ChunkIDA: chunkV3, ChunkIDB: chunkV1, ClusterID: factID, FactKey: fact.FactKey, FactAnchorKind: fact.AnchorKind, Status: types.ConflictStatusPending, CreatedAt: now, UpdatedAt: now},
		{ID: "c-v3-v2", TenantID: tenantID, KnowledgeBaseID: kbID, KnowledgeIDA: docV3, KnowledgeIDB: docV2, ChunkIDA: chunkV3, ChunkIDB: chunkV2, ClusterID: factID, FactKey: fact.FactKey, FactAnchorKind: fact.AnchorKind, Status: types.ConflictStatusPending, CreatedAt: now, UpdatedAt: now},
	}
	require.NoError(t, db.Create(conflicts).Error)

	var stored types.DisputedFact
	require.NoError(t, db.First(&stored, "id = ?", factID).Error)
	return winnerAdoptionFixture{
		db:       db,
		repo:     NewKnowledgeConflictRepository(db),
		tenantID: tenantID,
		kbID:     kbID,
		factID:   factID,
		winnerID: docV3,
		req: types.DisputedFactWinnerAdoption{
			DisputedFactID:             factID,
			ExpectedWinnerKnowledgeID:   docV3,
			ExpectedProposalVersion:     types.DisputedFactWinnerProposalVersion,
			ExpectedProposalUpdatedAt:   stored.UpdatedAt,
			ExpectedProposalSourceCount: 3,
			Note:                        "reviewer accepted the global source ordering",
		},
	}
}

func adoptWinnerFixture(t *testing.T, fixture winnerAdoptionFixture) (*types.DisputedFactWinnerAdoptionResult, types.DisputedFact) {
	t.Helper()
	result, err := fixture.repo.AdoptPendingWinnerProposal(
		context.Background(), fixture.tenantID, fixture.kbID, "reviewer-1", fixture.req,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	var fact types.DisputedFact
	require.NoError(t, fixture.db.First(&fact, "id = ?", fixture.factID).Error)
	return result, fact
}

func TestAdoptPendingWinnerProposalUsesGlobalWinnerAndDisablesOnlyLosers(t *testing.T) {
	fixture := setupWinnerAdoptionFixture(t)
	result, fact := adoptWinnerFixture(t, fixture)

	assert.Equal(t, fixture.factID, result.DisputedFactID)
	assert.Equal(t, types.ConflictStatusResolvedGlobalWinner, result.Resolution)
	assert.Equal(t, fixture.winnerID, result.WinnerKnowledgeID)
	assert.Equal(t, types.DisputedFactWinnerProposalVersion, result.ProposalVersion)
	assert.Equal(t, types.DisputedFactWinnerAdoptionVersion, result.AdoptionVersion)
	assert.Equal(t, 3, result.ProposalSourceCount)
	assert.Equal(t, 0.99, result.ProposalConfidence)
	assert.NotEmpty(t, result.WinnerAdoptionID)
	assert.Equal(t, []string{"c-v2-v1", "c-v3-v1", "c-v3-v2"}, result.UpdatedConflictIDs)
	assert.Equal(t, []string{"chunk-v1", "chunk-v2"}, result.DisabledChunkIDs)
	assert.Equal(t, []string{"doc-v1", "doc-v2"}, result.DisabledKnowledgeIDs)
	assert.Equal(t, []string{"chunk-v1", "chunk-v2", "chunk-v3"}, result.ClearPenaltyChunkIDs)
	assert.False(t, result.AdoptedAt.IsZero())
	assert.True(t, strings.Contains(result.ResolutionNote, "global winner=doc-v3"))
	assert.True(t, strings.Contains(result.ResolutionNote, "proposal_version=c3-c4-v1"))

	var conflicts []types.KnowledgeConflict
	require.NoError(t, fixture.db.Order("id ASC").Find(&conflicts).Error)
	require.Len(t, conflicts, 3)
	for _, conflict := range conflicts {
		assert.Equal(t, types.ConflictStatusResolvedGlobalWinner, conflict.Status)
		assert.Equal(t, result.WinnerAdoptionID, conflict.WinnerAdoptionID)
		assert.Equal(t, "reviewer-1", conflict.ResolvedBy)
		assert.False(t, conflict.AutoResolved)
		assert.NotNil(t, conflict.ResolvedAt)
		assert.True(t, strings.Contains(conflict.ResolutionNote, "global winner=doc-v3"))
	}

	var chunks []types.Chunk
	require.NoError(t, fixture.db.Order("id ASC").Find(&chunks).Error)
	require.Len(t, chunks, 3)
	chunkEnabled := map[string]bool{}
	for _, chunk := range chunks {
		chunkEnabled[chunk.ID] = chunk.IsEnabled
	}
	assert.False(t, chunkEnabled["chunk-v1"])
	assert.False(t, chunkEnabled["chunk-v2"])
	assert.True(t, chunkEnabled["chunk-v3"], "the global winner chunk must stay enabled")

	assert.Equal(t, types.DisputedFactStatusResolved, fact.Status)
	assert.Equal(t, 0, fact.PendingConflictCount)
	assert.Equal(t, fixture.winnerID, fact.SuggestedWinnerKnowledgeID)
	assert.Equal(t, types.DisputedFactWinnerProposalVersion, fact.WinnerProposalVersion)
	assert.Equal(t, result.WinnerAdoptionID, fact.ActiveWinnerAdoptionID)

	var adoption types.DisputedFactWinnerAdoptionRecord
	require.NoError(t, fixture.db.First(&adoption, "id = ?", result.WinnerAdoptionID).Error)
	assert.Equal(t, types.DisputedFactWinnerAdoptionStatusAdopted, adoption.Status)
	assert.Equal(t, fixture.factID, adoption.DisputedFactID)
	assert.Equal(t, fixture.winnerID, adoption.WinnerKnowledgeID)
	assert.Equal(t, result.UpdatedConflictIDs, []string(adoption.MemberConflictIDs))
	assert.Equal(t, result.DisabledChunkIDs, []string(adoption.DisabledChunkIDs))
}

func TestAdoptPendingWinnerProposalRejectsStaleSnapshotWithoutMutation(t *testing.T) {
	fixture := setupWinnerAdoptionFixture(t)
	fixture.req.ExpectedProposalUpdatedAt = fixture.req.ExpectedProposalUpdatedAt.Add(-time.Second)
	result, err := fixture.repo.AdoptPendingWinnerProposal(
		context.Background(), fixture.tenantID, fixture.kbID, "reviewer-1", fixture.req,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrDisputedFactWinnerAdoptionConflict)
	assert.Nil(t, result)

	var conflicts []types.KnowledgeConflict
	require.NoError(t, fixture.db.Order("id ASC").Find(&conflicts).Error)
	for _, conflict := range conflicts {
		assert.Equal(t, types.ConflictStatusPending, conflict.Status)
		assert.Nil(t, conflict.ResolvedAt)
		assert.Empty(t, conflict.WinnerAdoptionID)
	}
	var chunks []types.Chunk
	require.NoError(t, fixture.db.Order("id ASC").Find(&chunks).Error)
	for _, chunk := range chunks {
		assert.True(t, chunk.IsEnabled)
	}
	var fact types.DisputedFact
	require.NoError(t, fixture.db.First(&fact, "id = ?", fixture.factID).Error)
	assert.Equal(t, types.DisputedFactStatusPending, fact.Status)
	assert.Equal(t, 3, fact.PendingConflictCount)
	assert.Empty(t, fact.ActiveWinnerAdoptionID)
	var count int64
	require.NoError(t, fixture.db.Model(&types.DisputedFactWinnerAdoptionRecord{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

func TestAdoptPendingWinnerProposalRejectsDisabledMemberChunkWithoutMutation(t *testing.T) {
	fixture := setupWinnerAdoptionFixture(t)
	require.NoError(t, fixture.db.Model(&types.Chunk{}).
		Where("id = ?", "chunk-v3").
		Update("is_enabled", false).Error)

	result, err := fixture.repo.AdoptPendingWinnerProposal(
		context.Background(), fixture.tenantID, fixture.kbID, "reviewer-1", fixture.req,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrDisputedFactWinnerAdoptionConflict)
	assert.Nil(t, result)

	var conflicts []types.KnowledgeConflict
	require.NoError(t, fixture.db.Order("id ASC").Find(&conflicts).Error)
	for _, conflict := range conflicts {
		assert.Equal(t, types.ConflictStatusPending, conflict.Status)
	}
	var chunks []types.Chunk
	require.NoError(t, fixture.db.Order("id ASC").Find(&chunks).Error)
	for _, chunk := range chunks {
		if chunk.ID == "chunk-v3" {
			assert.False(t, chunk.IsEnabled, "the pre-existing disabled state is preserved")
		} else {
			assert.True(t, chunk.IsEnabled)
		}
	}
}

func TestAdoptPendingWinnerProposalRejectsNonExactFactAnchor(t *testing.T) {
	fixture := setupWinnerAdoptionFixture(t)
	require.NoError(t, fixture.db.Model(&types.DisputedFact{}).
		Where("id = ?", fixture.factID).
		Update("anchor_kind", types.ConflictFactAnchorFuzzySlot).Error)
	var fact types.DisputedFact
	require.NoError(t, fixture.db.First(&fact, "id = ?", fixture.factID).Error)
	fixture.req.ExpectedProposalUpdatedAt = fact.UpdatedAt

	result, err := fixture.repo.AdoptPendingWinnerProposal(
		context.Background(), fixture.tenantID, fixture.kbID, "reviewer-1", fixture.req,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrDisputedFactWinnerAdoptionConflict)
	assert.Nil(t, result)

	var conflicts []types.KnowledgeConflict
	require.NoError(t, fixture.db.Find(&conflicts).Error)
	for _, conflict := range conflicts {
		assert.Equal(t, types.ConflictStatusPending, conflict.Status)
	}
}

func TestReopenWinnerAdoptionRestoresOnlyRecordedState(t *testing.T) {
	fixture := setupWinnerAdoptionFixture(t)
	adoptionResult, factAfterAdoption := adoptWinnerFixture(t, fixture)
	reopenReq := types.DisputedFactWinnerRevocation{
		DisputedFactID:               fixture.factID,
		WinnerAdoptionID:              adoptionResult.WinnerAdoptionID,
		ExpectedDisputedFactUpdatedAt: factAfterAdoption.UpdatedAt,
		Note:                          "reviewer reopened the fact for new evidence",
	}

	result, err := fixture.repo.ReopenWinnerAdoption(
		context.Background(), fixture.tenantID, fixture.kbID, "reviewer-2", reopenReq,
	)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, fixture.factID, result.DisputedFactID)
	assert.Equal(t, adoptionResult.WinnerAdoptionID, result.WinnerAdoptionID)
	assert.Equal(t, fixture.winnerID, result.WinnerKnowledgeID)
	assert.Equal(t, types.DisputedFactWinnerReopenVersion, result.ReopenVersion)
	assert.Equal(t, []string{"c-v2-v1", "c-v3-v1", "c-v3-v2"}, result.ReopenedConflictIDs)
	assert.Equal(t, []string{"chunk-v1", "chunk-v2"}, result.ReenabledChunkIDs)
	assert.True(t, strings.Contains(result.ReopenNote, "winner_adoption_id="+adoptionResult.WinnerAdoptionID))

	var conflicts []types.KnowledgeConflict
	require.NoError(t, fixture.db.Order("id ASC").Find(&conflicts).Error)
	for _, conflict := range conflicts {
		assert.Equal(t, types.ConflictStatusPending, conflict.Status)
		assert.Empty(t, conflict.WinnerAdoptionID)
		assert.Empty(t, conflict.ResolvedBy)
		assert.Nil(t, conflict.ResolvedAt)
		assert.False(t, conflict.AutoResolved)
		assert.True(t, strings.Contains(conflict.ResolutionNote, "c4-winner-reopen-v1"))
	}

	var chunks []types.Chunk
	require.NoError(t, fixture.db.Order("id ASC").Find(&chunks).Error)
	for _, chunk := range chunks {
		assert.True(t, chunk.IsEnabled, "all and only recorded disabled chunks are reenabled; winner was already enabled")
	}

	var fact types.DisputedFact
	require.NoError(t, fixture.db.First(&fact, "id = ?", fixture.factID).Error)
	assert.Equal(t, types.DisputedFactStatusPending, fact.Status)
	assert.Equal(t, 3, fact.PendingConflictCount)
	assert.Empty(t, fact.ActiveWinnerAdoptionID)
	assert.Equal(t, fixture.winnerID, fact.SuggestedWinnerKnowledgeID)

	var adoption types.DisputedFactWinnerAdoptionRecord
	require.NoError(t, fixture.db.First(&adoption, "id = ?", adoptionResult.WinnerAdoptionID).Error)
	assert.Equal(t, types.DisputedFactWinnerAdoptionStatusRevoked, adoption.Status)
	assert.Equal(t, "reviewer-2", adoption.RevokedBy)
	assert.NotNil(t, adoption.RevokedAt)
	assert.True(t, strings.Contains(adoption.RevokeNote, "c4-winner-reopen-v1"))

	// Reopen does not automatically re-adopt. A reviewer may explicitly submit
	// the current proposal again, which creates a new durable record while the
	// revoked evidence remains intact.
	reAdoptReq := fixture.req
	reAdoptReq.ExpectedProposalUpdatedAt = fact.UpdatedAt
	second, err := fixture.repo.AdoptPendingWinnerProposal(
		context.Background(), fixture.tenantID, fixture.kbID, "reviewer-3", reAdoptReq,
	)
	require.NoError(t, err)
	assert.NotEqual(t, adoptionResult.WinnerAdoptionID, second.WinnerAdoptionID)
	var reAdoptedFact types.DisputedFact
	require.NoError(t, fixture.db.First(&reAdoptedFact, "id = ?", fixture.factID).Error)
	assert.Equal(t, second.WinnerAdoptionID, reAdoptedFact.ActiveWinnerAdoptionID)
	var preserved types.DisputedFactWinnerAdoptionRecord
	require.NoError(t, fixture.db.First(&preserved, "id = ?", adoptionResult.WinnerAdoptionID).Error)
	assert.Equal(t, types.DisputedFactWinnerAdoptionStatusRevoked, preserved.Status)
}

func TestDisputedFactDeleteByKBCleansDurableWinnerAdoptions(t *testing.T) {
	fixture := setupWinnerAdoptionFixture(t)
	adoption, _ := adoptWinnerFixture(t, fixture)
	require.NotNil(t, adoption)
	factRepo := NewDisputedFactRepository(fixture.db)
	require.NoError(t, factRepo.DeleteByKB(context.Background(), fixture.tenantID, fixture.kbID))

	var facts int64
	require.NoError(t, fixture.db.Model(&types.DisputedFact{}).Count(&facts).Error)
	assert.Equal(t, int64(0), facts)
	var adoptions int64
	require.NoError(t, fixture.db.Model(&types.DisputedFactWinnerAdoptionRecord{}).Count(&adoptions).Error)
	assert.Equal(t, int64(0), adoptions)
}

func TestReopenWinnerAdoptionRejectsStaleSnapshotWithoutMutation(t *testing.T) {
	fixture := setupWinnerAdoptionFixture(t)
	adoptionResult, factAfterAdoption := adoptWinnerFixture(t, fixture)
	reopenReq := types.DisputedFactWinnerRevocation{
		DisputedFactID:               fixture.factID,
		WinnerAdoptionID:              adoptionResult.WinnerAdoptionID,
		ExpectedDisputedFactUpdatedAt: factAfterAdoption.UpdatedAt.Add(-time.Second),
	}

	result, err := fixture.repo.ReopenWinnerAdoption(
		context.Background(), fixture.tenantID, fixture.kbID, "reviewer-2", reopenReq,
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrDisputedFactWinnerAdoptionConflict)
	assert.Nil(t, result)

	var conflicts []types.KnowledgeConflict
	require.NoError(t, fixture.db.Find(&conflicts).Error)
	for _, conflict := range conflicts {
		assert.Equal(t, types.ConflictStatusResolvedGlobalWinner, conflict.Status)
		assert.Equal(t, adoptionResult.WinnerAdoptionID, conflict.WinnerAdoptionID)
	}
	var chunks []types.Chunk
	require.NoError(t, fixture.db.Order("id ASC").Find(&chunks).Error)
	for _, chunk := range chunks {
		if chunk.ID == "chunk-v3" {
			assert.True(t, chunk.IsEnabled)
		} else {
			assert.False(t, chunk.IsEnabled)
		}
	}
}

func TestReopenWinnerAdoptionRejectsChunkOwnedByAnotherActiveAdoption(t *testing.T) {
	fixture := setupWinnerAdoptionFixture(t)
	adoptionResult, factAfterAdoption := adoptWinnerFixture(t, fixture)
	other := &types.KnowledgeConflict{
		ID:                "other-global-adoption",
		TenantID:          fixture.tenantID,
		KnowledgeBaseID:   fixture.kbID,
		KnowledgeIDA:      "other-source",
		KnowledgeIDB:      fixture.winnerID,
		ChunkIDA:          "chunk-v1",
		ChunkIDB:          "chunk-v3",
		ClusterID:         "other-fact",
		FactKey:           "claim_key:other",
		FactAnchorKind:    types.ConflictFactAnchorClaimKey,
		Status:            types.ConflictStatusResolvedGlobalWinner,
		WinnerAdoptionID:  "other-adoption",
	}
	require.NoError(t, fixture.db.Create(other).Error)

	result, err := fixture.repo.ReopenWinnerAdoption(
		context.Background(), fixture.tenantID, fixture.kbID, "reviewer-2", types.DisputedFactWinnerRevocation{
			DisputedFactID:               fixture.factID,
			WinnerAdoptionID:              adoptionResult.WinnerAdoptionID,
			ExpectedDisputedFactUpdatedAt: factAfterAdoption.UpdatedAt,
		},
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, types.ErrDisputedFactWinnerAdoptionConflict)
	assert.Nil(t, result)

	var chunk types.Chunk
	require.NoError(t, fixture.db.First(&chunk, "id = ?", "chunk-v1").Error)
	assert.False(t, chunk.IsEnabled, "a chunk still owned by another adoption must not be reenabled")
	var fact types.DisputedFact
	require.NoError(t, fixture.db.First(&fact, "id = ?", fixture.factID).Error)
	assert.Equal(t, types.DisputedFactStatusResolved, fact.Status)
	assert.Equal(t, adoptionResult.WinnerAdoptionID, fact.ActiveWinnerAdoptionID)
}
