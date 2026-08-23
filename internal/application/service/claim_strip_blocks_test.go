package service

import (
	"strings"
	"testing"
)

func TestStripMachineManagedBlocksNoBlocks(t *testing.T) {
	content := "# 页面\n正文第一行。\n正文第二行。\n"
	stripped, blocks, mapper := StripMachineManagedBlocks(content)
	if stripped != content {
		t.Errorf("content without blocks must pass through unchanged")
	}
	if len(blocks) != 0 {
		t.Errorf("expected 0 blocks, got %d", len(blocks))
	}
	// Identity span mapping.
	s, e, ok := mapper.ToOriginal(2, 5)
	if !ok || s != 2 || e != 5 {
		t.Errorf("identity mapping broken: got (%d,%d,%v)", s, e, ok)
	}
}

func TestStripMachineManagedBlocksBasic(t *testing.T) {
	content := strings.Join([]string{
		"# 报销上限",
		"正文A。",
		"<!-- weknora:dispute id=df-1 status=pending -->",
		"> 争议内容,不得参与抽取",
		"<!-- /weknora:dispute -->",
		"正文B。",
	}, "\n")
	stripped, blocks, mapper := StripMachineManagedBlocks(content)
	if strings.Contains(stripped, "争议内容") {
		t.Fatalf("dispute body leaked into stripped text: %q", stripped)
	}
	if !strings.Contains(stripped, "正文A。") || !strings.Contains(stripped, "正文B。") {
		t.Fatalf("kept text missing: %q", stripped)
	}
	if len(blocks) != 1 || blocks[0].Unclosed {
		t.Fatalf("expected 1 closed block, got %+v", blocks)
	}
	// Span restore: locate 正文B in stripped, map back, verify original text.
	idx := strings.Index(stripped, "正文B")
	start := len([]rune(stripped[:idx]))
	os, oe, ok := mapper.ToOriginal(start, start+len([]rune("正文B")))
	if !ok {
		t.Fatal("span mapping failed for kept text after block")
	}
	orig := []rune(content)
	if string(orig[os:oe]) != "正文B" {
		t.Errorf("restored span = %q, want 正文B", string(orig[os:oe]))
	}
}

func TestStripMachineManagedBlocksUnclosed(t *testing.T) {
	content := "正文。\n<!-- weknora:dispute id=df-2 -->\n烂尾内容\n没有闭合标记"
	stripped, blocks, _ := StripMachineManagedBlocks(content)
	if strings.Contains(stripped, "烂尾内容") {
		t.Fatalf("unclosed block body leaked: %q", stripped)
	}
	if len(blocks) != 1 || !blocks[0].Unclosed {
		t.Fatalf("expected 1 unclosed block, got %+v", blocks)
	}
	if !strings.Contains(stripped, "正文。") {
		t.Errorf("text before unclosed block must be kept")
	}
}

func TestStripMachineManagedBlocksMultiple(t *testing.T) {
	content := strings.Join([]string{
		"A",
		"<!-- weknora:dispute id=1 -->",
		"x",
		"<!-- /weknora:dispute -->",
		"B",
		"<!-- weknora:dispute id=2 -->",
		"y",
		"<!-- /weknora:dispute -->",
		"C",
	}, "\n")
	stripped, blocks, _ := StripMachineManagedBlocks(content)
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks, got %d", len(blocks))
	}
	for _, leak := range []string{"x", "y", "weknora:dispute"} {
		if strings.Contains(stripped, leak) {
			t.Errorf("leaked %q into stripped text: %q", leak, stripped)
		}
	}
	for _, keep := range []string{"A", "B", "C"} {
		if !strings.Contains(stripped, keep) {
			t.Errorf("lost kept text %q: %q", keep, stripped)
		}
	}
}

func TestLocateQuote(t *testing.T) {
	content := "国内出差住宿费实行城市分级上限：一线城市每晚上限为 650 元。"
	// Exact match.
	s, e := locateQuote(content, "一线城市每晚上限为 650 元")
	if e <= s {
		t.Fatal("exact quote location failed")
	}
	if got := string([]rune(content)[s:e]); got != "一线城市每晚上限为 650 元" {
		t.Errorf("located %q", got)
	}
	// Whitespace-folded match (quote drops the spaces).
	s2, e2 := locateQuote(content, "一线城市每晚上限为650元")
	if e2 <= s2 {
		t.Fatal("folded quote location failed")
	}
	// Miss → (0,0).
	if s3, e3 := locateQuote(content, "不存在的引文"); s3 != 0 || e3 != 0 {
		t.Errorf("miss should return (0,0), got (%d,%d)", s3, e3)
	}
}

func TestSplitRuneWindows(t *testing.T) {
	text := strings.Repeat("字", 100)
	ws := splitRuneWindows(text, 40, 10)
	if len(ws) < 3 {
		t.Fatalf("expected >=3 windows, got %d", len(ws))
	}
	// Overlap continuity: each window starts 30 runes after the previous.
	for i := 1; i < len(ws); i++ {
		if ws[i].start-ws[i-1].start != 30 {
			t.Errorf("window step = %d, want 30", ws[i].start-ws[i-1].start)
		}
	}
	// Short text → single window.
	if got := splitRuneWindows("短文本", 40, 10); len(got) != 1 || got[0].start != 0 {
		t.Errorf("short text should yield 1 window, got %+v", got)
	}
}
