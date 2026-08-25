package service

// Tests for the C1 claim normalization rules. The case matrix mirrors the
// calibration corpus in testdata/claims_eval (evaluate.py v1.1) — keep both
// in sync when rules change.

import (
	"testing"

	"github.com/Tencent/WeKnora/internal/types"
)

func TestNormalizeClaimText(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// NFKC + lowercase + whitespace fold
		{"  星晶反应堆 SQ-7  ", "星晶反应堆sq-7"},
		// parenthetical annotation removal (CN + EN parens)
		{"天穹财团 (SkyVault Consortium) 董事会", "天穹财团董事会"},
		{"星晶（StarQuartz）", "星晶"},
		// wrap punct strip
		{"《差旅与报销管理制度》", "差旅与报销管理制度"},
		// latin/digit internal space kept, CJK internal space dropped
		{"sq 7", "sq 7"},
		{"星晶 反应堆", "星晶反应堆"},
	}
	for _, c := range cases {
		if got := NormalizeClaimText(c.in); got != c.want {
			t.Errorf("NormalizeClaimText(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizeClaimTextValue(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		// particle 的
		{"幽能引擎的地面测试用反应堆", "幽能引擎地面测试用反应堆"},
		// affirmative modal prefix stripped
		{"必须部署谐波屏蔽罩", "部署谐波屏蔽罩"},
		// trailing count suffix stripped
		{"天穹财团与新弦工业两家", "天穹财团与新弦工业"},
		// connectives and list punctuation removed
		{"室温下超导、特定频段下自发振荡", "室温下超导特定频段下自发振荡"},
		{"室温下超导并在特定频段下自发振荡", "室温下超导特定频段下自发振荡"},
		// parenthetical removal cascades from base norm
		{"仅天穹财团（唯一）", "仅天穹财团"},
		// negations must survive (never strip 不/不得)
		{"不得少于 12 小时", "不得少于12小时"},
	}
	for _, c := range cases {
		if got := NormalizeClaimTextValue(c.in); got != c.want {
			t.Errorf("NormalizeClaimTextValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestNormalizePredicateAffixes(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"上岗资质", "资质"},
		{"资质要求", "资质"},
		{"处理方式", "处理"},
		{"申请条件", "条件"},
		{"完成时限", "时限"},
		{"项目目标", "目标"},
		{"年度天数", "天数"},
		{"原型机测试时间节点", "原型机测试时间"},
		// too short to strip (guard: must keep >1 rune after strip)
		{"要求", "要求"},
		{"方式", "方式"},
	}
	for _, c := range cases {
		if got := NormalizePredicate(c.in); got != c.want {
			t.Errorf("NormalizePredicate(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestFusedClaimKeyBoundaryInsensitive(t *testing.T) {
	// P1 calibration case: subject/predicate boundary drift fuses equal.
	a := FusedClaimKey("幽能引擎", "原型机测试时间")
	b := FusedClaimKey("幽能引擎原型机", "测试时间")
	if a != b {
		t.Errorf("fused keys differ: %q vs %q", a, b)
	}
	// Distinct facts stay distinct.
	c := FusedClaimKey("报销申请", "提交时限")
	d := FusedClaimKey("报销单", "提交时限")
	if c == d {
		t.Errorf("distinct subjects fused unexpectedly: %q", c)
	}
}

func TestNormalizeClaimValue(t *testing.T) {
	cases := []struct {
		in, hint, wantNorm, wantKind string
	}{
		// numbers with unit canonicalization
		{"650 元", "", "650|元", types.ClaimValueKindNumber},
		{"费用发生后 30 个自然日内", "", "30|天", types.ClaimValueKindNumber},
		{"费用发生后 45 天内", "", "45|天", types.ClaimValueKindNumber},
		{"7 个工作日内", "", "7|工作日", types.ClaimValueKindNumber},
		{"12.5 太瓦", "", "12.5|太瓦", types.ClaimValueKindNumber},
		// percent
		{"不超过 15%", "", "0.15|", types.ClaimValueKindNumber},
		{"不低于 99.2%", "", "0.992|", types.ClaimValueKindNumber},
		// 万 multiplier
		{"3万元", "", "30000|元", types.ClaimValueKindNumber},
		// dates
		{"2148年3月1日", "", "2148-03-01", types.ClaimValueKindDate},
		{"2150 年前", "", "2150", types.ClaimValueKindDate},
		{"推迟至 2153 年", "", "2153", types.ClaimValueKindDate},
		// clock time
		{"7:30", "", "07:30", types.ClaimValueKindNumber},
		// range (NFKC canonicalizes ℃ (U+2103) into °C → lowercased °c —
		// desired: "850℃" and "850 °C" spellings normalize identically)
		{"-40℃ 至 850℃", "", "-40~850|°c", types.ClaimValueKindNumber},
		// Chinese numeral count
		{"两个", "", "2|个", types.ClaimValueKindNumber},
		{"2 名", "", "2|个", types.ClaimValueKindNumber},
		// text fallback with deep normalization
		{"解除劳动合同", "", "解除劳动合同", types.ClaimValueKindText},
		{"仅天穹财团（唯一）", "enum", "仅天穹财团", types.ClaimValueKindEnum},
	}
	for _, c := range cases {
		gotNorm, gotKind := NormalizeClaimValue(c.in, c.hint)
		if gotNorm != c.wantNorm || gotKind != c.wantKind {
			t.Errorf("NormalizeClaimValue(%q,%q) = (%q,%q), want (%q,%q)",
				c.in, c.hint, gotNorm, gotKind, c.wantNorm, c.wantKind)
		}
	}
}

func TestNormalizeClaimValueConflictPairs(t *testing.T) {
	// The five planted contradiction pairs from the calibration corpus must
	// keep DIFFERENT value norms; the agreement control must keep EQUAL ones.
	type pair struct {
		a, b      string
		wantEqual bool
	}
	pairs := []pair{
		{"2150 年前", "推迟至 2153 年", false},             // P1
		{"仅天穹财团（唯一）", "天穹财团与新弦工业两家", false}, // P2
		{"费用发生后 30 个自然日内", "费用发生后 45 天内", false}, // P3
		{"80 元", "100 元", false},  // P4
		{"120 元", "150 元", false}, // P5
		{"150 元", "150 元", true},  // N1 control
	}
	for _, p := range pairs {
		na, _ := NormalizeClaimValue(p.a, "")
		nb, _ := NormalizeClaimValue(p.b, "")
		if (na == nb) != p.wantEqual {
			t.Errorf("value norm equality for (%q,%q): got %v (na=%q nb=%q), want %v",
				p.a, p.b, na == nb, na, nb, p.wantEqual)
		}
	}
}

func TestFusedClaimKeyOverlongHashSuffix(t *testing.T) {
	long := ""
	for i := 0; i < 600; i++ {
		long += "长"
	}
	key := FusedClaimKey(long, "属性")
	runes := []rune(key)
	if len(runes) > claimKeyMaxRunes+9 { // 500 + "#" + 8 hex
		t.Errorf("overlong key not truncated: len=%d", len(runes))
	}
	// Same input → same key (deterministic); different tail → different hash.
	if key != FusedClaimKey(long, "属性") {
		t.Error("truncated key not deterministic")
	}
	if key == FusedClaimKey(long+"尾", "属性") {
		t.Error("hash suffix failed to disambiguate overlong keys")
	}
}
