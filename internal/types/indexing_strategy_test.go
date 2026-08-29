package types

import "testing"

func TestEffectiveConflictCascadeMode(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want string
	}{
		{name: "empty defaults legacy", mode: "", want: ConflictCascadeModeLegacy},
		{name: "legacy stays legacy", mode: ConflictCascadeModeLegacy, want: ConflictCascadeModeLegacy},
		{name: "rules", mode: ConflictCascadeModeRules, want: ConflictCascadeModeRules},
		{name: "rules batch", mode: ConflictCascadeModeRulesBatch, want: ConflictCascadeModeRulesBatch},
		{name: "unknown safely legacy", mode: "typo", want: ConflictCascadeModeLegacy},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := (IndexingStrategy{ConflictCascadeMode: tt.mode}).EffectiveConflictCascadeMode()
			if got != tt.want {
				t.Fatalf("EffectiveConflictCascadeMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestKnowledgeBaseEffectiveConflictCascadeMode(t *testing.T) {
	var nilKB *KnowledgeBase
	if got := nilKB.EffectiveConflictCascadeMode(); got != ConflictCascadeModeLegacy {
		t.Fatalf("nil KB mode = %q, want %q", got, ConflictCascadeModeLegacy)
	}
	kb := &KnowledgeBase{IndexingStrategy: IndexingStrategy{ConflictCascadeMode: ConflictCascadeModeRules}}
	if got := kb.EffectiveConflictCascadeMode(); got != ConflictCascadeModeRules {
		t.Fatalf("KB mode = %q, want %q", got, ConflictCascadeModeRules)
	}
}
