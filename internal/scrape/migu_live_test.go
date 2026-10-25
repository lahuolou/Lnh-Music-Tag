package scrape

import (
	"strings"
	"testing"
)

// TestMiguLive hits the real Migu endpoint to make sure the current search API
// shape and encoding still parse (guards against silent upstream changes).
// 依赖真实外网（CI 环境无外网），-short 模式跳过；本地全量测试（go test ./...）仍执行。
func TestMiguLive(t *testing.T) {
	if testing.Short() {
		t.Skip("外部网络测试：-short 模式跳过（CI 无外网）")
	}
	c := NewClient()
	s := miguSource{}
	res, err := s.Search(c, "记忆", 3)
	if err != nil {
		t.Fatalf("search err: %v", err)
	}
	if len(res) == 0 {
		t.Fatalf("应返回结果")
	}
	found := false
	for _, r := range res {
		if strings.Contains(r.Title, "记忆") && !strings.ContainsRune(r.Title, '\uFFFD') {
			found = true
		}
	}
	if !found {
		t.Fatalf("应至少有一条正常中文结果，得到 %v", res)
	}
}
