// health.go：来源健康度 / 自适应退避 / 限流熔断（P0 刮削可靠性）。
//
// 每个刮削源独立跟踪成功率与连续失败。连续失败达到阈值后进入熔断：
//   - 熔断期间 SearchWithFallback / FetchCoverFallback / FetchLyrics 直接跳过该源，
//     不发起网络请求（限流源、死源、403/5xx 源不再拖慢批量流水线）；
//   - 冷却采用指数退避：连续失败越多，恢复等待越长；
//   - 任一次成功立即清零连续失败与冷却（源恢复即时生效）。
//
// 全部状态进程内维护，无持久化；熔断只影响调度决策，不改变源行为本身。
package scrape

import (
	"sync"
	"time"
)

const (
	// maxConsecutiveFails 连续失败次数达到该值即触发熔断。
	maxConsecutiveFails = 3
	// baseCooldown 熔断基础冷却；连续失败每 +1 冷却翻倍，封顶 maxCooldown。
	baseCooldown = 5 * time.Minute
	// maxCooldown 冷却封顶（约 30 分钟），防止失败源被长期隔离。
	maxCooldown = 30 * time.Minute
)

// srcHealth 单个来源的健康状态（内部使用，互斥保护）。
type srcHealth struct {
	mu          sync.Mutex
	consecFails int
	successes   int
	fails       int
	lastFail    time.Time
	cooldown    time.Duration
}

// probe 判断当前是否可用（熔断窗口内返回 false）。
// 熔断已过期时自动恢复（清零连续失败与冷却），返回 true。
func (h *srcHealth) probe(now time.Time) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.consecFails < maxConsecutiveFails {
		return true
	}
	if now.Sub(h.lastFail) >= h.cooldown {
		h.consecFails = 0
		h.cooldown = 0
		return true
	}
	return false
}

// recordSuccess 记录一次成功：清零连续失败与冷却。
func (h *srcHealth) recordSuccess(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.successes++
	h.consecFails = 0
	h.cooldown = 0
}

// recordFailure 记录一次失败：连续失败 +1，冷却按指数退避递增。
func (h *srcHealth) recordFailure(now time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.fails++
	h.consecFails++
	h.lastFail = now
	if h.cooldown == 0 {
		h.cooldown = baseCooldown
	} else {
		h.cooldown *= 2
		if h.cooldown > maxCooldown {
			h.cooldown = maxCooldown
		}
	}
}

// SourceHealthState 来源健康状态快照（供诊断/指标输出）。
type SourceHealthState struct {
	Successes   int   `json:"successes"`
	Fails       int   `json:"fails"`
	ConsecFails int   `json:"consecFails"`
	CoolingDown bool  `json:"coolingDown"`
	Cooldown    int64 `json:"cooldownSecs"` // 剩余冷却秒数，0 表示无
}

// now 可注入时钟（测试用）；生产默认 time.Now。
var now = time.Now

// sourceHealth 返回指定来源的健康状态（懒初始化）。
func (m *MultiSource) sourceHealth(name string) *srcHealth {
	m.healthMu.Lock()
	defer m.healthMu.Unlock()
	if m.health[name] == nil {
		m.health[name] = &srcHealth{}
	}
	return m.health[name]
}

// recordSourceSuccess / recordSourceFailure 由各调用链在源调用完成后记录。
func (m *MultiSource) recordSourceSuccess(name string) {
	m.sourceHealth(name).recordSuccess(now())
}

func (m *MultiSource) recordSourceFailure(name string) {
	m.sourceHealth(name).recordFailure(now())
}

// SourceHealthy 判断来源当前是否可用（熔断中返回 false，调用链据此跳过）。
func (m *MultiSource) SourceHealthy(name string) bool {
	return m.sourceHealth(name).probe(now())
}

// SourceHealthStates 导出全部注册来源的健康快照（供 /api/metrics 或诊断端点）。
func (m *MultiSource) SourceHealthStates() map[string]SourceHealthState {
	m.emu.RLock()
	names := make([]string, 0, len(m.order))
	for _, n := range m.order {
		names = append(names, n)
	}
	m.emu.RUnlock()
	out := map[string]SourceHealthState{}
	for _, n := range names {
		h := m.sourceHealth(n)
		h.mu.Lock()
		st := SourceHealthState{
			Successes:   h.successes,
			Fails:       h.fails,
			ConsecFails: h.consecFails,
		}
		if h.consecFails >= maxConsecutiveFails {
			remaining := h.cooldown - now().Sub(h.lastFail)
			st.CoolingDown = true
			if remaining > 0 {
				st.Cooldown = int64(remaining / time.Second)
			}
		}
		h.mu.Unlock()
		out[n] = st
	}
	return out
}
