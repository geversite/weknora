package service

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/Tencent/WeKnora/internal/types"
	"github.com/Tencent/WeKnora/internal/types/interfaces"
)

const (
	conflictVersionHeaderMaxRunes = 2400
	conflictVersionHeaderMaxLines = 48
)

var (
	conflictVersionMetadataLineRE = regexp.MustCompile(`^\s*(?:[-*]\s*)?([^:：]{1,48}?)[：:]\s*(.+?)\s*$`)
	conflictVersionDateRE         = regexp.MustCompile(`(\d{4})\s*(?:年|[-/.])\s*(\d{1,2})(?:\s*(?:月|[-/.])\s*(\d{1,2}))?\s*(?:日)?`)
	conflictVersionYearRE         = regexp.MustCompile(`(\d{4})\s*年`)
	conflictVersionVRE            = regexp.MustCompile(`(?i)\bv\s*(\d+(?:\.\d+){0,3})\b`)
	conflictVersionChineseRE      = regexp.MustCompile(`第\s*(\d+(?:\.\d+){0,3})\s*版`)
	conflictVersionPlainRE        = regexp.MustCompile(`^\s*(\d+(?:\.\d+){0,3})\s*(?:版)?\s*$`)
)

// conflictVersionMetadataResolver loads a document once and snapshots only
// title/header metadata for C3-Lite. It intentionally avoids a second LLM
// extraction and never treats arbitrary fact dates in the document body as a
// document version/effective date.
type conflictVersionMetadataResolver struct {
	ctx          context.Context
	knowledgeSvc interfaces.KnowledgeService
	cache        map[string]types.ConflictDocumentMeta
}

func newConflictVersionMetadataResolver(
	ctx context.Context, knowledgeSvc interfaces.KnowledgeService,
) *conflictVersionMetadataResolver {
	return &conflictVersionMetadataResolver{
		ctx:          ctx,
		knowledgeSvc: knowledgeSvc,
		cache:        make(map[string]types.ConflictDocumentMeta),
	}
}

func (r *conflictVersionMetadataResolver) metadataFor(
	knowledgeID, fallbackTitle, fallbackContent string,
) types.ConflictDocumentMeta {
	knowledgeID = strings.TrimSpace(knowledgeID)
	if knowledgeID != "" && r != nil {
		if cached, ok := r.cache[knowledgeID]; ok {
			return cached
		}
	}

	var knowledge *types.Knowledge
	if r != nil && r.knowledgeSvc != nil && knowledgeID != "" {
		if loaded, err := r.knowledgeSvc.GetKnowledgeByID(r.ctx, knowledgeID); err == nil {
			knowledge = loaded
		}
	}
	meta := extractConflictDocumentMeta(knowledge, knowledgeID, fallbackTitle, fallbackContent)
	if knowledgeID != "" && r != nil {
		r.cache[knowledgeID] = meta
	}
	return meta
}

func extractConflictDocumentMeta(
	knowledge *types.Knowledge,
	knowledgeID, fallbackTitle, fallbackContent string,
) types.ConflictDocumentMeta {
	meta := types.ConflictDocumentMeta{
		ParserVersion: types.ConflictVersionSuggestionVersion,
		KnowledgeID:   strings.TrimSpace(knowledgeID),
		Title:         strings.TrimSpace(fallbackTitle),
	}
	content := fallbackContent
	if knowledge != nil {
		if knowledge.ID != "" {
			meta.KnowledgeID = knowledge.ID
		}
		if strings.TrimSpace(knowledge.Title) != "" {
			meta.Title = strings.TrimSpace(knowledge.Title)
		}
		if manual, err := knowledge.ManualMetadata(); err == nil && manual != nil && strings.TrimSpace(manual.Content) != "" {
			content = manual.Content
		}
	}

	// A date in a title counts only when the title explicitly presents it as an
	// edition/revision marker (for example "2148年10月版"). A bare title year
	// could be a project or product fact, so it is intentionally ignored.
	if date, precision, ok := parseConflictTitleEditionDate(meta.Title); ok {
		meta.EffectiveDate = date
		meta.EffectiveDatePrecision = precision
		meta.EffectiveDateEvidence = meta.Title
	}
	if version, ok := parseConflictVersionMarker(meta.Title, false); ok {
		meta.Version = version
		meta.VersionEvidence = meta.Title
	}
	// A manually supplied document title may deliberately carry explicit
	// metadata segments ("发布机构：…；生效日期：…；版本号：…"). Parse only
	// labeled segments, never arbitrary title prose.
	for _, segment := range conflictTitleMetadataSegments(meta.Title) {
		applyConflictDocumentMetadataLine(&meta, segment)
	}
	for _, line := range conflictMetadataHeaderLines(content) {
		applyConflictDocumentMetadataLine(&meta, line)
	}
	return meta
}

func conflictTitleMetadataSegments(title string) []string {
	return strings.FieldsFunc(title, func(r rune) bool {
		return r == '；' || r == ';' || r == '|'
	})
}

func applyConflictDocumentMetadataLine(meta *types.ConflictDocumentMeta, line string) {
	if meta == nil {
		return
	}
	label, value, ok := splitConflictMetadataLine(line)
	if !ok {
		return
	}
	normalizedLabel := normalizeConflictMetadataLabel(label)
	if meta.Issuer == "" && isConflictIssuerLabel(normalizedLabel) {
		meta.Issuer = strings.TrimSpace(value)
		meta.IssuerEvidence = strings.TrimSpace(line)
	}
	if isConflictEffectiveDateLabel(normalizedLabel) {
		if date, precision, ok := parseConflictDate(value); ok {
			// Explicit labeled evidence overrides a title edition date.
			meta.EffectiveDate = date
			meta.EffectiveDatePrecision = precision
			meta.EffectiveDateEvidence = strings.TrimSpace(line)
		}
	}
	if meta.Version == "" && isConflictVersionLabel(normalizedLabel) {
		if version, ok := parseConflictVersionMarker(value, true); ok {
			meta.Version = version
			meta.VersionEvidence = strings.TrimSpace(line)
		}
	}
}

func conflictMetadataHeaderLines(content string) []string {
	runes := []rune(content)
	if len(runes) > conflictVersionHeaderMaxRunes {
		runes = runes[:conflictVersionHeaderMaxRunes]
	}
	lines := strings.Split(string(runes), "\n")
	if len(lines) > conflictVersionHeaderMaxLines {
		lines = lines[:conflictVersionHeaderMaxLines]
	}
	return lines
}

func splitConflictMetadataLine(line string) (label, value string, ok bool) {
	line = strings.TrimSpace(strings.TrimPrefix(line, "#"))
	line = strings.ReplaceAll(line, "**", "")
	line = strings.ReplaceAll(line, "__", "")
	match := conflictVersionMetadataLineRE.FindStringSubmatch(line)
	if len(match) != 3 {
		return "", "", false
	}
	label = strings.TrimSpace(match[1])
	value = strings.TrimSpace(match[2])
	return label, value, label != "" && value != ""
}

func normalizeConflictMetadataLabel(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

func isConflictIssuerLabel(label string) bool {
	switch label {
	case "发布机构", "发布单位", "编制单位", "发文单位", "发布者", "issuer", "publisher", "issuingorganization":
		return true
	default:
		return false
	}
}

func isConflictEffectiveDateLabel(label string) bool {
	switch label {
	case "生效日期", "生效时间", "发布日期", "发布日", "更新日期", "修订日期", "版本日期", "effectivedate", "publicationdate", "releasedate", "updateddate":
		return true
	default:
		return false
	}
}

func isConflictVersionLabel(label string) bool {
	switch label {
	case "版本", "版本号", "修订版本", "edition", "version":
		return true
	default:
		return false
	}
}

func parseConflictTitleEditionDate(title string) (string, string, bool) {
	if !strings.Contains(title, "版") && !strings.Contains(strings.ToLower(title), "edition") && !strings.Contains(strings.ToLower(title), "version") {
		return "", "", false
	}
	return parseConflictDate(title)
}

func parseConflictDate(value string) (normalized, precision string, ok bool) {
	match := conflictVersionDateRE.FindStringSubmatch(value)
	if len(match) == 4 {
		year, err := strconv.Atoi(match[1])
		if err != nil || year < 1000 || year > 9999 {
			return "", "", false
		}
		month := 0
		if match[2] != "" {
			month, err = strconv.Atoi(match[2])
			if err != nil || month < 1 || month > 12 {
				return "", "", false
			}
		}
		if match[3] == "" {
			return fmt.Sprintf("%04d-%02d", year, month), "month", true
		}
		day, err := strconv.Atoi(match[3])
		if err != nil || day < 1 || day > 31 {
			return "", "", false
		}
		validated := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
		if validated.Year() != year || int(validated.Month()) != month || validated.Day() != day {
			return "", "", false
		}
		return fmt.Sprintf("%04d-%02d-%02d", year, month, day), "day", true
	}
	match = conflictVersionYearRE.FindStringSubmatch(value)
	if len(match) == 2 {
		year, err := strconv.Atoi(match[1])
		if err == nil && year >= 1000 && year <= 9999 {
			return fmt.Sprintf("%04d", year), "year", true
		}
	}
	return "", "", false
}

func parseConflictVersionMarker(value string, allowPlain bool) (string, bool) {
	if match := conflictVersionVRE.FindStringSubmatch(value); len(match) == 2 {
		return normalizeConflictVersion(match[1])
	}
	if match := conflictVersionChineseRE.FindStringSubmatch(value); len(match) == 2 {
		return normalizeConflictVersion(match[1])
	}
	if allowPlain {
		if match := conflictVersionPlainRE.FindStringSubmatch(value); len(match) == 2 {
			return normalizeConflictVersion(match[1])
		}
	}
	return "", false
}

func normalizeConflictVersion(value string) (string, bool) {
	parts := strings.Split(strings.TrimSpace(value), ".")
	if len(parts) == 0 || len(parts) > 4 {
		return "", false
	}
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 || number > 1000000 {
			return "", false
		}
		normalized = append(normalized, strconv.Itoa(number))
	}
	return strings.Join(normalized, "."), true
}

// suggestConflictVersionResolution returns no result unless both sides carry
// explicit, normalized issuer evidence and comparable metadata. It is advisory
// only and is intentionally safe for any final fact conflict (not only an LLM
// version_update label): C2-A can direct-detect a numeric contradiction whose
// documents are nevertheless an explicit version sequence.
func suggestConflictVersionResolution(
	metaA, metaB types.ConflictDocumentMeta,
) types.ConflictVersionSuggestion {
	issuerA := normalizeConflictIssuer(metaA.Issuer)
	issuerB := normalizeConflictIssuer(metaB.Issuer)
	if issuerA == "" || issuerB == "" || issuerA != issuerB {
		return types.ConflictVersionSuggestion{}
	}

	dateDirection, dateConfidence, hasDate := compareConflictEffectiveDates(metaA, metaB)
	versionDirection, hasVersion := compareConflictVersions(metaA.Version, metaB.Version)
	if hasDate && hasVersion && dateDirection != versionDirection {
		// Conflicting metadata evidence must not create a suggested winner.
		return types.ConflictVersionSuggestion{}
	}
	if !hasDate && !hasVersion {
		return types.ConflictVersionSuggestion{}
	}

	direction := dateDirection
	confidence := dateConfidence
	reason := ""
	if hasDate {
		reason = fmt.Sprintf(
			"[c3:%s] 同发布机构“%s”；文档 A 生效日期 %s 与文档 B 生效日期 %s 可比较。",
			types.ConflictVersionSuggestionVersion, metaA.Issuer, metaA.EffectiveDate, metaB.EffectiveDate,
		)
	} else {
		direction = versionDirection
		confidence = 0.90
		reason = fmt.Sprintf(
			"[c3:%s] 同发布机构“%s”；文档 A 版本 %s 与文档 B 版本 %s 可比较。",
			types.ConflictVersionSuggestionVersion, metaA.Issuer, metaA.Version, metaB.Version,
		)
	}
	if hasDate && hasVersion {
		confidence = min(0.99, confidence+0.03)
		reason += fmt.Sprintf(" 版本号 %s 与 %s 的方向一致。", metaA.Version, metaB.Version)
	}
	if direction > 0 {
		return types.ConflictVersionSuggestion{
			Resolution: types.ConflictStatusResolvedNewer,
			Reason:     reason + " 建议以文档 A 为较新版本（仅建议，不自动裁决）。",
			Confidence: confidence,
		}
	}
	return types.ConflictVersionSuggestion{
		Resolution: types.ConflictStatusResolvedOlder,
		Reason:     reason + " 建议以文档 B 为较新版本（仅建议，不自动裁决）。",
		Confidence: confidence,
	}
}

// compareConflictDocumentRecency is shared by C3's pair-level suggestion and
// C3/C4.6's global winner proposal. It returns a direction only when every
// available metadata signal agrees; +1 means A is newer, -1 means B is newer.
func compareConflictDocumentRecency(
	metaA, metaB types.ConflictDocumentMeta,
) (direction int, confidence float64, ok bool) {
	dateDirection, dateConfidence, hasDate := compareConflictEffectiveDates(metaA, metaB)
	versionDirection, hasVersion := compareConflictVersions(metaA.Version, metaB.Version)
	if hasDate && hasVersion && dateDirection != versionDirection {
		return 0, 0, false
	}
	if hasDate {
		if hasVersion {
			return dateDirection, min(0.99, dateConfidence+0.03), true
		}
		return dateDirection, dateConfidence, true
	}
	if hasVersion {
		return versionDirection, 0.90, true
	}
	return 0, 0, false
}

func normalizeConflictIssuer(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	var builder strings.Builder
	for _, r := range value {
		if unicode.IsSpace(r) || unicode.IsPunct(r) || unicode.IsSymbol(r) {
			continue
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

type conflictDateInterval struct {
	start     time.Time
	end       time.Time
	precision string
}

// compareConflictEffectiveDates returns +1 when A is definitely newer, -1
// when B is definitely newer. Year/month precision becomes an interval; any
// overlapping intervals are deliberately treated as incomparable.
func compareConflictEffectiveDates(
	metaA, metaB types.ConflictDocumentMeta,
) (direction int, confidence float64, ok bool) {
	left, leftOK := conflictDateIntervalFromMeta(metaA)
	right, rightOK := conflictDateIntervalFromMeta(metaB)
	if !leftOK || !rightOK {
		return 0, 0, false
	}
	if left.start.After(right.end) {
		return 1, conflictDateComparisonConfidence(left.precision, right.precision), true
	}
	if right.start.After(left.end) {
		return -1, conflictDateComparisonConfidence(left.precision, right.precision), true
	}
	return 0, 0, false
}

func conflictDateIntervalFromMeta(meta types.ConflictDocumentMeta) (conflictDateInterval, bool) {
	parts := strings.Split(meta.EffectiveDate, "-")
	if len(parts) < 1 || len(parts) > 3 || parts[0] == "" {
		return conflictDateInterval{}, false
	}
	year, err := strconv.Atoi(parts[0])
	if err != nil || year < 1000 || year > 9999 {
		return conflictDateInterval{}, false
	}
	if len(parts) == 1 {
		return conflictDateInterval{
			start: time.Date(year, time.January, 1, 0, 0, 0, 0, time.UTC),
			end:   time.Date(year, time.December, 31, 23, 59, 59, 0, time.UTC),
			precision: "year",
		}, true
	}
	month, err := strconv.Atoi(parts[1])
	if err != nil || month < 1 || month > 12 {
		return conflictDateInterval{}, false
	}
	if len(parts) == 2 {
		start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		return conflictDateInterval{
			start: start,
			end:   time.Date(year, time.Month(month)+1, 0, 23, 59, 59, 0, time.UTC),
			precision: "month",
		}, true
	}
	day, err := strconv.Atoi(parts[2])
	if err != nil || day < 1 || day > 31 {
		return conflictDateInterval{}, false
	}
	instant := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	if instant.Year() != year || int(instant.Month()) != month || instant.Day() != day {
		return conflictDateInterval{}, false
	}
	return conflictDateInterval{start: instant, end: instant.Add(24*time.Hour - time.Nanosecond), precision: "day"}, true
}

func conflictDateComparisonConfidence(left, right string) float64 {
	if left == "day" && right == "day" {
		return 0.96
	}
	if left == "year" || right == "year" {
		return 0.86
	}
	return 0.91
}

func compareConflictVersions(left, right string) (direction int, ok bool) {
	if left == "" || right == "" {
		return 0, false
	}
	leftParts, leftOK := conflictVersionParts(left)
	rightParts, rightOK := conflictVersionParts(right)
	if !leftOK || !rightOK {
		return 0, false
	}
	length := len(leftParts)
	if len(rightParts) > length {
		length = len(rightParts)
	}
	for index := 0; index < length; index++ {
		leftValue, rightValue := 0, 0
		if index < len(leftParts) {
			leftValue = leftParts[index]
		}
		if index < len(rightParts) {
			rightValue = rightParts[index]
		}
		if leftValue > rightValue {
			return 1, true
		}
		if rightValue > leftValue {
			return -1, true
		}
	}
	return 0, false
}

func conflictVersionParts(value string) ([]int, bool) {
	parts := strings.Split(value, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return nil, false
	}
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return nil, false
		}
		out = append(out, number)
	}
	return out, true
}

func conflictDocumentMetaJSON(meta types.ConflictDocumentMeta) types.JSON {
	encoded, err := json.Marshal(meta)
	if err != nil {
		return types.JSON(`{}`)
	}
	return types.JSON(encoded)
}

func conflictAnchorSupportsVersionSuggestion(anchor conflictFactAnchor) bool {
	return anchor.AnchorKind == types.ConflictFactAnchorClaimKey ||
		anchor.AnchorKind == types.ConflictFactAnchorFuzzySlot ||
		anchor.AnchorKind == types.ConflictFactAnchorDocumentSingleton
}
