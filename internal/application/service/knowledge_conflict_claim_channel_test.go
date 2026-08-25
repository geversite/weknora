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
