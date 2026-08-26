package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/hibiken/asynq"
)

type claimFollowUpTaskRecorder struct {
	tasks []*asynq.Task
}

func (r *claimFollowUpTaskRecorder) Enqueue(task *asynq.Task, _ ...asynq.Option) (*asynq.TaskInfo, error) {
	r.tasks = append(r.tasks, task)
	return &asynq.TaskInfo{ID: "recorded", Type: task.Type()}, nil
}

func TestClaimExtractEnqueuesConflictOnlyForKnowledgeSource(t *testing.T) {
	recorder := &claimFollowUpTaskRecorder{}
	svc := &claimExtractService{taskEnqueuer: recorder}
	payload := types.ClaimExtractPayload{
		TenantID:        42,
		KnowledgeBaseID: "kb-1",
		KnowledgeID:     "knowledge-1",
		SourceType:      types.ClaimSourceChunk,
	}

	svc.enqueueConflictDetectAfterClaims(context.Background(), payload, "claims_ready")

	if len(recorder.tasks) != 1 {
		t.Fatalf("enqueued tasks = %d, want 1", len(recorder.tasks))
	}
	task := recorder.tasks[0]
	if task.Type() != types.TypeConflictDetect {
		t.Fatalf("task type = %q, want %q", task.Type(), types.TypeConflictDetect)
	}
	var got types.ConflictDetectPayload
	if err := json.Unmarshal(task.Payload(), &got); err != nil {
		t.Fatalf("unmarshal conflict payload: %v", err)
	}
	if got.TenantID != payload.TenantID || got.KnowledgeBaseID != payload.KnowledgeBaseID || got.KnowledgeID != payload.KnowledgeID {
		t.Fatalf("follow-up payload = %+v, want tenant/kb/knowledge from %+v", got, payload)
	}
}

func TestClaimExtractDoesNotEnqueueConflictWithoutKnowledgeID(t *testing.T) {
	recorder := &claimFollowUpTaskRecorder{}
	svc := &claimExtractService{taskEnqueuer: recorder}

	svc.enqueueConflictDetectAfterClaims(context.Background(), types.ClaimExtractPayload{
		TenantID:        42,
		KnowledgeBaseID: "kb-1",
		SourceType:      types.ClaimSourceWikiPage,
	}, "wiki_source")

	if len(recorder.tasks) != 0 {
		t.Fatalf("enqueued tasks = %d, want 0", len(recorder.tasks))
	}
}
