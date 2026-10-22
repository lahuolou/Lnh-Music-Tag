package main

import (
	"sync"
	"time"
)

// entityCacheTTL 网关实体响应缓存 TTL：Subsonic getSong / Navidrome song /
// Jellyfin/Emby Items 单曲详情在客户端同一浏览会话中可能被重复请求
// （同一首歌被播放队列、详情页、列表反复拉取），缓存序列化后的完整
// 响应字节 30 秒，跳过重复的实体组装与 XML/JSON 序列化。
// 曲库结构、标签、收藏、评分变更时主动清空（entityCacheClear），
// 保证用户编辑/收藏后立即看到新值，不会出现"改完没变化"。
const entityCacheTTL = 30 * time.Second

// entityCacheMax 缓存条目上限（单曲实体约 1-3KB，1024 条约几 MB）。
// 达到上限时整表重建——写入低频、清空无副作用，简单淘汰足够。
const entityCacheMax = 1024

var (
	entityMu   sync.Mutex
	entityResp = map[string]entityCacheEntry{}
)

type entityCacheEntry struct {
	data []byte
	at   time.Time
}

// entityCacheGet 命中且未过期返回缓存字节；过期或缺失返回 false 并清理过期项。
func entityCacheGet(key string) ([]byte, bool) {
	entityMu.Lock()
	defer entityMu.Unlock()
	e, ok := entityResp[key]
	if !ok {
		return nil, false
	}
	if time.Since(e.at) >= entityCacheTTL {
		delete(entityResp, key)
		return nil, false
	}
	return e.data, true
}

// entityCachePut 写入缓存；超上限时整表重建（简单 FIFO 淘汰）。
func entityCachePut(key string, data []byte) {
	entityMu.Lock()
	defer entityMu.Unlock()
	if len(entityResp) >= entityCacheMax {
		entityResp = map[string]entityCacheEntry{}
	}
	entityResp[key] = entityCacheEntry{data: data, at: time.Now()}
}

// entityCacheClear 清空全部实体缓存。曲库结构（增删曲目）、标签刷新、
// 收藏/评分变更时调用——缓存是纯优化，清空仅失去命中，无副作用。
func entityCacheClear() {
	entityMu.Lock()
	entityResp = map[string]entityCacheEntry{}
	entityMu.Unlock()
}
