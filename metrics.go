// metrics.go：轻量运行时指标计数器，供 /api/metrics 文本输出（Prometheus 风格）。
// 仅进程内累计，无持久化、无外部依赖；供自托管用户快速判断后台任务是否推进、
// 流媒体网关是否有请求、曲库规模与缓存状况。
package main

import "sync/atomic"

var (
	// metricScrapeTotal 刮削/一键类任务执行次数；metricScrapeSuccess 其中失败数为 0 的任务数。
	metricScrapeTotal   atomic.Int64
	metricScrapeSuccess atomic.Int64
	// metricGatewayReq 流媒体网关（Subsonic/Jellyfin/Emby/Navidrome）请求总数。
	metricGatewayReq atomic.Int64
)

// bumpScrapeMetric 记录一次刮削类任务结果，在 addLog 挂点调用。
func bumpScrapeMetric(act string, ok bool) {
	switch act {
	case "act_scrape", "act_onekey":
		metricScrapeTotal.Add(1)
		if ok {
			metricScrapeSuccess.Add(1)
		}
	}
}
