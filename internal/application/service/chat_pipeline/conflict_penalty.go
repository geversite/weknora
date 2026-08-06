package chatpipeline

import (
	"context"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

// conflictPenaltyFactor is the multiplicative score penalty applied to a chunk
// participating in an unresolved content conflict. Applied before reranking so
// the divergent source ranks lower than consistent alternatives, while the
// chunk itself is still tagged (ConflictPending) for answer-time divergence
// surfacing.
const conflictPenaltyFactor = 0.6

// PluginConflictPenalty demotes and tags chunks that participate in unresolved
// (pending) file-level content conflicts during the rerank stage (M3).
type PluginConflictPenalty struct {
	conflictRepo interfaces.KnowledgeConflictRepository
}

// NewPluginConflictPenalty creates and registers the conflict penalty plugin.
func NewPluginConflictPenalty(eventManager *EventManager, conflictRepo interfaces.KnowledgeConflictRepository) *PluginConflictPenalty {
	res := &PluginConflictPenalty{
		conflictRepo: conflictRepo,
	}
	eventManager.Register(res)
	return res
}

// ActivationEvents returns the event types this plugin handles.
func (p *PluginConflictPenalty) ActivationEvents() []types.EventType {
	return []types.EventType{types.CHUNK_RERANK}
}

// OnEvent marks chunks that belong to a pending conflict and demotes their
// score so they rank lower in the reranked output.
func (p *PluginConflictPenalty) OnEvent(
	ctx context.Context,
	eventType types.EventType,
	chatManage *types.ChatManage,
	next func() *PluginError,
) *PluginError {
	if p.conflictRepo == nil || len(chatManage.SearchResult) == 0 {
		return next()
	}
	chunkIDs := make([]string, 0, len(chatManage.SearchResult))
	for _, r := range chatManage.SearchResult {
		if r != nil && r.ID != "" {
			chunkIDs = append(chunkIDs, r.ID)
		}
	}
	if len(chunkIDs) == 0 {
		return next()
	}
	conflicts, err := p.conflictRepo.ListPendingByChunkIDs(ctx, chunkIDs)
	if err != nil {
		pipelineWarn(ctx, "ConflictPenalty", "query_pending_failed", map[string]interface{}{
			"error": err.Error(),
		})
		return next()
	}
	pendingChunkIDs := make(map[string]struct{}, len(conflicts))
	for _, c := range conflicts {
		if c == nil {
			continue
		}
		pendingChunkIDs[c.ChunkIDA] = struct{}{}
		pendingChunkIDs[c.ChunkIDB] = struct{}{}
	}
	if len(pendingChunkIDs) == 0 {
		return next()
	}
	penalized := 0
	for _, r := range chatManage.SearchResult {
		if r == nil {
			continue
		}
		if _, ok := pendingChunkIDs[r.ID]; ok {
			r.ConflictPending = true
			r.Score = r.Score * conflictPenaltyFactor
			penalized++
		}
	}
	pipelineInfo(ctx, "ConflictPenalty", "applied", map[string]interface{}{
		"penalized_chunks": penalized,
	})
	return next()
}
