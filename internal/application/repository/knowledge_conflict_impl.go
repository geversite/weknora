package repository

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type knowledgeConflictRepo struct {
	db *gorm.DB
}

func NewKnowledgeConflictRepository(db *gorm.DB) interfaces.KnowledgeConflictRepository {
	return &knowledgeConflictRepo{db: db}
}

func (r *knowledgeConflictRepo) BatchCreate(ctx context.Context, conflicts []*types.KnowledgeConflict) error {
	if len(conflicts) == 0 {
		return nil
	}
	for _, c := range conflicts {
		if c == nil {
			continue
		}
		if c.ID == "" {
			c.ID = uuid.New().String()
		}
		if c.ConflictType == "" {
			c.ConflictType = types.ConflictTypeFactContradiction
		}
		if c.Status == "" {
			c.Status = types.ConflictStatusPending
		}
		if c.DetectedBy == "" {
			c.DetectedBy = types.ConflictDetectedByUpload
		}
	}
	return r.db.WithContext(ctx).CreateInBatches(conflicts, 100).Error
}

func (r *knowledgeConflictRepo) ListByKB(ctx context.Context, tenantID uint64, kbID, status string, limit, offset int) ([]*types.KnowledgeConflict, error) {
	q := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var conflicts []*types.KnowledgeConflict
	err := q.Order("created_at DESC").Limit(limit).Offset(offset).Find(&conflicts).Error
	return conflicts, err
}

func (r *knowledgeConflictRepo) ListByKBForClustering(ctx context.Context, tenantID uint64, kbID string) ([]*types.KnowledgeConflict, error) {
	var conflicts []*types.KnowledgeConflict
	err := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Order("fact_key ASC, created_at ASC, id ASC").
		Find(&conflicts).Error
	return conflicts, err
}

func (r *knowledgeConflictRepo) CountByKB(ctx context.Context, tenantID uint64, kbID, status string) (int64, error) {
	q := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var count int64
	err := q.Count(&count).Error
	return count, err
}

func (r *knowledgeConflictRepo) GetByID(ctx context.Context, id string) (*types.KnowledgeConflict, error) {
	var conflict types.KnowledgeConflict
	err := r.db.WithContext(ctx).Where("id = ?", id).First(&conflict).Error
	if err != nil {
		return nil, err
	}
	return &conflict, nil
}

func (r *knowledgeConflictRepo) Update(ctx context.Context, conflict *types.KnowledgeConflict) error {
	return r.db.WithContext(ctx).Save(conflict).Error
}

// ResolvePendingByClusterID is the C4.5 atomic member update for safe
// no-disable resolutions. The caller validates the allowed status; keeping
// this operation narrowly scoped prevents a generic resolution from bypassing
// C4.7's explicit global-winner proposal checks.
func (r *knowledgeConflictRepo) ResolvePendingByClusterID(
	ctx context.Context,
	tenantID uint64,
	kbID, clusterID, status, resolverUserID, note string,
) ([]*types.KnowledgeConflict, error) {
	if kbID == "" || clusterID == "" || status == "" {
		return nil, gorm.ErrInvalidData
	}
	var conflicts []*types.KnowledgeConflict
	resolvedAt := time.Now()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND cluster_id = ? AND status = ?",
				tenantID, kbID, clusterID, types.ConflictStatusPending).
			Order("created_at ASC, id ASC").
			Find(&conflicts).Error; err != nil {
			return err
		}
		if len(conflicts) == 0 {
			return nil
		}
		ids := make([]string, 0, len(conflicts))
		for _, conflict := range conflicts {
			if conflict != nil {
				ids = append(ids, conflict.ID)
			}
		}
		if len(ids) == 0 {
			return nil
		}
		if err := tx.Model(&types.KnowledgeConflict{}).
			Where("id IN ?", ids).
			Updates(map[string]interface{}{
				"status":          status,
				"resolved_by":     resolverUserID,
				"resolved_at":     resolvedAt,
				"resolution_note": note,
				"updated_at":      resolvedAt,
			}).Error; err != nil {
			return err
		}
		for _, conflict := range conflicts {
			if conflict == nil {
				continue
			}
			conflict.Status = status
			conflict.ResolvedBy = resolverUserID
			conflict.ResolvedAt = &resolvedAt
			conflict.ResolutionNote = note
			conflict.UpdatedAt = resolvedAt
		}
		return nil
	})
	return conflicts, err
}

// AdoptPendingWinnerProposal is the C4.7 explicit, fact-level resolution
// path. Its single database transaction locks the aggregate, every current raw
// member and every member chunk before it writes anything. In particular, it
// never calls the legacy raw-pair newer/older resolver: a member may omit the
// global winner entirely, so local A/B orientation is not authoritative.
func (r *knowledgeConflictRepo) AdoptPendingWinnerProposal(
	ctx context.Context,
	tenantID uint64,
	kbID, resolverUserID string,
	req types.DisputedFactWinnerAdoption,
) (*types.DisputedFactWinnerAdoptionResult, error) {
	if tenantID == 0 || strings.TrimSpace(kbID) == "" || strings.TrimSpace(req.DisputedFactID) == "" {
		return nil, gorm.ErrInvalidData
	}

	var result *types.DisputedFactWinnerAdoptionResult
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var fact types.DisputedFact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ?", req.DisputedFactID, tenantID, kbID).
			First(&fact).Error; err != nil {
			return err
		}
		if err := validateLockedWinnerProposal(&fact, req); err != nil {
			return err
		}

		var members []*types.KnowledgeConflict
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND cluster_id = ?", tenantID, kbID, fact.ID).
			Order("created_at ASC, id ASC").
			Find(&members).Error; err != nil {
			return err
		}
		if len(members) == 0 {
			return winnerAdoptionConflict("disputed fact %s has no member conflicts", fact.ID)
		}
		if fact.Status != types.DisputedFactStatusPending ||
			fact.ConflictCount != len(members) ||
			fact.PendingConflictCount != len(members) {
			return winnerAdoptionConflict(
				"disputed fact %s member snapshot is stale (fact total=%d pending=%d, locked members=%d)",
				fact.ID, fact.ConflictCount, fact.PendingConflictCount, len(members),
			)
		}

		memberIDs := make([]string, 0, len(members))
		sources, loserChunkOwners, loserKnowledgeIDs, allChunkOwners, err := collectWinnerAdoptionTargets(
			members, fact.SuggestedWinnerKnowledgeID, types.ConflictStatusPending,
		)
		if err != nil {
			return err
		}
		for _, member := range members {
			if member == nil || member.ID == "" {
				return winnerAdoptionConflict("disputed fact %s contains an invalid raw member", fact.ID)
			}
			memberIDs = append(memberIDs, member.ID)
		}
		sort.Strings(memberIDs)
		if _, found := sources[fact.SuggestedWinnerKnowledgeID]; !found ||
			len(sources) != fact.SourceCount ||
			len(sources) != fact.WinnerProposalSourceCount {
			return winnerAdoptionConflict(
				"disputed fact %s source snapshot is stale (fact sources=%d proposal sources=%d, locked sources=%d)",
				fact.ID, fact.SourceCount, fact.WinnerProposalSourceCount, len(sources),
			)
		}

		disabledChunkIDs := sortedWinnerAdoptionChunkIDs(loserChunkOwners)
		if err := lockAndDisableWinnerAdoptionChunks(
			tx, tenantID, kbID, allChunkOwners, disabledChunkIDs, loserChunkOwners,
			fact.SuggestedWinnerKnowledgeID, now,
		); err != nil {
			return err
		}

		resolutionNote := globalWinnerAdoptionNote(
			req.Note, fact.SuggestedWinnerKnowledgeID, fact.WinnerProposalVersion, fact.WinnerProposalSourceCount,
		)
		adoptionID := uuid.NewString()
		disabledKnowledgeIDs := sortedWinnerAdoptionSet(loserKnowledgeIDs)
		adoption := &types.DisputedFactWinnerAdoptionRecord{
			ID:                   adoptionID,
			TenantID:             tenantID,
			KnowledgeBaseID:      kbID,
			DisputedFactID:       fact.ID,
			FactKey:              fact.FactKey,
			WinnerKnowledgeID:    fact.SuggestedWinnerKnowledgeID,
			ProposalVersion:      fact.WinnerProposalVersion,
			ProposalConfidence:   fact.WinnerProposalConfidence,
			ProposalSourceCount:  fact.WinnerProposalSourceCount,
			MemberConflictIDs:    types.StringArray(memberIDs),
			DisabledChunkIDs:     types.StringArray(disabledChunkIDs),
			DisabledKnowledgeIDs: types.StringArray(disabledKnowledgeIDs),
			Status:               types.DisputedFactWinnerAdoptionStatusAdopted,
			AdoptedBy:            resolverUserID,
			AdoptedAt:            now,
			AdoptionNote:         resolutionNote,
			CreatedAt:            now,
			UpdatedAt:            now,
		}
		if err := tx.Create(adoption).Error; err != nil {
			return err
		}

		updated := tx.Model(&types.KnowledgeConflict{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND cluster_id = ? AND id IN ? AND status = ?",
				tenantID, kbID, fact.ID, memberIDs, types.ConflictStatusPending).
			Updates(map[string]interface{}{
				"status":             types.ConflictStatusResolvedGlobalWinner,
				"winner_adoption_id": adoptionID,
				"resolved_by":        resolverUserID,
				"resolved_at":     now,
				"resolution_note": resolutionNote,
				"auto_resolved":   false,
				"updated_at":      now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != int64(len(memberIDs)) {
			return winnerAdoptionConflict(
				"disputed fact %s changed while adopting proposal (updated=%d expected=%d)",
				fact.ID, updated.RowsAffected, len(memberIDs),
			)
		}

		updatedFact := tx.Model(&types.DisputedFact{}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ?", fact.ID, tenantID, kbID).
			Updates(map[string]interface{}{
				"status":                    types.DisputedFactStatusResolved,
				"pending_conflict_count":    0,
				"active_winner_adoption_id": adoptionID,
				"updated_at":                now,
			})
		if updatedFact.Error != nil {
			return updatedFact.Error
		}
		if updatedFact.RowsAffected != 1 {
			return winnerAdoptionConflict("disputed fact %s disappeared while adopting proposal", fact.ID)
		}

		result = &types.DisputedFactWinnerAdoptionResult{
			DisputedFactID:      fact.ID,
			Resolution:           types.ConflictStatusResolvedGlobalWinner,
			WinnerKnowledgeID:    fact.SuggestedWinnerKnowledgeID,
			ProposalVersion:      fact.WinnerProposalVersion,
			ProposalConfidence:   fact.WinnerProposalConfidence,
			ProposalSourceCount:  fact.WinnerProposalSourceCount,
			AdoptionVersion:      types.DisputedFactWinnerAdoptionVersion,
			WinnerAdoptionID:     adoptionID,
			AdoptedAt:            now,
			ResolutionNote:       resolutionNote,
			UpdatedConflictIDs:   memberIDs,
			UpdatedConflictCount: len(memberIDs),
			DisabledChunkIDs:     disabledChunkIDs,
			DisabledKnowledgeIDs: disabledKnowledgeIDs,
			ClearPenaltyChunkIDs: sortedWinnerAdoptionChunkIDs(allChunkOwners),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// ReopenWinnerAdoption is C4.8's explicit reversal path. It can only reopen
// an adoption made after migration 000092, because the durable record ties the
// exact raw members and disabled chunks to one action. Legacy C4.7 rows with an
// empty adoption ID intentionally fail closed rather than guessing what to
// re-enable.
func (r *knowledgeConflictRepo) ReopenWinnerAdoption(
	ctx context.Context,
	tenantID uint64,
	kbID, resolverUserID string,
	req types.DisputedFactWinnerRevocation,
) (*types.DisputedFactWinnerRevocationResult, error) {
	if tenantID == 0 || strings.TrimSpace(kbID) == "" || strings.TrimSpace(req.DisputedFactID) == "" ||
		strings.TrimSpace(req.WinnerAdoptionID) == "" {
		return nil, gorm.ErrInvalidData
	}

	var result *types.DisputedFactWinnerRevocationResult
	now := time.Now().UTC()
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var fact types.DisputedFact
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ?", req.DisputedFactID, tenantID, kbID).
			First(&fact).Error; err != nil {
			return err
		}
		if err := validateLockedWinnerRevocationFact(&fact, req); err != nil {
			return err
		}

		var adoption types.DisputedFactWinnerAdoptionRecord
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND disputed_fact_id = ?",
				req.WinnerAdoptionID, tenantID, kbID, fact.ID).
			First(&adoption).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return winnerAdoptionConflict(
					"active winner adoption %s has no durable C4.8 audit record", req.WinnerAdoptionID,
				)
			}
			return err
		}
		if err := validateLockedWinnerAdoptionRecord(&fact, &adoption); err != nil {
			return err
		}

		var members []*types.KnowledgeConflict
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND cluster_id = ?", tenantID, kbID, fact.ID).
			Order("created_at ASC, id ASC").
			Find(&members).Error; err != nil {
			return err
		}
		if len(members) == 0 || fact.ConflictCount != len(members) || fact.PendingConflictCount != 0 {
			return winnerAdoptionConflict(
				"disputed fact %s member snapshot is stale for reopen (fact total=%d pending=%d, locked members=%d)",
				fact.ID, fact.ConflictCount, fact.PendingConflictCount, len(members),
			)
		}

		memberIDs := make([]string, 0, len(members))
		for _, member := range members {
			if member == nil || member.ID == "" || member.Status != types.ConflictStatusResolvedGlobalWinner ||
				member.WinnerAdoptionID != adoption.ID || member.AutoResolved {
				return winnerAdoptionConflict(
					"disputed fact %s no longer has one uniform active winner adoption", fact.ID,
				)
			}
			memberIDs = append(memberIDs, member.ID)
		}
		sort.Strings(memberIDs)
		if !sameWinnerAdoptionStrings(memberIDs, []string(adoption.MemberConflictIDs)) {
			return winnerAdoptionConflict("winner adoption %s member set changed; refresh and review", adoption.ID)
		}

		sources, loserChunkOwners, loserKnowledgeIDs, _, err := collectWinnerAdoptionTargets(
			members, adoption.WinnerKnowledgeID, types.ConflictStatusResolvedGlobalWinner,
		)
		if err != nil {
			return err
		}
		if _, found := sources[adoption.WinnerKnowledgeID]; !found || len(sources) != adoption.ProposalSourceCount ||
			len(sources) != fact.SourceCount || len(sources) != fact.WinnerProposalSourceCount {
			return winnerAdoptionConflict("winner adoption %s source set changed; refresh and review", adoption.ID)
		}
		reenableChunkIDs := sortedWinnerAdoptionChunkIDs(loserChunkOwners)
		if !sameWinnerAdoptionStrings(reenableChunkIDs, []string(adoption.DisabledChunkIDs)) ||
			!sameWinnerAdoptionStrings(sortedWinnerAdoptionSet(loserKnowledgeIDs), []string(adoption.DisabledKnowledgeIDs)) {
			return winnerAdoptionConflict("winner adoption %s target set changed; refresh and review", adoption.ID)
		}
		if err := lockAndReenableWinnerAdoptionChunks(
			tx, tenantID, kbID, reenableChunkIDs, loserChunkOwners, adoption.ID, adoption.AdoptedAt, now,
		); err != nil {
			return err
		}

		reopenNote := globalWinnerReopenNote(req.Note, adoption.ID, adoption.WinnerKnowledgeID)
		updated := tx.Model(&types.KnowledgeConflict{}).
			Where("tenant_id = ? AND knowledge_base_id = ? AND cluster_id = ? AND id IN ? AND status = ? AND winner_adoption_id = ?",
				tenantID, kbID, fact.ID, memberIDs, types.ConflictStatusResolvedGlobalWinner, adoption.ID).
			Updates(map[string]interface{}{
				"status":             types.ConflictStatusPending,
				"winner_adoption_id": "",
				"resolved_by":        "",
				"resolved_at":        nil,
				"resolution_note":    reopenNote,
				"auto_resolved":      false,
				"updated_at":         now,
			})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != int64(len(memberIDs)) {
			return winnerAdoptionConflict("winner adoption %s changed while reopening", adoption.ID)
		}

		updatedFact := tx.Model(&types.DisputedFact{}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND active_winner_adoption_id = ?",
				fact.ID, tenantID, kbID, adoption.ID).
			Updates(map[string]interface{}{
				"status":                    types.DisputedFactStatusPending,
				"pending_conflict_count":    len(memberIDs),
				"active_winner_adoption_id": "",
				"updated_at":                now,
			})
		if updatedFact.Error != nil {
			return updatedFact.Error
		}
		if updatedFact.RowsAffected != 1 {
			return winnerAdoptionConflict("disputed fact %s changed while reopening adoption", fact.ID)
		}

		updatedAdoption := tx.Model(&types.DisputedFactWinnerAdoptionRecord{}).
			Where("id = ? AND tenant_id = ? AND knowledge_base_id = ? AND status = ?",
				adoption.ID, tenantID, kbID, types.DisputedFactWinnerAdoptionStatusAdopted).
			Updates(map[string]interface{}{
				"status":      types.DisputedFactWinnerAdoptionStatusRevoked,
				"revoked_by":  resolverUserID,
				"revoked_at":  now,
				"revoke_note": reopenNote,
				"updated_at":  now,
			})
		if updatedAdoption.Error != nil {
			return updatedAdoption.Error
		}
		if updatedAdoption.RowsAffected != 1 {
			return winnerAdoptionConflict("winner adoption %s changed while reopening", adoption.ID)
		}

		result = &types.DisputedFactWinnerRevocationResult{
			DisputedFactID:       fact.ID,
			WinnerAdoptionID:      adoption.ID,
			WinnerKnowledgeID:     adoption.WinnerKnowledgeID,
			ReopenVersion:         types.DisputedFactWinnerReopenVersion,
			RevokedAt:             now,
			ReopenNote:            reopenNote,
			ReopenedConflictIDs:   memberIDs,
			ReopenedConflictCount: len(memberIDs),
			ReenabledChunkIDs:     reenableChunkIDs,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func validateLockedWinnerRevocationFact(
	fact *types.DisputedFact,
	req types.DisputedFactWinnerRevocation,
) error {
	if fact == nil || fact.Status != types.DisputedFactStatusResolved || fact.ActiveWinnerAdoptionID == "" {
		return winnerAdoptionConflict("disputed fact has no active winner adoption to reopen")
	}
	if fact.ActiveWinnerAdoptionID != req.WinnerAdoptionID {
		return winnerAdoptionConflict("active winner adoption changed; refresh the disputed fact before reopening")
	}
	if !fact.UpdatedAt.Equal(req.ExpectedDisputedFactUpdatedAt) {
		return winnerAdoptionConflict("disputed fact snapshot is stale; refresh before reopening")
	}
	return nil
}

func validateLockedWinnerAdoptionRecord(
	fact *types.DisputedFact,
	adoption *types.DisputedFactWinnerAdoptionRecord,
) error {
	if adoption == nil || adoption.Status != types.DisputedFactWinnerAdoptionStatusAdopted || adoption.AdoptedAt.IsZero() {
		return winnerAdoptionConflict("winner adoption is not active")
	}
	if adoption.DisputedFactID != fact.ID || adoption.FactKey != fact.FactKey || adoption.WinnerKnowledgeID == "" ||
		adoption.WinnerKnowledgeID != fact.SuggestedWinnerKnowledgeID ||
		adoption.ProposalVersion != fact.WinnerProposalVersion ||
		adoption.ProposalSourceCount < 2 || adoption.ProposalSourceCount != fact.WinnerProposalSourceCount {
		return winnerAdoptionConflict("winner adoption record no longer matches the current proposal")
	}
	return nil
}

func lockAndReenableWinnerAdoptionChunks(
	tx *gorm.DB,
	tenantID uint64,
	kbID string,
	reenableChunkIDs []string,
	loserChunkOwners map[string]string,
	adoptionID string,
	adoptedAt time.Time,
	now time.Time,
) error {
	if len(reenableChunkIDs) == 0 || adoptedAt.IsZero() {
		return winnerAdoptionConflict("winner adoption has no durable reenable chunk snapshot")
	}

	// Lock chunks before inspecting other adoption rows. C4.7 locks the same
	// member chunks before it writes a new adoption, so this ordering prevents
	// a concurrent adoption from committing after the ownership check but
	// before this reopen re-enables the chunk.
	var chunks []*types.Chunk
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND id IN ?", tenantID, kbID, reenableChunkIDs).
		Order("id ASC").
		Find(&chunks).Error; err != nil {
		return err
	}
	if len(chunks) != len(reenableChunkIDs) {
		return winnerAdoptionConflict(
			"disabled chunk snapshot changed (locked=%d expected=%d)", len(chunks), len(reenableChunkIDs),
		)
	}
	for _, chunk := range chunks {
		if chunk == nil {
			return winnerAdoptionConflict("disabled chunk snapshot contains nil row")
		}
		if expectedKnowledgeID, found := loserChunkOwners[chunk.ID]; !found || chunk.KnowledgeID != expectedKnowledgeID {
			return winnerAdoptionConflict("disabled chunk %s no longer matches its adoption source", chunk.ID)
		}
		if chunk.IsEnabled || !chunk.UpdatedAt.Equal(adoptedAt) {
			return winnerAdoptionConflict(
				"disabled chunk %s changed after adoption; refuse unsafe reenable", chunk.ID,
			)
		}
	}
	var otherOwnerCount int64
	if err := tx.Model(&types.KnowledgeConflict{}).
		Where("status = ? AND winner_adoption_id <> ? AND (chunk_id_a IN ? OR chunk_id_b IN ?)",
			types.ConflictStatusResolvedGlobalWinner, adoptionID, reenableChunkIDs, reenableChunkIDs).
		Count(&otherOwnerCount).Error; err != nil {
		return err
	}
	if otherOwnerCount > 0 {
		return winnerAdoptionConflict(
			"one or more loser chunks are owned by another active global winner adoption",
		)
	}
	updated := tx.Model(&types.Chunk{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND id IN ?", tenantID, kbID, reenableChunkIDs).
		Updates(map[string]interface{}{
			"is_enabled": true,
			"updated_at": now,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != int64(len(reenableChunkIDs)) {
		return winnerAdoptionConflict("disabled chunks changed while reopening adoption")
	}
	return nil
}

func globalWinnerReopenNote(note, adoptionID, winnerKnowledgeID string) string {
	base := fmt.Sprintf(
		"[c4.8:%s] explicitly reopened winner_adoption_id=%s former_winner=%s.",
		types.DisputedFactWinnerReopenVersion, adoptionID, winnerKnowledgeID,
	)
	if note = strings.TrimSpace(note); note != "" {
		return base + " " + note
	}
	return base
}

func sameWinnerAdoptionStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	leftCopy := append([]string(nil), left...)
	rightCopy := append([]string(nil), right...)
	sort.Strings(leftCopy)
	sort.Strings(rightCopy)
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func validateLockedWinnerProposal(fact *types.DisputedFact, req types.DisputedFactWinnerAdoption) error {
	if fact == nil {
		return winnerAdoptionConflict("disputed fact is missing")
	}
	if fact.Status != types.DisputedFactStatusPending {
		return winnerAdoptionConflict("disputed fact %s is not pending (status=%s)", fact.ID, fact.Status)
	}
	if fact.ActiveWinnerAdoptionID != "" {
		return winnerAdoptionConflict("disputed fact %s still has an active winner adoption", fact.ID)
	}
	// C4.7 deliberately starts from the strongest fact identity only. Fuzzy
	// and document-singleton clusters remain advisory until their broader
	// identity precision has dedicated adoption evidence.
	if fact.AnchorKind != types.ConflictFactAnchorClaimKey {
		return winnerAdoptionConflict(
			"disputed fact %s uses %q anchor; only exact claim_key clusters are adoptable",
			fact.ID, fact.AnchorKind,
		)
	}
	if fact.SuggestedWinnerKnowledgeID == "" || fact.WinnerProposalVersion == "" ||
		fact.WinnerProposalSourceCount < 2 {
		return winnerAdoptionConflict("disputed fact %s has no adoptable global winner proposal", fact.ID)
	}
	if fact.SuggestedWinnerKnowledgeID != req.ExpectedWinnerKnowledgeID {
		return winnerAdoptionConflict("winner changed; refresh the disputed fact before adoption")
	}
	if fact.WinnerProposalVersion != req.ExpectedProposalVersion {
		return winnerAdoptionConflict("proposal version changed; refresh the disputed fact before adoption")
	}
	if fact.WinnerProposalSourceCount != req.ExpectedProposalSourceCount {
		return winnerAdoptionConflict("proposal source count changed; refresh the disputed fact before adoption")
	}
	if !fact.UpdatedAt.Equal(req.ExpectedProposalUpdatedAt) {
		return winnerAdoptionConflict("proposal snapshot is stale; refresh the disputed fact before adoption")
	}
	return nil
}

func collectWinnerAdoptionTargets(
	members []*types.KnowledgeConflict,
	winnerKnowledgeID string,
	expectedStatus string,
) (map[string]struct{}, map[string]string, map[string]struct{}, map[string]string, error) {
	winnerKnowledgeID = strings.TrimSpace(winnerKnowledgeID)
	if winnerKnowledgeID == "" {
		return nil, nil, nil, nil, winnerAdoptionConflict("winner knowledge id is empty")
	}
	sources := make(map[string]struct{})
	loserChunkOwners := make(map[string]string)
	loserKnowledgeIDs := make(map[string]struct{})
	// allChunkOwners lets the transaction validate every raw A/B chunk side,
	// including winner chunks. A corrupt row must not silently preserve a
	// loser chunk merely because its knowledge ID was recorded as the winner.
	allChunkOwners := make(map[string]string)
	for _, member := range members {
		if member == nil {
			return nil, nil, nil, nil, winnerAdoptionConflict("cluster contains a nil raw member")
		}
		if member.Status != expectedStatus {
			return nil, nil, nil, nil, winnerAdoptionConflict(
				"raw conflict %s has status=%s, expected=%s", member.ID, member.Status, expectedStatus,
			)
		}
		for _, side := range []struct {
			knowledgeID string
			chunkID     string
		}{
			{knowledgeID: member.KnowledgeIDA, chunkID: member.ChunkIDA},
			{knowledgeID: member.KnowledgeIDB, chunkID: member.ChunkIDB},
		} {
			knowledgeID := strings.TrimSpace(side.knowledgeID)
			chunkID := strings.TrimSpace(side.chunkID)
			if knowledgeID == "" || chunkID == "" {
				return nil, nil, nil, nil, winnerAdoptionConflict(
					"raw conflict %s has an unadoptable empty knowledge/chunk side", member.ID,
				)
			}
			sources[knowledgeID] = struct{}{}
			if owner, found := allChunkOwners[chunkID]; found && owner != knowledgeID {
				return nil, nil, nil, nil, winnerAdoptionConflict(
					"chunk %s belongs to conflicting knowledge ids %s and %s", chunkID, owner, knowledgeID,
				)
			}
			allChunkOwners[chunkID] = knowledgeID
			if knowledgeID == winnerKnowledgeID {
				continue
			}
			if owner, found := loserChunkOwners[chunkID]; found && owner != knowledgeID {
				return nil, nil, nil, nil, winnerAdoptionConflict(
					"chunk %s belongs to conflicting loser knowledge ids %s and %s", chunkID, owner, knowledgeID,
				)
			}
			loserChunkOwners[chunkID] = knowledgeID
			loserKnowledgeIDs[knowledgeID] = struct{}{}
		}
	}
	return sources, loserChunkOwners, loserKnowledgeIDs, allChunkOwners, nil
}

func lockAndDisableWinnerAdoptionChunks(
	tx *gorm.DB,
	tenantID uint64,
	kbID string,
	allChunkOwners map[string]string,
	disabledChunkIDs []string,
	loserChunkOwners map[string]string,
	winnerKnowledgeID string,
	now time.Time,
) error {
	if len(disabledChunkIDs) == 0 {
		return winnerAdoptionConflict("global winner adoption has no loser chunks to disable")
	}
	allChunkIDs := sortedWinnerAdoptionChunkIDs(allChunkOwners)
	var chunks []*types.Chunk
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND id IN ?", tenantID, kbID, allChunkIDs).
		Order("id ASC").
		Find(&chunks).Error; err != nil {
		return err
	}
	if len(chunks) != len(allChunkIDs) {
		return winnerAdoptionConflict(
			"member chunk snapshot changed (locked=%d expected=%d)", len(chunks), len(allChunkIDs),
		)
	}
	for _, chunk := range chunks {
		if chunk == nil {
			return winnerAdoptionConflict("member chunk snapshot contains nil row")
		}
		expectedKnowledgeID, found := allChunkOwners[chunk.ID]
		if !found || chunk.KnowledgeID != expectedKnowledgeID {
			return winnerAdoptionConflict("member chunk %s does not match its raw conflict source", chunk.ID)
		}
		// C4.7 starts from an intact review snapshot. If another action already
		// disabled any member chunk, refusing is safer than making a partial
		// adoption appear to have restored a usable global winner.
		if !chunk.IsEnabled {
			return winnerAdoptionConflict("member chunk %s is already disabled; refresh and review the cluster", chunk.ID)
		}
		if chunk.KnowledgeID == winnerKnowledgeID {
			if _, isLoser := loserChunkOwners[chunk.ID]; isLoser {
				return winnerAdoptionConflict("winner chunk %s was also selected as a loser target", chunk.ID)
			}
			continue
		}
		if _, isLoser := loserChunkOwners[chunk.ID]; !isLoser {
			return winnerAdoptionConflict("non-winner member chunk %s was not selected as a loser target", chunk.ID)
		}
	}
	updated := tx.Model(&types.Chunk{}).
		Where("tenant_id = ? AND knowledge_base_id = ? AND id IN ?", tenantID, kbID, disabledChunkIDs).
		Updates(map[string]interface{}{
			"is_enabled": false,
			"updated_at": now,
		})
	if updated.Error != nil {
		return updated.Error
	}
	if updated.RowsAffected != int64(len(disabledChunkIDs)) {
		return winnerAdoptionConflict(
			"loser chunks changed while adopting proposal (updated=%d expected=%d)",
			updated.RowsAffected, len(disabledChunkIDs),
		)
	}
	return nil
}

func globalWinnerAdoptionNote(
	note, winnerKnowledgeID, proposalVersion string,
	proposalSourceCount int,
) string {
	base := fmt.Sprintf(
		"[c4.7:%s] explicitly adopted global winner=%s proposal_version=%s source_count=%d.",
		types.DisputedFactWinnerAdoptionVersion, winnerKnowledgeID, proposalVersion, proposalSourceCount,
	)
	if note = strings.TrimSpace(note); note != "" {
		return base + " " + note
	}
	return base
}

func winnerAdoptionConflict(format string, args ...interface{}) error {
	return fmt.Errorf("%w: %s", types.ErrDisputedFactWinnerAdoptionConflict, fmt.Sprintf(format, args...))
}

func sortedWinnerAdoptionChunkIDs(values map[string]string) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func sortedWinnerAdoptionSet(values map[string]struct{}) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func (r *knowledgeConflictRepo) ListPendingByChunkIDs(ctx context.Context, chunkIDs []string) ([]*types.KnowledgeConflict, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}
	var conflicts []*types.KnowledgeConflict
	err := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("status = ?", types.ConflictStatusPending).
		Where("chunk_id_a IN ? OR chunk_id_b IN ?", chunkIDs, chunkIDs).
		Find(&conflicts).Error
	return conflicts, err
}

func (r *knowledgeConflictRepo) ListPendingByKnowledgeID(ctx context.Context, knowledgeID string) ([]*types.KnowledgeConflict, error) {
	var conflicts []*types.KnowledgeConflict
	err := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("status = ?", types.ConflictStatusPending).
		Where("knowledge_id_a = ? OR knowledge_id_b = ?", knowledgeID, knowledgeID).
		Find(&conflicts).Error
	return conflicts, err
}

func (r *knowledgeConflictRepo) HasPendingByChunkPair(ctx context.Context, chunkAID, chunkBID string) (bool, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.KnowledgeConflict{}).
		Where("status = ?", types.ConflictStatusPending).
		Where("(chunk_id_a = ? AND chunk_id_b = ?) OR (chunk_id_a = ? AND chunk_id_b = ?)",
			chunkAID, chunkBID, chunkBID, chunkAID).
		Count(&count).Error
	return count > 0, err
}

func (r *knowledgeConflictRepo) DeleteByKnowledge(ctx context.Context, knowledgeID string) error {
	return r.db.WithContext(ctx).
		Where("knowledge_id_a = ? OR knowledge_id_b = ?", knowledgeID, knowledgeID).
		Delete(&types.KnowledgeConflict{}).Error
}

func (r *knowledgeConflictRepo) DeleteByKB(ctx context.Context, kbID string) error {
	return r.db.WithContext(ctx).Where("knowledge_base_id = ?", kbID).
		Delete(&types.KnowledgeConflict{}).Error
}
