package service

// claim_normalize.go implements the C1 claim key / value normalization rules
// (Conflict V2 tech design §4 + §4.1 v1.1 calibration). The reference
// prototype and calibration corpus live in testdata/claims_eval/evaluate.py —
// keep the two in sync when rules change, and bump types.ClaimExtractorVersion
// whenever the output of these functions changes for existing inputs.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/Tencent/WeKnora/internal/types"
	"golang.org/x/text/unicode/norm"
)

const claimKeyMaxRunes = 500

// wrapPunct are wrapping punctuation runes stripped from the edges of
// normalized strings.
const wrapPunct = "《》「」『』【】\u201c\u201d\u2018\u2019\"'()（）"

var (
	claimParenRE       = regexp.MustCompile(`[（(][^（）()]*[）)]`)
	claimConnectiveRE  = regexp.MustCompile(`并在|并且|以及|并|且`)
	claimModalPrefixRE = regexp.MustCompile(`^(必须|须|应当|应|需要|需)`)
	claimCountSuffixRE = regexp.MustCompile(`[零一两二三四五六七八九十百千0-9]+[家个项条名位]$`)
	claimSpacesRE      = regexp.MustCompile(`\s+`)

	claimDateRE = regexp.MustCompile(`([0-9]{4})\s*年\s*([0-9]{1,2})\s*月(?:\s*([0-9]{1,2})\s*日)?`)
	claimYearRE = regexp.MustCompile(`([0-9]{4})\s*年`)
	claimTimeRE = regexp.MustCompile(`\b([0-9]{1,2}):([0-9]{2})\b`)
	claimNumRE  = regexp.MustCompile(`(?i)(-?[0-9]+(?:\.[0-9]+)?)([万亿]?)\s*([%％]|[a-zａ-ｚＡ-Ｚ℃°]+|\p{Han}{1,4})?`)
	claimRangeRE = regexp.MustCompile(`至|~|—|--`)
)

// claimPredLead / claimPredTail are the weak-semantics predicate affixes
// stripped before key comparison (v1.1; small lexicon on purpose — validate
// merge-error rate before extending).
var claimPredLead = []string{"项目", "年度", "完成", "申请", "选择", "上岗", "例行"}
var claimPredTail = []string{"要求", "方式", "节点"}

// claimUnitAlias canonicalizes measurement units after number extraction.
var claimUnitAlias = map[string]string{
	"日": "天", "自然日": "天", "个自然日": "天", "块": "元", "rmb": "元",
	"人民币": "元", "个工作日": "工作日", "名": "个", "位": "个", "人": "个",
}

// claimCNNum maps single Chinese numerals (used for counts like "两个").
var claimCNNum = map[rune]int{
	'零': 0, '一': 1, '两': 2, '二': 2, '三': 3, '四': 4,
	'五': 5, '六': 6, '七': 7, '八': 8, '九': 9, '十': 10,
}

// NormalizeClaimText is the base normalization shared by subjects, predicates
// and enum/text values: NFKC fold, lowercase, parenthetical-annotation
// removal, whitespace fold, edge wrap-punct strip, and removal of internal
// spaces that do not separate two latin/digit runes.
func NormalizeClaimText(s string) string {
	s = norm.NFKC.String(s)
	s = strings.ToLower(strings.TrimSpace(s))
	s = claimParenRE.ReplaceAllString(s, "")
	s = claimSpacesRE.ReplaceAllString(s, " ")
	s = strings.Trim(s, wrapPunct)
	s = removeNonLatinSpaces(s)
	return s
}

// removeNonLatinSpaces drops every space whose two neighbours are not both
// latin letters or digits (CJK text keeps no internal spaces; "sq 7" keeps
// its separator).
func removeNonLatinSpaces(s string) string {
	runes := []rune(s)
	var b strings.Builder
	b.Grow(len(s))
	isAlnum := func(r rune) bool {
		return (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
	}
	for i, r := range runes {
		if r == ' ' {
			prevOK := i > 0 && isAlnum(runes[i-1])
			nextOK := i+1 < len(runes) && isAlnum(runes[i+1])
			if !(prevOK && nextOK) {
				continue
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// NormalizeClaimTextValue applies the deeper text/enum value normalization
// used ONLY for equality comparison (stored Value keeps the verbatim
// phrasing): particle "的", connectives, list punctuation, affirmative modal
// prefixes and trailing count suffixes are removed. Negations (不/不得/非/禁止)
// are never touched — stripping them would flip meaning.
func NormalizeClaimTextValue(s string) string {
	s = NormalizeClaimText(s)
	s = strings.ReplaceAll(s, "的", "")
	s = claimConnectiveRE.ReplaceAllString(s, "")
	s = strings.NewReplacer("、", "", "，", "", ",", "", "·", "").Replace(s)
	s = claimModalPrefixRE.ReplaceAllString(s, "")
	s = claimCountSuffixRE.ReplaceAllString(s, "")
	return strings.Trim(s, wrapPunct)
}

// NormalizePredicate normalizes a predicate and strips one weak-semantics
// lead affix and one tail affix ("上岗资质"→"资质", "处理方式"→"处理").
func NormalizePredicate(p string) string {
	p = NormalizeClaimText(p)
	for _, lead := range claimPredLead {
		if strings.HasPrefix(p, lead) && len([]rune(p)) > len([]rune(lead))+1 {
			p = strings.TrimPrefix(p, lead)
			break
		}
	}
	for _, tail := range claimPredTail {
		if strings.HasSuffix(p, tail) && len([]rune(p)) > len([]rune(tail))+1 {
			p = strings.TrimSuffix(p, tail)
			break
		}
	}
	return p
}

// FusedClaimKey builds the boundary-insensitive pairing key
// norm(subject)+normPredicate(predicate) (v1.1): "幽能引擎@原型机测试时间" and
// "幽能引擎原型机@测试时间" fuse to the same key. Overlong keys are truncated
// with a content-hash suffix to stay collision-safe within VARCHAR(512).
func FusedClaimKey(subject, predicate string) string {
	key := NormalizeClaimText(subject) + NormalizePredicate(predicate)
	runes := []rune(key)
	if len(runes) <= claimKeyMaxRunes {
		return key
	}
	sum := sha256.Sum256([]byte(key))
	return string(runes[:claimKeyMaxRunes]) + "#" + hex.EncodeToString(sum[:])[:8]
}

// DisplayClaimKey keeps the human-readable subject@predicate form for logs
// and debugging (NOT used for pairing).
func DisplayClaimKey(subject, predicate string) string {
	return NormalizeClaimText(subject) + "@" + NormalizePredicate(predicate)
}

// NormalizeClaimValue normalizes a value string and classifies its kind.
// Order of precedence: date → clock time → numeric range → single number →
// single Chinese numeral count → enum/text. The extractor's kind hint is only
// honoured for the enum/text distinction; numeric detection never trusts it
// (design §5.3: the server does not trust LLM value_kind).
func NormalizeClaimValue(value, kindHint string) (valueNorm, valueKind string) {
	raw := strings.TrimSpace(norm.NFKC.String(value))

	if m := claimDateRE.FindStringSubmatch(raw); m != nil {
		mo, _ := strconv.Atoi(m[2])
		out := fmt.Sprintf("%s-%02d", m[1], mo)
		if m[3] != "" {
			d, _ := strconv.Atoi(m[3])
			out += fmt.Sprintf("-%02d", d)
		}
		return out, types.ClaimValueKindDate
	}
	if m := claimYearRE.FindStringSubmatch(raw); m != nil {
		return m[1], types.ClaimValueKindDate
	}
	if m := claimTimeRE.FindStringSubmatch(raw); m != nil {
		h, _ := strconv.Atoi(m[1])
		return fmt.Sprintf("%02d:%s", h, m[2]), types.ClaimValueKindNumber
	}

	nums := claimNumRE.FindAllStringSubmatch(raw, -1)
	if len(nums) >= 2 && claimRangeRE.MatchString(raw) {
		unit := ""
		parts := make([]string, 0, 2)
		for _, m := range nums[:2] {
			parts = append(parts, formatClaimNumber(m[1], m[2]))
			if m[3] != "" {
				unit = cleanClaimUnit(m[3])
			}
		}
		return parts[0] + "~" + parts[1] + "|" + unit, types.ClaimValueKindNumber
	}
	if len(nums) >= 1 {
		m := nums[0]
		if m[3] == "%" || m[3] == "％" {
			f, err := strconv.ParseFloat(m[1], 64)
			if err == nil {
				return trimFloat(f/100) + "|", types.ClaimValueKindNumber
			}
		}
		return formatClaimNumber(m[1], m[2]) + "|" + cleanClaimUnit(m[3]), types.ClaimValueKindNumber
	}

	// Single Chinese numeral count ("两个工程师").
	if strings.ContainsAny(raw, "个名位人次") {
		for _, r := range raw {
			if v, ok := claimCNNum[r]; ok {
				return strconv.Itoa(v) + "|个", types.ClaimValueKindNumber
			}
		}
	}

	if kindHint == types.ClaimValueKindEnum {
		return NormalizeClaimTextValue(raw), types.ClaimValueKindEnum
	}
	return NormalizeClaimTextValue(raw), types.ClaimValueKindText
}

// formatClaimNumber applies the 万/亿 multiplier and renders a canonical
// numeric literal without trailing zeros.
func formatClaimNumber(num, mult string) string {
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return num
	}
	switch mult {
	case "万":
		f *= 1e4
	case "亿":
		f *= 1e8
	}
	return trimFloat(f)
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

// cleanClaimUnit canonicalizes a unit token captured after a number.
func cleanClaimUnit(u string) string {
	u = strings.TrimSpace(strings.ToLower(u))
	if v, ok := claimUnitAlias[u]; ok {
		u = v
	}
	// Trim direction/limit suffix words that leak into the unit capture.
	for _, stop := range []string{"内", "前", "后"} {
		if strings.HasSuffix(u, stop) && len([]rune(u)) > 1 {
			u = strings.TrimSuffix(u, stop)
		}
	}
	if v, ok := claimUnitAlias[u]; ok {
		u = v
	}
	return u
}

// isClaimHan reports whether r is a Han rune (kept for future key rules).
func isClaimHan(r rune) bool { return unicode.Is(unicode.Han, r) }
