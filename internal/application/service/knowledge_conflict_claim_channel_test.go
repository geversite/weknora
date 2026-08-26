package service

import (
	"strings"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestClaimChannelCoveredChunksRequiresEveryClaimToMatch(t *testing.T) {
	exact := &types.Claim{ClaimKey: "exact"}
	unmatched := &types.Claim{ClaimKey: "unmatched"}

	covered := claimChannelCoveredChunks(
		map[string][]*types.Claim{
			"all-exact": []*types.Claim{exact},
			// A chunk can contain both an exact P5-style fact and an unmatched
			// P3-style synonym fact. It must enter fallback so the latter is not
			// silently lost.
			"mixed":          []*types.Claim{exact, unmatched},
			"only-unmatched": []*types.Claim{unmatched},
		},
		map[string]bool{"exact": true},
	)

	if !covered["all-exact"] {
		t.Fatal("all-exact chunk should be covered by claim channel")
	}
	if covered["mixed"] {
		t.Fatal("mixed chunk must remain uncovered so semantic fallback runs")
	}
	if covered["only-unmatched"] {
		t.Fatal("unmatched chunk must remain uncovered so semantic fallback runs")
	}
}

func TestConflictCandidateChannelCounts(t *testing.T) {
	claimPairs, fallbackPairs := conflictCandidateChannelCounts([]conflictPair{
		{ClaimKeyHit: "工业级星晶供应实体"},
		{},
		{ClaimKeyHit: "国际漫游通讯补贴每天标准"},
	})
	if claimPairs != 2 || fallbackPairs != 1 {
		t.Fatalf("channel counts = (%d claim, %d fallback), want (2, 1)", claimPairs, fallbackPairs)
	}
}

func TestDedupeConflictCandidatePairsPrefersClaimProvenance(t *testing.T) {
	newChunk := &types.Chunk{ID: "new", KnowledgeID: "new-doc"}
	existingChunk := &types.Chunk{ID: "old", KnowledgeID: "old-doc"}

	pairs := dedupeConflictCandidatePairs([]conflictPair{
		{NewChunk: newChunk, ExistingChunk: existingChunk}, // fallback first
		{
			NewChunk: newChunk, ExistingChunk: existingChunk,
			ClaimKeyHit: "市内交通费每日上限",
			NewClaimIDs: []string{"new-claim"}, ExistClaimIDs: []string{"old-claim"},
		},
	})

	if len(pairs) != 1 {
		t.Fatalf("dedupeConflictCandidatePairs() len = %d, want 1", len(pairs))
	}
	if pairs[0].ClaimKeyHit != "市内交通费每日上限" {
		t.Fatalf("ClaimKeyHit = %q, want claim provenance", pairs[0].ClaimKeyHit)
	}
	if len(pairs[0].NewClaimIDs) != 1 || len(pairs[0].ExistClaimIDs) != 1 {
		t.Fatalf("claim IDs were not retained: %+v", pairs[0])
	}
}

func TestBuildConflictAdjudicationPromptIncludesClaimEvidence(t *testing.T) {
	pair := conflictPair{
		NewChunk:      &types.Chunk{Content: "新文档原文"},
		ExistingChunk: &types.Chunk{Content: "旧文档原文"},
		NewTitle:      "new",
		ExistingTitle: "old",
		ClaimKeyHit:   "工业级星晶供应实体",
		NewClaimEvidence: []claimEvidence{{
			ID: "new-claim", Subject: "工业级星晶", Predicate: "供应实体",
			Value: "天穹财团与新弦工业两家实体", ValueNorm: "天穹财团与新弦工业两家实体", Qualifiers: `{"time":"目前"}`,
		}},
		ExistClaimEvidence: []claimEvidence{{
			ID: "old-claim", Subject: "工业级星晶", Predicate: "供应实体",
			Value: "天穹财团", ValueNorm: "天穹财团", Qualifiers: `{"time":"目前"}`,
		}},
	}

	prompt := buildConflictAdjudicationPrompt(pair)
	for _, want := range []string{
		"候选声明证据", "工业级星晶供应实体", "天穹财团与新弦工业两家实体", "新文档原文", "旧文档原文",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("claim-conditioned prompt missing %q", want)
		}
	}
}

func TestBuildConflictAdjudicationPromptKeepsFallbackEvidenceFree(t *testing.T) {
	prompt := buildConflictAdjudicationPrompt(conflictPair{
		NewChunk:      &types.Chunk{Content: "新文档原文"},
		ExistingChunk: &types.Chunk{Content: "旧文档原文"},
	})
	if strings.Contains(prompt, "候选声明证据（本次配对") {
		t.Fatal("fallback prompt must not invent claim evidence")
	}
}

func TestClaimExtractPromptV2RetainsCanonicalSlotContract(t *testing.T) {
	for _, want := range []string{
		"不要把时间、地点、适用范围、条件、情态词塞入 subject",
		"以资源/制度为 subject，以供应实体/生产实体/归属为 predicate",
		"国际漫游通讯补贴",
		"工业级星晶, predicate=供应实体",
	} {
		if !strings.Contains(claimExtractSystemPrompt, want) {
			t.Fatalf("claimExtractSystemPrompt is missing canonical-slot contract %q", want)
		}
	}
}
