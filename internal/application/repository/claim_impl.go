package repository

import (
	"context"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

type claimRepo struct {
	db *gorm.DB
}

// NewClaimRepository creates the gorm-backed claim repository (C1).
func NewClaimRepository(db *gorm.DB) interfaces.ClaimRepository {
	return &claimRepo{db: db}
}

// ReplaceBySource inserts the new batch and deletes rows of other batches for
// the same source, in one transaction. Idempotent: re-running with the same
// batch converges to the same row set; a mid-way failure rolls back both
// steps so the previous batch stays intact.
func (r *claimRepo) ReplaceBySource(
	ctx context.Context, sourceType, sourceID, batchID string, claims []*types.Claim,
) error {
	if sourceType == "" || sourceID == "" || batchID == "" {
		return gorm.ErrInvalidData
	}
	for _, c := range claims {
		if c == nil {
			continue
		}
		if c.ID == "" {
			c.ID = uuid.New().String()
		}
		c.SourceType = sourceType
		c.SourceID = sourceID
		c.ExtractBatchID = batchID
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if len(claims) > 0 {
			if err := tx.CreateInBatches(claims, 100).Error; err != nil {
				return err
			}
		}
		return tx.Where(
			"source_type = ? AND source_id = ? AND extract_batch_id != ?",
			sourceType, sourceID, batchID,
		).Delete(&types.Claim{}).Error
	})
}

func (r *claimRepo) DeleteBySource(ctx context.Context, sourceType, sourceID string) error {
	if sourceType == "" || sourceID == "" {
		return nil
	}
	return r.db.WithContext(ctx).
		Where("source_type = ? AND source_id = ?", sourceType, sourceID).
		Delete(&types.Claim{}).Error
}

func (r *claimRepo) DeleteByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) error {
	if knowledgeID == "" {
		return nil
	}
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		Delete(&types.Claim{}).Error
}

func (r *claimRepo) DeleteByKnowledgeBase(ctx context.Context, tenantID uint64, kbID string) error {
	if kbID == "" {
		return nil
	}
	return r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ?", tenantID, kbID).
		Delete(&types.Claim{}).Error
}

func (r *claimRepo) ListBySource(ctx context.Context, sourceType, sourceID string) ([]*types.Claim, error) {
	var out []*types.Claim
	err := r.db.WithContext(ctx).
		Where("source_type = ? AND source_id = ?", sourceType, sourceID).
		Order("span_start ASC").
		Find(&out).Error
	return out, err
}

func (r *claimRepo) ListByKnowledge(ctx context.Context, tenantID uint64, knowledgeID string) ([]*types.Claim, error) {
	if knowledgeID == "" {
		return nil, nil
	}
	var out []*types.Claim
	err := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_id = ?", tenantID, knowledgeID).
		Find(&out).Error
	return out, err
}

func (r *claimRepo) ListByKeys(
	ctx context.Context, tenantID uint64, kbID string, keys []string,
	excludeSourceID, excludeKnowledgeID string,
) ([]*types.Claim, error) {
	if len(keys) == 0 {
		return nil, nil
	}
	q := r.db.WithContext(ctx).
		Where("tenant_id = ? AND knowledge_base_id = ? AND claim_key IN ?", tenantID, kbID, keys)
	if excludeSourceID != "" {
		q = q.Where("source_id != ?", excludeSourceID)
	}
	if excludeKnowledgeID != "" {
		q = q.Where("knowledge_id != ?", excludeKnowledgeID)
	}
	var out []*types.Claim
	err := q.Find(&out).Error
	return out, err
}

func (r *claimRepo) CountBySource(ctx context.Context, sourceType, sourceID string) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&types.Claim{}).
		Where("source_type = ? AND source_id = ?", sourceType, sourceID).
		Count(&count).Error
	return count, err
}
