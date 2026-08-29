package service

import (
	"context"
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func ruleEvidence(key, valueNorm, valueKind, qualifiers string) claimEvidence {
	return claimEvidence{
		ClaimKey: key, Subject: "主体", Predicate: "属性", Value: valueNorm,
		ValueNorm: valueNorm, ValueKind: valueKind, Qualifiers: qualifiers,
	}
}

func rulePair(newer, older claimEvidence) conflictPair {
	return conflictPair{
		ClaimKeyHit:       newer.ClaimKey,
		NewClaimEvidence:  []claimEvidence{newer},
		ExistClaimEvidence: []claimEvidence{older},
	}
}

func TestConflictRulesDirectNumericMismatchWithEquivalentQualifiers(t *testing.T) {
	pair := rulePair(
		ruleEvidence("国际漫游通讯补贴每天标准", "80|元", types.ClaimValueKindNumber, `{"scope":"国际出差期间"}`),
		ruleEvidence("国际漫游通讯补贴每天标准", "100|元", types.ClaimValueKindNumber, `{"scope":"国际出差期间"}`),
	)

	decision := decideConflictPairByRules(pair)
	if decision.Action != conflictRuleDirectConflict {
		t.Fatalf("Action = %q, want %q (%+v)", decision.Action, conflictRuleDirectConflict, decision)
	}
	if decision.RuleID != conflictRuleNumericMismatch || decision.ConflictType != types.ConflictTypeFactContradiction {
		t.Fatalf("unexpected direct decision: %+v", decision)
	}
}

func TestConflictRulesDropSameNormalizedValue(t *testing.T) {
	pair := rulePair(
		ruleEvidence("国内餐费补贴每天标准", "150|元", types.ClaimValueKindNumber, "{}"),
		ruleEvidence("国内餐费补贴每天标准", "150|元", types.ClaimValueKindNumber, `{"scope":"国内出差"}`),
	)

	decision := decideConflictPairByRules(pair)
	if decision.Action != conflictRuleNoConflict || decision.RuleID != conflictRuleSameNormalizedValue {
		t.Fatalf("decision = %+v, want same-value no-conflict", decision)
	}
}

func TestConflictRulesKeepDateAndTextForLLM(t *testing.T) {
	datePair := rulePair(
		ruleEvidence("幽能引擎原型机测试时间", "2153", types.ClaimValueKindDate, `{"status":"已推迟"}`),
		ruleEvidence("幽能引擎原型机测试时间", "2150", types.ClaimValueKindDate, "{}"),
	)
	if got := decideConflictPairByRules(datePair); got.Action != conflictRuleNeedsLLM {
		t.Fatalf("date decision = %+v, want needs_llm", got)
	}

	textPair := rulePair(
		ruleEvidence("工业级星晶供应实体", "天穹财团与新弦工业", types.ClaimValueKindText, `{"time":"目前"}`),
		ruleEvidence("工业级星晶供应实体", "天穹财团", types.ClaimValueKindText, `{"time":"目前"}`),
	)
	if got := decideConflictPairByRules(textPair); got.Action != conflictRuleNeedsLLM {
		t.Fatalf("text decision = %+v, want needs_llm", got)
	}
}

func TestConflictRulesDropClearlyDisjointDomesticInternationalScope(t *testing.T) {
	pair := rulePair(
		ruleEvidence("住宿费每日上限", "650|元", types.ClaimValueKindNumber, `{"scope":"国内"}`),
		ruleEvidence("住宿费每日上限", "1200|元", types.ClaimValueKindNumber, `{"scope":"国际"}`),
	)
	decision := decideConflictPairByRules(pair)
	if decision.Action != conflictRuleNoConflict || decision.RuleID != conflictRuleQualifierDisjoint {
		t.Fatalf("decision = %+v, want scope-disjoint no-conflict", decision)
	}
}

func TestConflictRulesDoNotDirectJudgeUnitMismatchOrFallback(t *testing.T) {
	unitMismatch := rulePair(
		ruleEvidence("补贴标准", "100|元", types.ClaimValueKindNumber, "{}"),
		ruleEvidence("补贴标准", "100|天", types.ClaimValueKindNumber, "{}"),
	)
	if got := decideConflictPairByRules(unitMismatch); got.Action != conflictRuleNeedsLLM {
		t.Fatalf("unit mismatch decision = %+v, want needs_llm", got)
	}

	fallback := conflictPair{
		NewClaimEvidence: []claimEvidence{ruleEvidence("x", "1|元", types.ClaimValueKindNumber, "{}")},
		ExistClaimEvidence: []claimEvidence{ruleEvidence("x", "2|元", types.ClaimValueKindNumber, "{}")},
	}
	if got := decideConflictPairByRules(fallback); got.Action != conflictRuleNeedsLLM || got.RuleID != conflictRuleUnsupportedCandidate {
		t.Fatalf("fallback decision = %+v, want unsupported needs_llm", got)
	}
}

func TestFineAdjudicateRulesBypassesChatForDirectNumericConflict(t *testing.T) {
	service := &KnowledgeConflictService{}
	kb := &types.KnowledgeBase{
		IndexingStrategy: types.IndexingStrategy{ConflictCascadeMode: types.ConflictCascadeModeRules},
	}
	pair := rulePair(
		ruleEvidence("补贴每天标准", "80|元", types.ClaimValueKindNumber, "{}"),
		ruleEvidence("补贴每天标准", "100|元", types.ClaimValueKindNumber, "{}"),
	)

	got, stats := service.fineAdjudicate(context.Background(), kb, []conflictPair{pair})
	if stats.RuleDirectConflict != 1 || stats.LLMPairCount != 0 {
		t.Fatalf("cascade stats = %+v, want one direct rule and no LLM", stats)
	}
	if len(got) != 1 {
		t.Fatalf("fineAdjudicate direct result len = %d, want 1", len(got))
	}
	if got[0].ConflictType != types.ConflictTypeFactContradiction {
		t.Fatalf("ConflictType = %q, want %q", got[0].ConflictType, types.ConflictTypeFactContradiction)
	}
	if got[0].Reason == "" {
		t.Fatal("direct rule result must retain deterministic reason")
	}
}

func TestApplyConflictRulesAggregatesStats(t *testing.T) {
	directPair := rulePair(
		ruleEvidence("金额", "80|元", types.ClaimValueKindNumber, "{}"),
		ruleEvidence("金额", "100|元", types.ClaimValueKindNumber, "{}"),
	)
	samePair := rulePair(
		ruleEvidence("金额", "80|元", types.ClaimValueKindNumber, "{}"),
		ruleEvidence("金额", "80|元", types.ClaimValueKindNumber, "{}"),
	)
	llmPair := rulePair(
		ruleEvidence("时间", "2150", types.ClaimValueKindDate, "{}"),
		ruleEvidence("时间", "2153", types.ClaimValueKindDate, "{}"),
	)

	direct, unresolved, stats := applyConflictRules([]conflictPair{directPair, samePair, llmPair})
	if len(direct) != 1 || len(unresolved) != 1 {
		t.Fatalf("direct=%d unresolved=%d, want 1/1", len(direct), len(unresolved))
	}
	if stats.DirectConflict != 1 || stats.NoConflict != 1 || stats.NeedsLLM != 1 {
		t.Fatalf("stats = %+v, want 1/1/1", stats)
	}
	if direct[0].ConflictType != types.ConflictTypeFactContradiction {
		t.Fatalf("direct pair type = %q", direct[0].ConflictType)
	}
}
