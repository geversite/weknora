package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveChatModelIDBuiltinAgentWithoutModelFallsBack(t *testing.T) {
	// Builtin agents (wiki-fixer / wiki-researcher) are programmatically
	// invoked and carry no tenant-owned model_id. They must NOT hard-fail;
	// resolution falls through to the session / KB chat model instead.
	svc := &sessionService{
		modelService: &stubModelService{modelsByID: map[string]*types.Model{}},
	}
	req := &types.QARequest{
		Session: &types.Session{},
		CustomAgent: &types.CustomAgent{
			ID:        types.BuiltinWikiFixerID,
			IsBuiltin: true,
		},
	}

	modelID, err := svc.resolveChatModelID(context.Background(), req, nil, nil)

	// The "please set model_id on agent" gate must be bypassed for builtins.
	require.NotContains(t, err.Error(), "please set model_id on agent")
	assert.Empty(t, modelID)
}

func TestResolveChatModelIDBuiltinAgentWithExplicitModelUsesIt(t *testing.T) {
	svc := &sessionService{
		modelService: &stubModelService{
			modelsByID: map[string]*types.Model{
				"builtin-wiki-chat": {ID: "builtin-wiki-chat", Type: types.ModelTypeKnowledgeQA},
			},
		},
	}
	req := &types.QARequest{
		Session: &types.Session{},
		CustomAgent: &types.CustomAgent{
			ID:        types.BuiltinWikiFixerID,
			IsBuiltin: true,
			Config:    types.CustomAgentConfig{ModelID: "builtin-wiki-chat"},
		},
	}

	modelID, err := svc.resolveChatModelID(context.Background(), req, nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "builtin-wiki-chat", modelID)
}

func TestResolveChatModelIDRequiresConfiguredAgentModel(t *testing.T) {
	svc := &sessionService{
		modelService: &stubModelService{
			modelsByID: map[string]*types.Model{
				"builtin-chat": {
					ID:   "builtin-chat",
					Type: types.ModelTypeKnowledgeQA,
				},
			},
		},
	}
	req := &types.QARequest{
		Session: &types.Session{},
		CustomAgent: &types.CustomAgent{
			ID: "agent-1",
		},
		// Even a valid request-level model must not hide incomplete agent config.
		SummaryModelID: "builtin-chat",
	}

	modelID, err := svc.resolveChatModelID(context.Background(), req, nil, nil)

	require.Error(t, err)
	assert.Empty(t, modelID)
	assert.Contains(t, err.Error(), "model_id")
}

func TestResolveChatModelIDRejectsUnavailableConfiguredAgentModel(t *testing.T) {
	svc := &sessionService{
		modelService: &stubModelService{modelsByID: map[string]*types.Model{}},
	}
	req := &types.QARequest{
		Session: &types.Session{},
		CustomAgent: &types.CustomAgent{
			ID: "agent-1",
			Config: types.CustomAgentConfig{
				ModelID: "deleted-model",
			},
		},
	}

	modelID, err := svc.resolveChatModelID(context.Background(), req, nil, nil)

	require.Error(t, err)
	assert.Empty(t, modelID)
	assert.Contains(t, err.Error(), "unavailable")
}

func TestResolveChatModelIDUsesValidConfiguredAgentModel(t *testing.T) {
	svc := &sessionService{
		modelService: &stubModelService{
			modelsByID: map[string]*types.Model{
				"agent-chat": {
					ID:   "agent-chat",
					Type: types.ModelTypeKnowledgeQA,
				},
			},
		},
	}
	req := &types.QARequest{
		Session: &types.Session{},
		CustomAgent: &types.CustomAgent{
			ID: "agent-1",
			Config: types.CustomAgentConfig{
				ModelID: "agent-chat",
			},
		},
	}

	modelID, err := svc.resolveChatModelID(context.Background(), req, nil, nil)

	require.NoError(t, err)
	assert.Equal(t, "agent-chat", modelID)
}

func TestResolveChatModelIDRejectsNonChatSummaryModelOverride(t *testing.T) {
	svc := &sessionService{
		modelService: &stubModelService{
			modelsByID: map[string]*types.Model{
				"agent-chat": {
					ID:   "agent-chat",
					Type: types.ModelTypeKnowledgeQA,
				},
				"rerank-only": {
					ID:   "rerank-only",
					Type: types.ModelTypeRerank,
				},
			},
		},
	}
	req := &types.QARequest{
		Session: &types.Session{},
		CustomAgent: &types.CustomAgent{
			ID: "agent-1",
			Config: types.CustomAgentConfig{
				ModelID: "agent-chat",
			},
		},
		SummaryModelID: "rerank-only",
	}

	modelID, err := svc.resolveChatModelID(context.Background(), req, nil, nil)

	require.NoError(t, err)
	assert.Equal(t, "agent-chat", modelID)
}

func TestResolveChatModelIDUsesValidSummaryModelOverride(t *testing.T) {
	svc := &sessionService{
		modelService: &stubModelService{
			modelsByID: map[string]*types.Model{
				"agent-chat": {
					ID:   "agent-chat",
					Type: types.ModelTypeKnowledgeQA,
				},
				"override-chat": {
					ID:   "override-chat",
					Type: types.ModelTypeKnowledgeQA,
				},
			},
		},
	}
	req := &types.QARequest{
		Session: &types.Session{},
		CustomAgent: &types.CustomAgent{
			ID: "agent-1",
			Config: types.CustomAgentConfig{
				ModelID: "agent-chat",
			},
		},
		SummaryModelID: "override-chat",
	}

	modelID, err := svc.resolveChatModelID(context.Background(), req, nil, nil)

	require.NoError(t, err)
	assert.Equal(t, "override-chat", modelID)
}
