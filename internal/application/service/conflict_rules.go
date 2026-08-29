package service

// conflict_rules.go is C2-A's conservative deterministic verifier. It runs
// only after C1 candidate generation and before an LLM call. The rules are
// intentionally narrow: an uncertain qualifier, relation, text value, date,
// or fallback candidate always stays on the LLM path rather than trading
// recall/precision for a cheap but unsafe decision.

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/Tencent/WeKnora/internal/types"
)

type conflictRuleAction string

const (
	conflictRuleNeedsLLM      conflictRuleAction = "needs_llm"
	conflictRuleNoConflict    conflictRuleAction = "no_conflict"
	conflictRuleDirectConflict conflictRuleAction = "direct_conflict"
)

const (
	conflictRuleUnsupportedCandidate = "unsupported_candidate"
	conflictRuleSameNormalizedValue  = "same_normalized_value"
	conflictRuleNumericMismatch      = "numeric_value_mismatch"
	conflictRuleQualifierDisjoint    = "qualifier_disjoint"
	conflictRuleAmbiguous            = "ambiguous_claim_relation"
)

// conflictRuleDecision describes the outcome for one C1 candidate chunk pair.
// DirectConflict pairs bypass the LLM but retain a deterministic reason in the
// existing knowledge_conflicts row; NoConflict pairs are dropped; NeedsLLM
// pairs retain C1's evidence-conditioned LLM adjudication path.
type conflictRuleDecision struct {
	Action       conflictRuleAction
	RuleID       string
	ConflictType string
	Reason       string
}

type conflictCascadeRuleStats struct {
	NoConflict     int
	DirectConflict int
	NeedsLLM       int
}

// applyConflictRules runs C2-A rules over candidate pairs. It never mutates an
// input pair; direct conflicts are copied and annotated with the rule verdict.
func applyConflictRules(pairs []conflictPair) (direct, unresolved []conflictPair, stats conflictCascadeRuleStats) {
	direct = make([]conflictPair, 0, len(pairs))
	unresolved = make([]conflictPair, 0, len(pairs))
	for _, pair := range pairs {
		decision := decideConflictPairByRules(pair)
		switch decision.Action {
		case conflictRuleNoConflict:
			stats.NoConflict++
		case conflictRuleDirectConflict:
			stats.DirectConflict++
			pair.ConflictType = decision.ConflictType
			pair.Reason = decision.Reason
			direct = append(direct, pair)
		default:
			stats.NeedsLLM++
			unresolved = append(unresolved, pair)
		}
	}
	return direct, unresolved, stats
}

// decideConflictPairByRules is intentionally all-or-nothing at chunk-pair
// level. A pair can carry several claim evidences. One high-confidence numeric
// contradiction is sufficient to emit a direct conflict; otherwise every
// compared claim must be proven same-value or clearly scope-disjoint before a
// pair is dropped. Any residual ambiguity goes to the LLM.
func decideConflictPairByRules(pair conflictPair) conflictRuleDecision {
	if pair.ClaimKeyHit == "" || len(pair.NewClaimEvidence) == 0 || len(pair.ExistClaimEvidence) == 0 {
		return conflictRuleDecision{Action: conflictRuleNeedsLLM, RuleID: conflictRuleUnsupportedCandidate}
	}

	compared := false
	ambiguous := false
	disjoint := false
	for _, newer := range pair.NewClaimEvidence {
		for _, older := range pair.ExistClaimEvidence {
			if newer.ClaimKey == "" || newer.ClaimKey != older.ClaimKey {
				continue
			}
			compared = true

			// Identical normalized values cannot constitute a value conflict,
			// irrespective of surface wording or compatible qualifiers.
			if newer.ValueNorm != "" && newer.ValueNorm == older.ValueNorm {
				continue
			}

			if qualifiersClearlyDisjoint(newer.Qualifiers, older.Qualifiers) {
				disjoint = true
				continue
			}

			if newer.ValueKind == types.ClaimValueKindNumber &&
				older.ValueKind == types.ClaimValueKindNumber &&
				numericUnitsCompatible(newer.ValueNorm, older.ValueNorm) &&
				qualifiersEquivalent(newer.Qualifiers, older.Qualifiers) {
				return conflictRuleDecision{
					Action:       conflictRuleDirectConflict,
					RuleID:       conflictRuleNumericMismatch,
					ConflictType: types.ConflictTypeFactContradiction,
					Reason: fmt.Sprintf(
						"[rule:%s] 同一声明槽位 %q 的归一数值互斥：%s vs %s。",
						conflictRuleNumericMismatch, newer.ClaimKey, newer.ValueNorm, older.ValueNorm,
					),
				}
			}

			// Date/version, enum/text relation, incompatible units and unequal
			// qualifiers all require semantic adjudication (C2 LLM/C3 later).
			ambiguous = true
		}
	}

	if !compared {
		return conflictRuleDecision{Action: conflictRuleNeedsLLM, RuleID: conflictRuleUnsupportedCandidate}
	}
	if ambiguous {
		return conflictRuleDecision{Action: conflictRuleNeedsLLM, RuleID: conflictRuleAmbiguous}
	}
	// At least one matching key was compared and every comparable assertion was
	// same-value or provably scope-disjoint.
	ruleID := conflictRuleSameNormalizedValue
	if disjoint {
		ruleID = conflictRuleQualifierDisjoint
	}
	return conflictRuleDecision{Action: conflictRuleNoConflict, RuleID: ruleID}
}

// numericUnitsCompatible allows a direct numeric decision only when the
// normalizer preserved equal units (or both represent a unitless number). An
// absent unit on just one side is ambiguous: "100" may be a count or currency.
func numericUnitsCompatible(left, right string) bool {
	leftUnit, leftOK := claimValueUnit(left)
	rightUnit, rightOK := claimValueUnit(right)
	return leftOK && rightOK && leftUnit == rightUnit
}

func claimValueUnit(valueNorm string) (string, bool) {
	parts := strings.SplitN(valueNorm, "|", 2)
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
		return "", false
	}
	return strings.TrimSpace(parts[1]), true
}

// qualifiersEquivalent accepts empty, null and {} as the same empty qualifier
// set. JSON map comparison is key-order independent. Invalid JSON is never
// equivalent because it should be sent to the LLM rather than hidden.
func qualifiersEquivalent(left, right string) bool {
	leftMap, leftOK := parseClaimQualifiers(left)
	rightMap, rightOK := parseClaimQualifiers(right)
	return leftOK && rightOK && reflect.DeepEqual(leftMap, rightMap)
}

// qualifiersClearlyDisjoint only recognizes a tiny set of unquestionably
// mutually-exclusive scopes. It deliberately does NOT treat arbitrary unequal
// scopes or dates as disjoint; those remain semantic LLM/C3 work.
func qualifiersClearlyDisjoint(left, right string) bool {
	leftMap, leftOK := parseClaimQualifiers(left)
	rightMap, rightOK := parseClaimQualifiers(right)
	if !leftOK || !rightOK {
		return false
	}
	leftScope, leftHasScope := qualifierString(leftMap, "scope")
	rightScope, rightHasScope := qualifierString(rightMap, "scope")
	if !leftHasScope || !rightHasScope || !isMutuallyExclusiveScope(leftScope, rightScope) {
		return false
	}
	delete(leftMap, "scope")
	delete(rightMap, "scope")
	return reflect.DeepEqual(leftMap, rightMap)
}

func parseClaimQualifiers(raw string) (map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return map[string]any{}, true
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return nil, false
	}
	return values, true
}

func qualifierString(values map[string]any, key string) (string, bool) {
	value, ok := values[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	text = strings.TrimSpace(strings.ToLower(text))
	return text, ok && text != ""
}

func isMutuallyExclusiveScope(left, right string) bool {
	left = canonicalClaimScope(left)
	right = canonicalClaimScope(right)
	return (left == "domestic" && right == "international") ||
		(left == "international" && right == "domestic") ||
		(left == "weekday" && right == "holiday") ||
		(left == "holiday" && right == "weekday")
}

func canonicalClaimScope(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	switch value {
	case "国内", "境内", "domestic":
		return "domestic"
	case "国际", "境外", "海外", "international", "overseas":
		return "international"
	case "工作日", "weekday", "weekdays":
		return "weekday"
	case "节假日", "假日", "holiday", "holidays":
		return "holiday"
	default:
		return value
	}
}
