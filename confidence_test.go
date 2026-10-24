package main

import (
	"testing"

	"lnh-music-tag/internal/scrape"
)

func TestCandidateConfidencePerfect(t *testing.T) {
	// 标题 + 艺人 + 时长全部对齐 → 100
	r := scrape.SearchResult{Title: "勇气", Artists: []string{"梁静茹"}, Duration: 230}
	if got := candidateConfidence(r, "勇气", "梁静茹", 230); got != 100 {
		t.Fatalf("完全匹配应 100，got %d", got)
	}
}

func TestCandidateConfidenceSameTitleWrongArtist(t *testing.T) {
	// 同名异艺人（标题强匹配但艺人不同）：标题 50 + 艺人 0 + 时长 20 = 70
	r := scrape.SearchResult{Title: "勇气", Artists: []string{"派伟俊"}, Duration: 230}
	if got := candidateConfidence(r, "勇气", "梁静茹", 230); got != 70 {
		t.Fatalf("同名异艺人应 70，got %d", got)
	}
}

func TestCandidateConfidenceArtistMatchTitleUnrelated(t *testing.T) {
	// 艺人匹配但标题完全无关：0 + 30 + 20 = 50
	r := scrape.SearchResult{Title: "Faded", Artists: []string{"梁静茹"}, Duration: 230}
	if got := candidateConfidence(r, "勇气", "梁静茹", 230); got != 50 {
		t.Fatalf("艺人匹配标题无关应 50，got %d", got)
	}
}

func TestCandidateConfidenceUnknownArtistNoPenalty(t *testing.T) {
	// 目标艺人未知：不扣艺人分，权重归一（标题 80 + 时长 20）= 100
	r := scrape.SearchResult{Title: "勇气", Duration: 230}
	if got := candidateConfidence(r, "勇气", "", 230); got != 100 {
		t.Fatalf("目标艺人未知应 100，got %d", got)
	}
}

func TestCandidateConfidenceDurationMismatch(t *testing.T) {
	// 时长差 70s：时长维度归零 → 50 + 30 + 0 = 80
	r := scrape.SearchResult{Title: "勇气", Artists: []string{"梁静茹"}, Duration: 300}
	if got := candidateConfidence(r, "勇气", "梁静茹", 230); got != 80 {
		t.Fatalf("时长差大应 80，got %d", got)
	}
	// 时长差 8s：半分区 → 50 + 30 + 10 = 90
	r2 := scrape.SearchResult{Title: "勇气", Artists: []string{"梁静茹"}, Duration: 238}
	if got := candidateConfidence(r2, "勇气", "梁静茹", 230); got != 90 {
		t.Fatalf("时长差 8s 应 90，got %d", got)
	}
}

func TestCandidateConfidenceLowThreshold(t *testing.T) {
	// 标题、艺人、时长全部不匹配 → 显著低于低置信度阈值
	r := scrape.SearchResult{Title: "Faded", Artists: []string{"Alan Walker"}, Duration: 210}
	if got := candidateConfidence(r, "勇气", "梁静茹", 230); got >= lowConfidenceThreshold {
		t.Fatalf("完全不匹配应低于 %d，got %d", lowConfidenceThreshold, got)
	}
}

func TestIsLowConfidence(t *testing.T) {
	cases := []struct {
		name       string
		confidence int
		want       bool
	}{
		{"未评分（0）不进队列", 0, false},
		{"低置信度", 40, true},
		{"边界下", 54, true},
		{"边界", 55, false},
		{"高置信度", 100, false},
	}
	for _, c := range cases {
		if got := isLowConfidence(scrape.SearchResult{Confidence: c.confidence}); got != c.want {
			t.Errorf("%s：isLowConfidence(%d) = %v，want %v", c.name, c.confidence, got, c.want)
		}
	}
}
