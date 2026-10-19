package dedup

import (
	"testing"

	"lnh-music-tag/internal/model"
)

func TestGroupBySameName(t *testing.T) {
	tracks := []*model.Track{
		{ID: "a", FileName: "广岛之恋.mp3", Ext: ".mp3", Bitrate: 192, SampleRate: 44100},
		{ID: "b", FileName: "广岛之恋.flac", Ext: ".flac", Bitrate: 1000, SampleRate: 96000},
		{ID: "c", FileName: "独奏曲.mp3", Ext: ".mp3", Bitrate: 320, SampleRate: 44100},
		{ID: "d", FileName: "独奏曲.ogg", Ext: ".ogg", Bitrate: 400, SampleRate: 48000},
	}
	groups := GroupBySameName(tracks)
	if len(groups) != 2 {
		t.Fatalf("期望 2 组，得到 %d", len(groups))
	}
	for _, g := range groups {
		if g.Method != "format" {
			t.Fatalf("method 应为 format: %v", g.Method)
		}
		var best *model.Track
		for _, tr := range tracks {
			for _, id := range g.IDs {
				if tr.ID == id {
					if best == nil || qualityScore(tr) > qualityScore(best) {
						best = tr
					}
				}
			}
		}
		if g.KeepID != best.ID {
			t.Fatalf("组 %q KeepID=%s 应为最佳 %s", g.Key, g.KeepID, best.ID)
		}
	}
	// 音质最佳者：flac 胜过 mp3
	for _, g := range groups {
		if g.Key == "广岛之恋" && g.KeepID != "b" {
			t.Fatalf("广岛之恋 应保留 flac(b)，得到 %s", g.KeepID)
		}
	}
}

func TestGroupByArtistTitle(t *testing.T) {
	tracks := []*model.Track{
		{ID: "a", FileName: "a.mp3", Ext: ".mp3", Bitrate: 192, SampleRate: 44100, Tags: map[string]string{"ARTIST": "陈小春", "TITLE": "街角的晚风"}},
		{ID: "b", FileName: "b.flac", Ext: ".flac", Bitrate: 1000, SampleRate: 96000, Tags: map[string]string{"ARTIST": "陈小春", "TITLE": "街角的晚风"}},
		{ID: "c", FileName: "c.mp3", Ext: ".mp3", Bitrate: 320, SampleRate: 44100, Tags: map[string]string{"ARTIST": "别人", "TITLE": "别的歌"}},
		{ID: "d", FileName: "d.mp3", Ext: ".mp3", Bitrate: 128, SampleRate: 44100, Tags: map[string]string{"ARTIST": "陈小春", "TITLE": ""}}, // 缺标题不参与
	}
	groups := GroupByArtistTitle(tracks)
	if len(groups) != 1 {
		t.Fatalf("期望 1 组，得到 %d", len(groups))
	}
	g := groups[0]
	if g.Method != "tags" {
		t.Fatalf("method 应为 tags: %v", g.Method)
	}
	if g.Key != "陈小春 - 街角的晚风" {
		t.Fatalf("key 应为 艺术家 - 标题: %s", g.Key)
	}
	if g.KeepID != "b" {
		t.Fatalf("应保留 flac(b)，得到 %s", g.KeepID)
	}
	if len(g.IDs) != 2 {
		t.Fatalf("应只有 2 首参与: %v", g.IDs)
	}
}

// 用户反馈：同一艺术家的多首不同歌不得被误判为重复组。
func TestGroupByArtistTitleNoFalseGroup(t *testing.T) {
	songs := []string{"Mojito", "一路向北", "七里香", "发如雪", "告白气球", "夜曲", "大笨钟", "安静", "搁浅", "晴天", "稻香", "青花瓷"}
	tracks := []*model.Track{}
	for i, s := range songs {
		tracks = append(tracks, &model.Track{
			ID: string(rune('a' + i)), FileName: "周杰伦 - " + s + ".flac", Ext: ".flac",
			Bitrate: 900, SampleRate: 96000, Tags: map[string]string{"ARTIST": "周杰伦", "TITLE": s},
		})
	}
	groups := GroupByArtistTitle(tracks)
	if len(groups) != 0 {
		t.Fatalf("12 首不同歌不应产生任何重复组，得到 %d 组", len(groups))
	}
}

// 空标签时从“艺术家 - 标题”文件名解析，同名同艺术家仍能判重。
func TestGroupByArtistTitleFallsBackToFileName(t *testing.T) {
	tracks := []*model.Track{
		{ID: "a", FileName: "陈慧娴 - 傻女.mp3", Ext: ".mp3", Bitrate: 192, SampleRate: 44100},
		{ID: "b", FileName: "陈慧娴 - 傻女.flac", Ext: ".flac", Bitrate: 1000, SampleRate: 96000},
		{ID: "c", FileName: "陈慧娴 - 月亮.flac", Ext: ".flac", Bitrate: 974, SampleRate: 44100},
		{ID: "d", FileName: "陈慧娴 - 飘雪.flac", Ext: ".flac", Bitrate: 829, SampleRate: 44100},
	}
	groups := GroupByArtistTitle(tracks)
	if len(groups) != 1 {
		t.Fatalf("期望 1 组（傻女两版本），得到 %d", len(groups))
	}
	if groups[0].Key != "陈慧娴 - 傻女" {
		t.Fatalf("key 应为 陈慧娴 - 傻女: %s", groups[0].Key)
	}
	if groups[0].KeepID != "b" {
		t.Fatalf("应保留 flac(b)，得到 %s", groups[0].KeepID)
	}
}
