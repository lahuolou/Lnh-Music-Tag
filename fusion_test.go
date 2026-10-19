package main

import (
	"testing"

	"lnh-music-tag/internal/scrape"
)

// 多源融合：标题取匹配度最高的候选，艺术家/专辑/年代/流派从其他更完整的
// 候选取，ReleaseMBID 从 MusicBrainz 候选继承，流派跨源去重合并。
func TestFuseMetadataFieldWise(t *testing.T) {
	want := "蓝色蝴蝶"
	a := scrape.SearchResult{Source: "netease", Title: "蓝色蝴蝶"} // 精确匹配，但信息少
	b := scrape.SearchResult{
		Source: "itunes", Title: "蓝蝴蝶",
		Artists: []string{"歌手乙"}, Album: "专辑乙",
		Genre: []string{"Pop"}, Date: "2020", TrackNumber: 3,
	}
	c := scrape.SearchResult{
		Source: "musicbrainz", Title: "蓝色蝴蝶",
		ReleaseMBID: "mb-123", AlbumArtist: []string{"专辑艺人"},
		Genre: []string{"流行", "Pop"}, Language: "zh",
	}
	out := fuseMetadata([]scrape.SearchResult{a, b, c}, want)

	if out.Title != "蓝色蝴蝶" {
		t.Fatalf("title should come from exact-match candidate, got %q", out.Title)
	}
	if len(out.Artists) != 1 || out.Artists[0] != "歌手乙" {
		t.Fatalf("artist should come from complete candidate, got %v", out.Artists)
	}
	if out.Album != "专辑乙" {
		t.Fatalf("album should come from complete candidate, got %q", out.Album)
	}
	if out.Date != "2020" {
		t.Fatalf("date should come from complete candidate, got %q", out.Date)
	}
	if out.TrackNumber != 3 {
		t.Fatalf("track number should come from complete candidate, got %d", out.TrackNumber)
	}
	if out.ReleaseMBID != "mb-123" {
		t.Fatalf("ReleaseMBID should be inherited from MB candidate, got %q", out.ReleaseMBID)
	}
	if len(out.AlbumArtist) != 1 || out.AlbumArtist[0] != "专辑艺人" {
		t.Fatalf("album artist should come from MB candidate, got %v", out.AlbumArtist)
	}
	if out.Language != "zh" {
		t.Fatalf("language should come from MB candidate, got %q", out.Language)
	}
	// 流派：Pop 去重只留一个，流行保留
	seen := map[string]int{}
	for _, g := range out.Genre {
		seen[g]++
	}
	if seen["Pop"] != 1 {
		t.Fatalf("Pop genre should be deduped to one, got %v", out.Genre)
	}
	if seen["流行"] != 1 {
		t.Fatalf("unique genre 流行 should be kept, got %v", out.Genre)
	}
}

// 占位值（Unknown/未知专辑/Various）不应被择优采用。
func TestFuseMetadataRejectsPlaceholders(t *testing.T) {
	a := scrape.SearchResult{Source: "netease", Title: "歌", Artists: []string{"Unknown"}, Album: "未知专辑"}
	b := scrape.SearchResult{Source: "qq", Title: "歌", Artists: []string{"真人"}, Album: "真实专辑", Date: "2021"}
	out := fuseMetadata([]scrape.SearchResult{a, b}, "歌")
	if len(out.Artists) == 1 && out.Artists[0] == "Unknown" {
		t.Fatalf("placeholder artist must be rejected, got %v", out.Artists)
	}
	if out.Album != "真实专辑" {
		t.Fatalf("album should be the real one, got %q", out.Album)
	}
	if out.Date != "2021" {
		t.Fatalf("date should be usable, got %q", out.Date)
	}
}

func TestFuseHelpers(t *testing.T) {
	if !albumUsable("某专辑") || albumUsable("") || albumUsable("Unknown Album") || albumUsable("Various Artists") {
		t.Fatal("albumUsable boundary wrong")
	}
	if !dateUsable("2020") || !dateUsable("2020-05-01") || dateUsable("") || dateUsable("Unknown") {
		t.Fatal("dateUsable boundary wrong")
	}
}
