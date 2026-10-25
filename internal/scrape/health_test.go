package scrape

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSource 测试假源：可配置失败/成功，记录调用次数。
type fakeSource struct {
	name string
	ok   bool
}

func (f fakeSource) Name() string  { return f.name }
func (f fakeSource) Label() string { return f.name }
func (f fakeSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	if !f.ok {
		return nil, errors.New("fake failure")
	}
	return []SearchResult{{Source: f.name, Title: query}}, nil
}

// breakerMulti 构造一个仅含 netease（可死）+ itunes（常活）的多源，便于控制回退链。
func breakerMulti(neteaseOK bool, dead *atomic.Int64, alive *atomic.Int64) *MultiSource {
	m := &MultiSource{
		client:  NewClient(),
		byName:  map[string]Source{},
		order:   []string{"netease", "itunes"},
		enabled: map[string]bool{"netease": true, "itunes": true},
		health:  map[string]*srcHealth{},
	}
	m.byName["netease"] = countingSource{name: "netease", ok: neteaseOK, calls: dead}
	m.byName["itunes"] = countingSource{name: "itunes", ok: true, calls: alive}
	return m
}

// countingSource 在 fakeSource 基础上记录调用次数（原子）。
type countingSource struct {
	name  string
	ok    bool
	calls *atomic.Int64
}

func (f countingSource) Name() string  { return f.name }
func (f countingSource) Label() string { return f.name }
func (f countingSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	f.calls.Add(1)
	if !f.ok {
		return nil, errors.New("fake failure")
	}
	return []SearchResult{{Source: f.name, Title: query}}, nil
}

// 单源熔断：连续 3 次失败后 probe=false；冷却过期自动恢复；成功立即清零。
func TestSourceHealthCircuitBreaker(t *testing.T) {
	m := breakerMulti(false, &atomic.Int64{}, &atomic.Int64{})
	if !m.SourceHealthy("netease") {
		t.Fatal("初始应可用")
	}
	for i := 0; i < 3; i++ {
		m.recordSourceFailure("netease")
	}
	if m.SourceHealthy("netease") {
		t.Fatal("连续 3 次失败后应熔断")
	}
	if m.SourceHealthy("netease") {
		t.Fatal("熔断窗口内应保持熔断")
	}
	// 冷却过期自动恢复
	old := now
	now = func() time.Time { return old().Add(40 * time.Minute) }
	defer func() { now = old }()
	if !m.SourceHealthy("netease") {
		t.Fatal("冷却过期应自动恢复")
	}
	// 成功立即清零
	m.recordSourceFailure("netease")
	m.recordSourceFailure("netease")
	m.recordSourceSuccess("netease")
	if !m.SourceHealthy("netease") {
		t.Fatal("成功应清零连续失败")
	}
}

// 指数退避：冷却时长递增且封顶 maxCooldown。
func TestSourceHealthExponentialBackoff(t *testing.T) {
	m := breakerMulti(false, &atomic.Int64{}, &atomic.Int64{})
	// 第 1 次：5m；第 2 次：10m（递增）
	for i := 0; i < 2; i++ {
		m.recordSourceFailure("netease")
	}
	h := m.sourceHealth("netease")
	h.mu.Lock()
	cd := h.cooldown
	h.mu.Unlock()
	if want := baseCooldown * 2; cd != want {
		t.Fatalf("冷却应递增到 %v，got %v", want, cd)
	}
	// 再失败 2 次：40m 触发封顶 → 30m
	for i := 0; i < 2; i++ {
		m.recordSourceFailure("netease")
	}
	h.mu.Lock()
	cd = h.cooldown
	h.mu.Unlock()
	if cd != maxCooldown {
		t.Fatalf("冷却应封顶 %v，got %v", maxCooldown, cd)
	}
}

// SearchWithFallback：熔断源被跳过（不发请求），正常源照常返回并记录成功；
// 冷却过期后熔断源重新参与。
func TestSearchWithFallbackSkipsBreaker(t *testing.T) {
	dead := &atomic.Int64{}
	alive := &atomic.Int64{}
	m := breakerMulti(false, dead, alive)
	// 先把 netease 打到熔断
	for i := 0; i < 3; i++ {
		m.recordSourceFailure("netease")
	}
	res, err := m.SearchWithFallback("auto", "测试 歌曲", 3)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if dead.Load() != 0 {
		t.Fatalf("熔断源不应收到请求，calls=%d", dead.Load())
	}
	if alive.Load() == 0 {
		t.Fatal("正常源应收到请求")
	}
	if len(res) == 0 {
		t.Fatal("应返回正常源结果")
	}
	// 冷却过期：dead 源应重新参与
	old := now
	now = func() time.Time { return old().Add(40 * time.Minute) }
	defer func() { now = old }()
	before := dead.Load()
	res, err = m.SearchWithFallback("auto", "测试 歌曲", 3)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if dead.Load() != before+1 {
		t.Fatalf("恢复后 netease 源应重新参与，calls=%d", dead.Load())
	}
	if len(res) == 0 {
		t.Fatal("应返回结果")
	}
	// 恢复后成功记录：熔断解除
	if !m.SourceHealthy("netease") {
		t.Fatal("重新参与成功应解除熔断")
	}
}
