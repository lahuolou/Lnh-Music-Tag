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
	out := fuseMetadata([]scrape.SearchResult{a, b, c}, want, "")

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
	out := fuseMetadata([]scrape.SearchResult{a, b}, "歌", "")
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

// 同名不同艺人不得互相顶替：目标 EO2《内疚》不能被陈奕迅《内疚》的
// 艺人/代表源/专辑/封面覆盖（串歌回归用例）。
func TestFuseMetadataRejectsSameNameOtherArtist(t *testing.T) {
	e2 := scrape.SearchResult{
		Source: "netease", SourceID: "e2id",
		Title: "内疚", Artists: []string{"EO2"}, Album: "EO2同名专辑",
	}
	eason := scrape.SearchResult{
		Source: "qq", SourceID: "easonid",
		Title: "内疚", Artists: []string{"陈奕迅"}, Album: "U87",
	}
	out := fuseMetadata([]scrape.SearchResult{eason, e2}, "内疚", "EO2")

	for _, a := range out.Artists {
		if a == "陈奕迅" {
			t.Fatalf("must not adopt other-artist 陈奕迅, got %v", out.Artists)
		}
	}
	if len(out.Artists) == 0 || out.Artists[0] != "EO2" {
		t.Fatalf("artist should be EO2, got %v", out.Artists)
	}
	if out.SourceID != "e2id" {
		t.Fatalf("representative source must stay on EO2 candidate, got %q", out.SourceID)
	}
	if out.Album == "U87" {
		t.Fatalf("album must not come from other-artist candidate, got %q", out.Album)
	}
}

// 目标艺人已知但所有候选都是异名同名曲时，不得写艺术家、不得挂异名源ID/封面，
// 保留原标签交给后续兜底，而不是把对的艺人冲成错的。
func TestFuseMetadataHoldsBackWhenNoArtistMatch(t *testing.T) {
	onlyOther := []scrape.SearchResult{
		{Source: "qq", SourceID: "easonid", Title: "内疚", Artists: []string{"陈奕迅"}, CoverURL: "http://wrong/cover.jpg"},
	}
	out := fuseMetadata(onlyOther, "内疚", "EO2")
	if len(out.Artists) != 0 {
		t.Fatalf("must not adopt non-matching artist, got %v", out.Artists)
	}
	if out.SourceID != "" || out.CoverURL != "" {
		t.Fatalf("must not attach non-matching representative source, got id=%q cover=%q", out.SourceID, out.CoverURL)
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
