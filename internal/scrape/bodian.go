package scrape

// bodianSource is 波点音乐 (NetEase's streaming music product). It exposes no
// stable public search endpoint, so it is registered for the source list but
// returns no results; "auto" will transparently fall through to other sources.
type bodianSource struct{}

func (bodianSource) Name() string  { return "bodian" }
func (bodianSource) Label() string { return "波点音乐" }

func (bodianSource) Search(c *Client, query string, limit int) ([]SearchResult, error) {
	// 无公开搜索接口，返回空结果（auto 会继续尝试其他源）
	return []SearchResult{}, nil
}
