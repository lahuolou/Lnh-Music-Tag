package main

import (
	"testing"

	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/scrape"
)

// TestScrapeWrongArtistRepro 复现"梁静茹 - 勇气"被刮成同名异艺人问题，
// 逐环节打印：文件名解析 → 有效搜索词 → 艺人相似度 → 融合结果。
func TestScrapeWrongArtistRepro(t *testing.T) {
	// 1. 文件名解析
	a, ti := parseFileNameArtistTitle("梁静茹 - 勇气.flac")
	t.Logf("parseFileName: artist=%q title=%q", a, ti)

	// 2. effectiveSearchTerms（标签为空场景：首次刮削）
	tr := &model.Track{FileName: "梁静茹 - 勇气.flac", Tags: map[string]string{}}
	tt, ta := effectiveSearchTerms(tr)
	t.Logf("effectiveSearchTerms(空标签): title=%q artist=%q", tt, ta)

	// 3. effectiveSearchTerms（标签已是错误艺人场景：二次刮削自我强化）
	//    修复后：标签艺人与文件名明显不一致且标题一致 → 回退文件名"梁静茹"，
	//    避免用错误标签"派伟俊"检索继续刮回错误版本。
	tr2 := &model.Track{FileName: "梁静茹 - 勇气.flac", Tags: map[string]string{"TITLE": "勇气 (2018春晚表演歌曲)", "ARTIST": "派伟俊"}}
	tt2, ta2 := effectiveSearchTerms(tr2)
	t.Logf("effectiveSearchTerms(错误标签): title=%q artist=%q", tt2, ta2)
	if ta2 != "梁静茹" {
		t.Errorf("错误标签未被文件名纠正: artist=%q", ta2)
	}

	// 4. 艺人相似度（简体候选 / 繁体候选 / 异名）
	t.Logf("ArtistSimilar(梁静茹,[梁静茹])=%v", scrape.ArtistSimilar([]string{"梁静茹"}, []string{"梁静茹"}))
	t.Logf("ArtistSimilar(梁靜茹,[梁静茹])=%v", scrape.ArtistSimilar([]string{"梁靜茹"}, []string{"梁静茹"}))
	t.Logf("ArtistSimilar(派伟俊,[梁静茹])=%v", scrape.ArtistSimilar([]string{"派伟俊"}, []string{"梁静茹"}))
	t.Logf("ArtistSimilar(棉子,[梁静茹])=%v", scrape.ArtistSimilar([]string{"棉子"}, []string{"梁静茹"}))

	// 5. 融合：候选含梁静茹原版 + 异名版时（wantArtist=梁静茹）
	results := []scrape.SearchResult{
		{Title: "勇气 (2018春晚表演歌曲)", Artists: []string{"派伟俊"}, Album: "2018春晚表演歌曲", Date: "2018"},
		{Title: "勇气", Artists: []string{"梁静茹"}, Album: "勇气", Date: "2000"},
	}
	out := fuseMetadata(results, "勇气", "梁静茹")
	t.Logf("fused(含梁静茹版): title=%q artists=%v album=%q date=%q", out.Title, out.Artists, out.Album, out.Date)
	if out.Title != "勇气" || len(out.Artists) == 0 || out.Artists[0] != "梁静茹" {
		t.Errorf("含梁静茹版融合错误: title=%q artists=%v", out.Title, out.Artists)
	}

	// 5b. 融合：候选是繁体"梁靜茹"版（简繁归一后应正确匹配）
	resultsTrad := []scrape.SearchResult{
		{Title: "勇氣", Artists: []string{"梁靜茹"}, Album: "勇氣", Date: "2000"},
		{Title: "勇气 (2018春晚表演歌曲)", Artists: []string{"派伟俊"}, Album: "2018春晚表演歌曲", Date: "2018"},
	}
	outTrad := fuseMetadata(resultsTrad, "勇气", "梁静茹")
	t.Logf("fused(繁体梁靜茹版): title=%q artists=%v album=%q", outTrad.Title, outTrad.Artists, outTrad.Album)
	// 融合输出保持候选原始名（繁体），后续 fixSrc 统一转简体；这里验证艺人已正确命中
	if len(outTrad.Artists) == 0 || scrape.ArtistSimilar(outTrad.Artists, []string{"梁静茹"}) < 0.5 {
		t.Errorf("繁体候选匹配失败: artists=%v", outTrad.Artists)
	}
	if outTrad.Title == "" || outTrad.Album == "" {
		t.Errorf("繁体候选字段未采用: title=%q album=%q", outTrad.Title, outTrad.Album)
	}

	// 6. 融合：候选只有异名版时（源没返回梁静茹版）——修复后应拒绝污染，
	//    只保留干净标题"勇气"，专辑/日期等不写（保留原标签）。
	results2 := []scrape.SearchResult{
		{Title: "勇气 (2018春晚表演歌曲)", Artists: []string{"派伟俊"}, Album: "2018春晚表演歌曲", Date: "2018"},
		{Title: "勇气", Artists: []string{"棉子"}, Album: "恋爱的力量 Disc1", Date: "2020-03-22", TrackNumber: 6},
	}
	out2 := fuseMetadata(results2, "勇气", "梁静茹")
	t.Logf("fused(仅异名版): title=%q artists=%v album=%q date=%q", out2.Title, out2.Artists, out2.Album, out2.Date)
	if out2.Title != "勇气" || out2.Album != "" || out2.Date != "" || out2.TrackNumber != 0 {
		t.Errorf("仅异名版未被正确拒绝: title=%q artists=%v album=%q date=%q track=%d",
			out2.Title, out2.Artists, out2.Album, out2.Date, out2.TrackNumber)
	}
}
