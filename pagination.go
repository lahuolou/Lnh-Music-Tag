// pagination.go：四协议（Subsonic / Navidrome / Jellyfin / Emby）共享分页解析与边界约束。
// 强制回归契约（不可破坏）：
//   - 负数参数在解析层 clamp 到 0（不能只在切片处补救）；
//   - 单页上限 maxPageSize=1000，防止异常大 Limit/_end 拖垮序列化；
//   - 非法字符串 / 缺失参数按 0 处理；
//   - 切片层仍由 sliceRange 做第二道防御（调用方保持），
//   - 空列表由各协议序列化为 []（非 null）。
package main

import (
	"net/http"
	"strconv"
)

// maxPageSize 单页最大记录数：防异常大分页参数导致全量切片/序列化。
const maxPageSize = 1000

// clampStartLimit 将 Jellyfin/Subsonic 风格的 (StartIndex, Limit) 归一为合法值：
// start 下限 0；limit<=0 表示不限制（取到结尾），limit>maxPageSize 截断为上限。
func clampStartLimit(start, limit int) (int, int) {
	if start < 0 {
		start = 0
	}
	if limit > maxPageSize {
		limit = maxPageSize
	}
	return start, limit
}

// parseStartLimit 从查询参数读取起始与页大小（支持多种拼写，按顺序取首个合法值）。
// 非法字符串按 0 处理，返回前经 clampStartLimit 归一。
func parseStartLimit(r *http.Request, startKeys, limitKeys []string) (int, int) {
	var start, limit int
	for _, k := range startKeys {
		if v, err := strconv.Atoi(r.URL.Query().Get(k)); err == nil {
			start = v
			break
		}
	}
	for _, k := range limitKeys {
		if v, err := strconv.Atoi(r.URL.Query().Get(k)); err == nil {
			limit = v
			break
		}
	}
	return clampStartLimit(start, limit)
}

// parseLimit 仅解析页大小（Subsonic 专辑/搜索类接口），语义同 parseStartLimit 的 limit。
func parseLimit(r *http.Request, limitKeys []string) int {
	_, limit := parseStartLimit(r, nil, limitKeys)
	return limit
}
