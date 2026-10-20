package scrape

import "testing"

// 艺人归一化：小写、去括号备注、拆分并列与 feat.。
func TestNormalizeArtistTokens(t *testing.T) {
	got := normalizeArtistTokens("Eason Chan feat. XX")
	if len(got) == 0 || got[0] != "easonchan" {
		t.Fatalf("normalize failed, got %v", got)
	}
	joint := normalizeArtistTokens("EO2 & 好友")
	found := false
	for _, p := range joint {
		if p == "eo2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected eo2 token after split, got %v", joint)
	}
}

// ArtistSimilar：同艺人=1，异名=0，包含关系给部分分。
func TestArtistSimilar(t *testing.T) {
	if ArtistSimilar([]string{"EO2"}, []string{"EO2"}) < 1 {
		t.Fatal("same artist should score 1")
	}
	if ArtistSimilar([]string{"陈奕迅"}, []string{"EO2"}) >= 0.5 {
		t.Fatal("different artists must not match")
	}
	if ArtistSimilar([]string{""}, []string{"EO2"}) != 0 {
		t.Fatal("empty candidate artist should score 0")
	}
}

// pickLyricCandidate：目标艺人非空时，同名异人候选必须被淘汰。
func TestPickLyricCandidateRejectsOtherArtist(t *testing.T) {
	list := []SearchResult{
		{Source: "qq", SourceID: "eason", Title: "内疚", Artists: []string{"陈奕迅"}},
		{Source: "netease", SourceID: "eo2", Title: "内疚", Artists: []string{"EO2"}},
	}
	c := pickLyricCandidate(list, "内疚", []string{"EO2"})
	if c.SourceID != "eo2" {
		t.Fatalf("must pick EO2 candidate, got %q", c.SourceID)
	}
	// 只有异名候选且目标有艺人 → 不取（宁缺毋滥，避免串歌）。
	onlyOther := []SearchResult{
		{Source: "qq", SourceID: "eason", Title: "内疚", Artists: []string{"陈奕迅"}},
	}
	if c2 := pickLyricCandidate(onlyOther, "内疚", []string{"EO2"}); c2.SourceID != "" {
		t.Fatalf("must reject non-matching artist, got %q", c2.SourceID)
	}
	// 目标艺人未知（空）时退回标题匹配，不阻断。
	c3 := pickLyricCandidate(onlyOther, "内疚", nil)
	if c3.SourceID != "eason" {
		t.Fatalf("unknown target artist should fall back to title match, got %q", c3.SourceID)
	}
}
