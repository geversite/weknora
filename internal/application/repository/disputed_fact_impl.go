package repository

import (
	"context"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type disputedFactRepo struct {
	db *gorm.DB
}

func NewDisputedFactRepository(db *gorm.DB) interfaces.DisputedFactRepository {
	return &disputedFactRepo{db: db}
}

// UpsertByFactKey is concurrency-safe at the database uniqueness boundary.
// Rebuilds may overlap while several conflict:detect tasks finish at once; the
// (tenant_id, knowledge_base_id, fact_key) constraint preserves one stable
// cluster ID, and the final SELECT returns that canonical row to the caller.
func (r *disputedFactRepo) UpsertByFactKey(ctx context.Context, fact *types.DisputedFact) (*types.DisputedFact, error) {
	if fact == nil || fact.TenantID == 0 || fact.KnowledgeBaseID == "" || fact.FactKey == "" {
		return nil, gorm.ErrInvalidData
	}
	now := time.Now()
	if fact.ID == "" {
		fact.ID = uuid.NewString()
	}
	if fact.ClustererVersion == "" {
		fact.ClustererVersion = types.ConflictClustererVersion
	}
	if fact.AnchorKind == "" {
		fact.AnchorKind = types.ConflictFactAnchorChunkPair
	}
	if fact.ConflictType == "" {
		fact.ConflictType = types.ConflictTypeFactContradiction
	}
	if fact.Status == "" {
		fact.Status = types.DisputedFactStatusPending
	}
	if fact.CandidateValues == nil {
		fact.CandidateValues = types.StringArray{}
	}
	if fact.SourceRefs == nil {
		fact.SourceRefs = types.StringArray{}
	}
	if fact.CreatedAt.IsZero() {
		fact.CreatedAt = now
	}
	fact.UpdatedAt = now

	updates := map[string]interface{}{
		"clusterer_version":     fact.ClustererVersion,
		"anchor_kind":           fact.AnchorKind,
		"claim_key":             fact.ClaimKey,
		"subject":               fact.Subject,
		"predicate":             fact.Predicate,
		"conflict_type":                 fact.ConflictType,
		"status":                        fact.Status,
		"suggested_winner_knowledge_id": fact.SuggestedWinnerKnowledgeID,
		"winner_proposal_reason":        fact.WinnerProposalReason,
		"winner_proposal_confidence":    fact.WinnerProposalConfidence,
		"winner_proposal_version":       fact.WinnerProposalVersion,
		"winner_proposal_source_count":  fact.WinnerProposalSourceCount,
		"active_winner_adoption_id":     fact.ActiveWinnerAdoptionID,
		"conflict_count":                fact.ConflictCount,
		"pending_conflict_count": fact.PendingConflictCount,
		"source_count":          fact.SourceCount,
		"candidate_value_count": fact.CandidateValueCount,
		"candidate_values":      fact.CandidateValues,
		"source_refs":           fact.SourceRefs,
		"updated_at":            now,
	}
	if err := r.db.WithContext(ctx).Clauses(clause.OnConflict{
		Columns: []clause.Column{
			{Name: "tenant_id"},
			{Name: "knowledge_base_id"},
			{Name: "fact_key"},
		},
		DoUpdates: clause.Assignments(updates),
	}).Create(fact).Error; err != nil {
		return nil, err
	}

	var stored types.DisputedFact
	if err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND fact_key = ?", fact.TenantID, fact.KnowledgeBaseID, fact.FactKey).
		First(&stored).Error; err != nil {
		return nil, err
	}
	return &stored, nil
}

func (r *disputedFactRepo) GetByID(
	ctx context.Context, tenantID uint64, kbID, factID string,
) (*types.DisputedFact, error) {
	var fact types.DisputedFact
	err := r.db.WithContext(ctx).
		Where("id = ? AND tenant_id = ? AND knowledge_base_id = ?", factID, tenantID, kbID).
		First(&fact).Error
	if err != nil {
		return nil, err
	}
	return &fact, nil
}

func (r *disputedFactRepo) ListByKB(
	ctx context.Context, tenantID uint64, kbID, status string, limit, offset int,
) ([]*types.DisputedFact, error) {
	q := r.db.WithContext(ctx).Model(&types.DisputedFact{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var facts []*types.DisputedFact
	err := q.Order("updated_at DESC, id ASC").Limit(limit).Offset(offset).Find(&facts).Error
	return facts, err
}

func (r *disputedFactRepo) CountByKB(ctx context.Context, tenantID uint64, kbID, status string) (int64, error) {
	q := r.db.WithContext(ctx).Model(&types.DisputedFact{}).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)
	if status != "" {
		q = q.Where("status = ?", status)
	}
	var count int64
	err := q.Count(&count).Error
	return count, err
}

func (r *disputedFactRepo) DeleteExceptFactKeys(
	ctx context.Context, tenantID uint64, kbID string, factKeys []string,
) error {
	if kbID == "" {
		return nil
	}
	q := r.db.WithContext(ctx).Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID)
	if len(factKeys) > 0 {
		q = q.Where("fact_key NOT IN ?", factKeys)
	}
	return q.Delete(&types.DisputedFact{}).Error
}

func (r *disputedFactRepo) DeleteByKB(ctx context.Context, tenantID uint64, kbID string) error {
	if kbID == "" {
		return nil
	}
	// C4.8 adoption rows are audit records scoped to the same tenant/KB. Keep
	// them while an individual source is deleted, but remove them when the KB
	// itself is removed so no cross-tenant historical row is orphaned.
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// Keep KB deletion compatible with an operator temporarily running a
		// binary after a failed/disabled migration: the derived fact rows still
		// need cleanup even when the new audit table is unavailable.
		if tx.Migrator().HasTable(&types.DisputedFactWinnerAdoptionRecord{}) {
			if err := tx.Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
				Delete(&types.DisputedFactWinnerAdoptionRecord{}).Error; err != nil {
				return err
			}
		}
		return tx.Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
			Delete(&types.DisputedFact{}).Error
	})
}
