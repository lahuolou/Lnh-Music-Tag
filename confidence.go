// confidence.go：匹配置信度评分（P0 刮削可靠性）。
//
// candidateConfidence 把单个刮削候选与目标（标题 + 艺人 + 时长）对齐，
// 输出 0-100 的匹配置信度，供：
//   - fuseMetadata 按字段择优时使用统一标尺（P0-3）；
//   - 低置信度队列判定（Confidence < lowConfidenceThreshold 时标记待人工确认，P0-4）。
//
// 权重：标题 50%（含括号版本归一后的 Dice 相似）、艺人 30%（ArtistSimilar）、
// 时长 20%（±3s 内满分、±10s 半分的梯形分）。目标维度缺失（艺人未知/时长未知）
// 时不扣分并把权重让给其余维度，避免"信息缺失"被误判为"不匹配"。
package main

import (
	"math"
	"strings"

	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/scrape"
)

// lowConfidenceThreshold 置信度低于该值视为"低置信度匹配"（入待确认队列）。
const lowConfidenceThreshold = 55

// candidateConfidence 计算候选与目标的匹配置信度（0-100）。
func candidateConfidence(r scrape.SearchResult, wantTitle, wantArtist string, wantDur int) int {
	wantArtists := []string{}
	if a := strings.TrimSpace(wantArtist); a != "" && !looksUnknown(a) && !looksGarbled(a) {
		wantArtists = []string{a}
	}

	tSim := titleSimilar(r.Title, wantTitle)

	aSim := 1.0 // 目标艺人未知时不扣分
	if len(wantArtists) > 0 {
		aSim = scrape.ArtistSimilar(r.Artists, wantArtists)
	}

	dSim := 1.0 // 时长信息缺失不扣分
	haveDur := wantDur > 0 && r.Duration > 0
	if haveDur {
		diff := math.Abs(float64(r.Duration - wantDur))
		switch {
		case diff <= 3:
			dSim = 1
		case diff <= 10:
			dSim = 0.5
		default:
			dSim = 0
		}
	}

	// 归一化权重：只有实际可比的维度参与
	tw, aw, dw := 0.5, 0.3, 0.2
	if !haveDur {
		tw, aw, dw = 0.6, 0.4, 0.0
	}
	if len(wantArtists) == 0 {
		tw, aw = 0.8, 0.0 // dw 保持：时长维度照常计分
	}

	score := tSim*tw*100 + aSim*aw*100 + dSim*dw*100
	return int(math.Round(score))
}

// trackDuration 取 Track 的时长（秒）；无时长返回 0（置信度不计时长维度）。
func trackDuration(t *model.Track) int {
	if t == nil || t.Duration <= 0 {
		return 0
	}
	return int(t.Duration)
}

// isLowConfidence 判断融合结果是否低置信度（待人工确认）。
// 0 表示未评分（例如仅补歌词/无元数据路径），不进队列；仅评分后且低于阈值才入队。
func isLowConfidence(sr scrape.SearchResult) bool {
	return sr.Confidence > 0 && sr.Confidence < lowConfidenceThreshold
}
