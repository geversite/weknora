package service

// claim_strip_blocks.go removes machine-managed wiki blocks before claim
// extraction (C1). This is the second loop-breaker gate of Conflict V2: the
// dispute blocks written back by the pipeline (C4) are metadata, not
// knowledge, and must never re-enter claim extraction. The block markers are
// defined in the milestone plan §1.2:
//
//	<!-- weknora:dispute id=... ... -->
//	...block body...
//	<!-- /weknora:dispute -->
//
// Extraction runs on the stripped text; spans are mapped back to ORIGINAL
// rune offsets via the returned ClaimSpanMapper so dispute-block anchoring
// (C4) and frontend highlighting stay correct.

import (
	"strings"
)

// claimMachineBlockOpen / Close are matched per line (trimmed prefix match,
// defensive against indentation). Nested opens are treated as part of the
// outer block; an unclosed open strips to end-of-text and is reported.
const (
	claimMachineBlockOpen  = "<!-- weknora:dispute"
	claimMachineBlockClose = "<!-- /weknora:dispute"
)

// MachineBlock reports one removed block in ORIGINAL rune offsets.
type MachineBlock struct {
	Start    int  // rune offset of the block's first line start
	End      int  // rune offset just past the block's terminating newline
	Unclosed bool // true when the block ran to end-of-text without a close marker
}

// claimSpanSegment maps one kept run of text: stripped[SStart:SStart+Len]
// corresponds to original[OStart:OStart+Len].
type claimSpanSegment struct {
	SStart int
	OStart int
	Len    int
}

// ClaimSpanMapper converts spans on the stripped text back to original text
// offsets.
type ClaimSpanMapper struct {
	segments []claimSpanSegment
}

// ToOriginal maps a [start,end) rune span on the stripped text to the
// corresponding original-text span. Spans that fall outside every kept
// segment return (0, 0, false).
func (m *ClaimSpanMapper) ToOriginal(start, end int) (int, int, bool) {
	if m == nil || start >= end {
		return 0, 0, false
	}
	origStart, origEnd := -1, -1
	for _, seg := range m.segments {
		segEnd := seg.SStart + seg.Len
		if origStart < 0 && start >= seg.SStart && start < segEnd {
			origStart = seg.OStart + (start - seg.SStart)
		}
		if end > seg.SStart && end <= segEnd {
			origEnd = seg.OStart + (end - seg.SStart)
		}
	}
	if origStart < 0 || origEnd < 0 || origEnd <= origStart {
		return 0, 0, false
	}
	return origStart, origEnd, true
}

// StripMachineManagedBlocks removes weknora:dispute blocks from content and
// returns the stripped text, the removed blocks (original offsets) and a span
// mapper. Line-based state machine — no regex greediness, tolerant of
// unclosed markers (strips to end and flags Unclosed for the caller's
// telemetry counter).
func StripMachineManagedBlocks(content string) (string, []MachineBlock, *ClaimSpanMapper) {
	if !strings.Contains(content, claimMachineBlockOpen) {
		full := []rune(content)
		mapper := &ClaimSpanMapper{segments: []claimSpanSegment{{SStart: 0, OStart: 0, Len: len(full)}}}
		return content, nil, mapper
	}

	runes := []rune(content)
	var out strings.Builder
	out.Grow(len(content))
	var blocks []MachineBlock
	var segments []claimSpanSegment

	inBlock := false
	blockStart := 0
	lineStart := 0 // rune offset of current line start in original
	sOffset := 0   // rune offset in stripped output

	flushKept := func(from, to int) {
		if to <= from {
			return
		}
		segments = append(segments, claimSpanSegment{SStart: sOffset, OStart: from, Len: to - from})
		out.WriteString(string(runes[from:to]))
		sOffset += to - from
	}

	i := 0
	keptFrom := 0
	for i <= len(runes) {
		atEnd := i == len(runes)
		if !atEnd && runes[i] != '\n' {
			i++
			continue
		}
		// Current line is runes[lineStart:i] (excl. newline). Include the
		// newline in the line's extent when present.
		lineEnd := i
		if !atEnd {
			lineEnd = i + 1
		}
		line := strings.TrimSpace(string(runes[lineStart:i]))
		if !inBlock && strings.HasPrefix(line, claimMachineBlockOpen) &&
			!strings.HasPrefix(line, claimMachineBlockClose) {
			// Keep everything before this line, then enter the block.
			flushKept(keptFrom, lineStart)
			inBlock = true
			blockStart = lineStart
		} else if inBlock && strings.HasPrefix(line, claimMachineBlockClose) {
			inBlock = false
			blocks = append(blocks, MachineBlock{Start: blockStart, End: lineEnd})
			keptFrom = lineEnd
		}
		lineStart = lineEnd
		if atEnd {
			break
		}
		i++
	}
	if inBlock {
		// Unclosed block: strip to end of text.
		blocks = append(blocks, MachineBlock{Start: blockStart, End: len(runes), Unclosed: true})
	} else {
		flushKept(keptFrom, len(runes))
	}

	return out.String(), blocks, &ClaimSpanMapper{segments: segments}
}
