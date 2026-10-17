package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"lnh-music-tag/internal/model"
	"lnh-music-tag/internal/scrape"
	cstore "lnh-music-tag/internal/store"
	"lnh-music-tag/internal/taglibx"
)

// Batch scraping runs in a background job so the UI can show progress.
// Sources are rate-limited, so a short delay is applied between tracks when
// scraping more than one.

// ScrapeFields selects which metadata fields to write during scraping.
// 顺序即前端展示顺序（常用优先）：核心标签 → 扩展标签 → 操作项 → 音频属性。
type ScrapeFields struct {
	// 核心标签
	Title       bool
	Artist      bool
	Album       bool
	AlbumArtist bool
	Genre       bool
	Year        bool
	TrackNumber bool
	Lyrics      bool
	Cover       bool
	// 扩展标签
	Composer   bool
	Lyricist   bool
	AlbumType  bool
	Language   bool
	DiscNumber bool
	DiscTotal  bool
	TrackTotal bool
	Comment    bool
	TrackGain  bool
	AlbumGain  bool
	TrackPeak  bool
	AlbumPeak  bool
	// 操作项
	FileName bool // 按「艺术家 - 标题.后缀」重命名
	// 音频属性（写标签自定义键）
	Bitrate    bool
	SampleRate bool
	BitDepth   bool
	Channels   bool
	Duration   bool
	FileSize   bool
}

// fieldNames 返回按常用度排序的全部字段名（前端展示与后端解析共用）。
// 常用优先：海报 → 核心标签 → 操作项（文件名重命名）→ 扩展标签 → 音频属性。
// UI 中文别名在此映射；用户清单中"简介/描述"合并为 COMMENT 一项。
func (f ScrapeFields) fieldNames() []string {
	return []string{
		"cover", "title", "artist", "album", "albumartist", "genre", "year",
		"tracknumber", "lyrics", "filename", "composer", "lyricist",
		"albumtype", "language", "comment", "discnumber", "disctotal", "tracktotal",
		"trackgain", "albumgain", "trackpeak", "albumpeak",
		"bitrate", "samplerate", "bitdepth", "channels", "duration", "filesize",
	}
}

// allMetadataFields returns fields with every metadata tag enabled but no
// cover/lyrics (which are controlled separately by the caller's fetch flags).
func allMetadataFields() ScrapeFields {
	return ScrapeFields{
		Title: true, Artist: true, Album: true, AlbumArtist: true,
		Genre: true, Year: true, TrackNumber: true,
		Composer: true, Lyricist: true, AlbumType: true, Language: true,
		DiscNumber: true, DiscTotal: true, TrackTotal: true, Comment: true,
		TrackGain: true, AlbumGain: true, TrackPeak: true, AlbumPeak: true,
		Bitrate: true, SampleRate: true, BitDepth: true, Channels: true,
		Duration: true, FileSize: true,
	}
}

// allScrapeFields 一键刮削使用的完整字段集：与批量刮削元素全部字段一致
// （含海报、歌词、文件名重命名 + 全部扩展标签与音频属性）。
func allScrapeFields() ScrapeFields {
	f := allMetadataFields()
	f.Cover = true
	f.Lyrics = true
	f.FileName = true
	return f
}

// fieldsFromList converts an explicit list of selected field names (from the
// UI) into ScrapeFields. An empty list falls back to all metadata plus the
// legacy cover/lyrics fetch flags.
func fieldsFromList(list []string, fetchCover, fetchLyrics bool) ScrapeFields {
	if len(list) == 0 {
		f := allMetadataFields()
		f.Cover = fetchCover
		f.Lyrics = fetchLyrics
		return f
	}
	set := map[string]bool{}
	for _, s := range list {
		set[strings.ToLower(strings.TrimSpace(s))] = true
	}
	has := func(keys ...string) bool {
		for _, k := range keys {
			if set[k] {
				return true
			}
		}
		return false
	}
	return ScrapeFields{
		Cover:       has("cover", "海报", "封面"),
		Title:       has("title", "歌名", "标题"),
		Artist:      has("artist", "艺术家"),
		Album:       has("album", "专辑"),
		AlbumArtist: has("albumartist", "专辑艺术家"),
		Genre:       has("genre", "流派", "风格"),
		Year:        has("year", "年代", "年份"),
		TrackNumber: has("tracknumber", "曲目号", "音轨号"),
		Lyrics:      has("lyrics", "歌词", "lrcfile", "歌词(lrc文件)", "lrc"), // 内置方式：抓歌词写内嵌标签（兼容旧 LRC 别名）
		Composer:    has("composer", "作曲家"),
		Lyricist:    has("lyricist", "作词家"),
		AlbumType:   has("albumtype", "专辑类型"),
		Language:    has("language", "语言"),
		DiscNumber:  has("discnumber", "光盘编号"),
		DiscTotal:   has("disctotal", "总光盘数"),
		TrackTotal:  has("tracktotal", "总音轨数"),
		Comment:     has("comment", "简介", "描述"),
		TrackGain:   has("trackgain", "曲目增益"),
		AlbumGain:   has("albumgain", "专辑增益"),
		TrackPeak:   has("trackpeak", "曲目峰值"),
		AlbumPeak:   has("albumpeak", "专辑峰值"),
		FileName:    has("filename", "文件名"),
		Bitrate:     has("bitrate", "比特率"),
		SampleRate:  has("samplerate", "采样率"),
		BitDepth:    has("bitdepth", "位深度"),
		Channels:    has("channels", "声道"),
		Duration:    has("duration", "时长"),
		FileSize:    has("filesize", "文件大小"),
	}
}

// trackHasField reports whether a track already carries a usable value for
// field. Mojibake text is NOT a usable value: garbled tags are treated as
// missing so scraping/repair can overwrite them (previously the smart-skip
// protected broken labels and the "乱码修复/批量补全" never touched them).
// "Unknown"/"未知" placeholder values are also treated as missing, so files
// like "Unknown - 开始恋爱.flac" still get their real artist scraped and the
// filename renamed accordingly.
// field is a taglibx constant (e.g. taglibx.Title) or "cover"/"lyrics".
func trackHasField(t *model.Track, field string) bool {
	switch field {
	case "cover":
		return t.HasCover
	case "lyrics":
		v := strings.TrimSpace(t.Tags["LYRICS"])
		return v != "" && !looksGarbledText(v) && !looksUnknown(v)
	default:
		v := strings.TrimSpace(t.Tags[field])
		return v != "" && !looksGarbledText(v) && !looksUnknown(v)
	}
}

// applySkipFilled drops fields whose value the track already has. It returns
// true if at least one field remains to be written.
// tagNeedsFill 判定某标签是否"需要补全"：字段为空、"Unknown"占位、乱码，
// 或与文件名解析出的标题/艺术家不一致（如 "Unknown - 蓝色蝴蝶.mp3" 的标签
// 残留错误内容）——都视为未填充，刮削阶段会继续修正而不是智能跳过。
func tagNeedsFill(t *model.Track, k string) bool {
	v := strings.TrimSpace(t.Tags[k])
	if v == "" || looksUnknown(v) || looksGarbled(v) {
		return true
	}
	fa, ft := parseFileNameArtistTitle(t.FileName)
	switch k {
	case taglibx.Title:
		return ft != "" && v != ft
	case taglibx.Artist:
		// 文件名只有 "Unknown" 占位（无真实艺术家）时，标签艺术家不可信，
		// 一律交给刮削阶段按歌名补全（如 "Unknown - 蓝色蝴蝶.mp3" 修正标签）。
		// 文件名解析不出艺术家（fa 为空）时不做强制。
		if fa != "" && looksUnknown(fa) {
			return true
		}
		return fa != "" && !looksGarbled(fa) && v != fa
	}
	return false
}

func applySkipFilled(t *model.Track, f *ScrapeFields) bool {
	if f.Title && !tagNeedsFill(t, taglibx.Title) {
		f.Title = false
	}
	if f.Artist && !tagNeedsFill(t, taglibx.Artist) {
		f.Artist = false
	}
	if f.Album && trackHasField(t, taglibx.Album) {
		f.Album = false
	}
	if f.AlbumArtist && trackHasField(t, taglibx.AlbumArtist) {
		f.AlbumArtist = false
	}
	if f.Genre && trackHasField(t, taglibx.Genre) {
		f.Genre = false
	}
	if f.Year && trackHasField(t, taglibx.Date) {
		f.Year = false
	}
	if f.TrackNumber && trackHasField(t, taglibx.TrackNumber) {
		f.TrackNumber = false
	}
	if f.Cover && trackHasField(t, "cover") {
		f.Cover = false
	}
	if f.Lyrics && trackHasField(t, "lyrics") {
		f.Lyrics = false
	}
	// 扩展标签
	if f.Composer && trackHasField(t, taglibx.Composer) {
		f.Composer = false
	}
	if f.Lyricist && trackHasField(t, taglibx.Lyricist) {
		f.Lyricist = false
	}
	if f.AlbumType && trackHasField(t, taglibx.ReleaseType) {
		f.AlbumType = false
	}
	if f.Language && trackHasField(t, taglibx.Language) {
		f.Language = false
	}
	if f.DiscNumber && trackHasField(t, taglibx.DiscNumber) {
		f.DiscNumber = false
	}
	if f.DiscTotal && trackHasField(t, taglibx.DiscTotal) {
		f.DiscTotal = false
	}
	if f.TrackTotal && trackHasField(t, taglibx.TrackTotal) {
		f.TrackTotal = false
	}
	if f.Comment && trackHasField(t, taglibx.Comment) {
		f.Comment = false
	}
	if f.TrackGain && trackHasField(t, taglibx.ReplayGainTrackGain) {
		f.TrackGain = false
	}
	if f.AlbumGain && trackHasField(t, taglibx.ReplayGainAlbumGain) {
		f.AlbumGain = false
	}
	if f.TrackPeak && trackHasField(t, taglibx.ReplayGainTrackPeak) {
		f.TrackPeak = false
	}
	if f.AlbumPeak && trackHasField(t, taglibx.ReplayGainAlbumPeak) {
		f.AlbumPeak = false
	}
	// 操作项：文件名已是「艺术家-标题」则跳过
	if f.FileName {
		if !fileNameNeedsRename(t) {
			f.FileName = false
		}
	}
	// 音频属性：标签已有探测值则跳过
	if f.Bitrate && trackHasField(t, taglibx.Bitrate) {
		f.Bitrate = false
	}
	if f.SampleRate && trackHasField(t, taglibx.SampleRate) {
		f.SampleRate = false
	}
	if f.BitDepth && trackHasField(t, taglibx.BitDepth) {
		f.BitDepth = false
	}
	if f.Channels && trackHasField(t, taglibx.Channels) {
		f.Channels = false
	}
	if f.Duration && trackHasField(t, taglibx.Length) {
		f.Duration = false
	}
	if f.FileSize && trackHasField(t, taglibx.FileSize) {
		f.FileSize = false
	}
	return f.Cover || f.Title || f.Artist || f.Album || f.AlbumArtist ||
		f.Genre || f.Year || f.TrackNumber || f.Lyrics ||
		f.Composer || f.Lyricist || f.AlbumType || f.Language ||
		f.DiscNumber || f.DiscTotal || f.TrackTotal || f.Comment ||
		f.TrackGain || f.AlbumGain || f.TrackPeak || f.AlbumPeak ||
		f.FileName ||
		f.Bitrate || f.SampleRate || f.BitDepth || f.Channels || f.Duration || f.FileSize
}

type scrapeJob struct {
	ID      string         `json:"id"`
	Kind    string         `json:"kind"` // scrape | convert | identify | fixEnc | script | onekey
	Total   int            `json:"total"`
	Done    int            `json:"done"`
	Current string         `json:"current"`
	Status  string         `json:"status"` // "running" | "done" | "cancelled"
	Results []scrapeResult `json:"results"`
	Summary jobSummary     `json:"summary"`
	// CreatedAt 任务创建时间（落库与耗时统计用，restore 时从库回填）。
	CreatedAt time.Time `json:"createdAt"`
	// Stage 标识当前流水线阶段（仅 onekey 任务使用，如 fixEnc/script/artist/scrape）；
	// StageLabel 是该阶段面向用户的中文/英文文案（由后端按请求语言给出）。
	Stage      string `json:"stage,omitempty"`
	StageLabel string `json:"stageLabel,omitempty"`
	// HashDone/HashTotal 一键刮削「自动去重」阶段的 SHA256 哈希进度：
	// 哈希期间曲目 Done 不推进（哈希完才逐首去重），前端据此显示
	// "哈希 x/n" 而不是 0/947 假死。
	HashDone  int `json:"hashDone,omitempty"`
	HashTotal int `json:"hashTotal,omitempty"`
	// StageDone/StageTotal 当前流水线阶段内的进度（如去重阶段逐首判断、
	// 乱码修复逐首处理）。前端进度条优先显示"当前阶段"的进度，
	// 而不是全局"完成全部阶段的曲目数"，让用户直观看到阶段在推进。
	StageDone  int `json:"stageDone,omitempty"`
	StageTotal int `json:"stageTotal,omitempty"`
	// Duration 任务已耗时（秒，进度轮询时按 CreatedAt 计算；历史任务无创建时间时为 0）。
	Duration int `json:"duration,omitempty"`
	// Updated carries tracks whose tags/cover changed since the last poll.
	// Each progress poll consumes (clears) it so the UI can patch the list
	// incrementally instead of reloading the whole list.
	Updated []*model.Track `json:"updated"`
	mu      sync.Mutex
	// Cancel 由用户在前端点击“取消”置位；各任务循环在下一步前检查，
	// 发现后立即置 status="cancelled" 并退出，不再处理剩余曲目。
	Cancel bool `json:"-"`
}

type scrapeResult struct {
	ID       string `json:"id"`
	FileName string `json:"fileName"`
	OK       bool   `json:"ok"`
	Skip     bool   `json:"skip,omitempty"`
	Message  string `json:"message"`
}

// jobSummary 聚合计数：前端统计不再依赖遍历全量 results（大任务节省传输）。
type jobSummary struct {
	OK   int `json:"ok"`
	Fail int `json:"fail"`
	Skip int `json:"skip"`
}

// summarize 统计结果计数。
func summarize(results []scrapeResult) jobSummary {
	var s jobSummary
	for _, r := range results {
		switch {
		case r.OK:
			s.OK++
		case r.Skip:
			s.Skip++
		default:
			s.Fail++
		}
	}
	return s
}

// failResults 仅返回真失败项（skip 属预期行为，只进 summary 计数）。
func failResults(results []scrapeResult) []scrapeResult {
	out := make([]scrapeResult, 0, len(results))
	for _, r := range results {
		if !r.OK && !r.Skip {
			out = append(out, r)
		}
	}
	return out
}

type jobStore struct {
	mu   sync.Mutex
	jobs map[string]*scrapeJob
	db   *sql.DB // 持久化（nil 时跳过，测试用）
}

// newJobStore 创建任务表并恢复历史任务（重启前 running 的任务标记为
// interrupted，完成/取消的历史任务保留结果）。db 为 nil 时仅内存（测试）。
func newJobStore(db *sql.DB) *jobStore {
	js := &jobStore{jobs: map[string]*scrapeJob{}, db: db}
	js.initDB()
	js.restore()
	go js.autoPersist()
	return js
}

func (js *jobStore) initDB() {
	if js.db == nil {
		return
	}
	js.db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id TEXT PRIMARY KEY, kind TEXT NOT NULL, total INTEGER NOT NULL,
		done INTEGER NOT NULL, current TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
		stage TEXT NOT NULL DEFAULT '', stage_label TEXT NOT NULL DEFAULT '',
		results TEXT NOT NULL DEFAULT '[]', created_at TEXT NOT NULL, updated_at TEXT NOT NULL
	)`)
}

// persistedJob 是落库的任务快照（不含运行时字段 Updated/Cancel/mu）。
type persistedJob struct {
	ID         string         `json:"id"`
	Kind       string         `json:"kind"`
	Total      int            `json:"total"`
	Done       int            `json:"done"`
	Current    string         `json:"current"`
	Status     string         `json:"status"`
	Stage      string         `json:"stage"`
	StageLabel string         `json:"stageLabel"`
	Results    []scrapeResult `json:"results"`
	CreatedAt  string         `json:"createdAt"`
	UpdatedAt  string         `json:"updatedAt"`
}

func (j *scrapeJob) snapshot() persistedJob {
	j.mu.Lock()
	defer j.mu.Unlock()
	now := time.Now()
	created := j.CreatedAt
	if created.IsZero() {
		created = now
	}
	return persistedJob{
		ID: j.ID, Kind: j.Kind, Total: j.Total, Done: j.Done,
		Current: j.Current, Status: j.Status, Stage: j.Stage, StageLabel: j.StageLabel,
		Results: append([]scrapeResult(nil), j.Results...),
		CreatedAt: created.Format(time.RFC3339),
		UpdatedAt: now.Format(time.RFC3339),
	}
}

// persist 全量写库（create/cancel/完成/定时快照共用）；created_at 首插保留，
// updated_at 每次刷新。
func (js *jobStore) persist(j *scrapeJob) {
	if js.db == nil {
		return
	}
	p := j.snapshot()
	rb, err := json.Marshal(p.Results)
	if err != nil {
		return
	}
	js.db.Exec(`INSERT INTO jobs(id,kind,total,done,current,status,stage,stage_label,results,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET done=excluded.done,current=excluded.current,status=excluded.status,
		stage=excluded.stage,stage_label=excluded.stage_label,results=excluded.results,updated_at=excluded.updated_at`,
		p.ID, p.Kind, p.Total, p.Done, p.Current, p.Status, p.Stage, p.StageLabel, string(rb), p.CreatedAt, p.UpdatedAt)
}

// autoPersist 每 5 秒把进行中任务全量落库一次：进程意外退出时进度最多丢失
// 5 秒；重启后按库内快照恢复（running → interrupted）。
func (js *jobStore) autoPersist() {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for range t.C {
		if js.db == nil {
			return
		}
		for _, j := range js.active() {
			js.persist(j)
		}
	}
}

// restore 启动时从库恢复任务：running → interrupted（本进程任务线程已随重启
// 结束），done/cancelled 历史保留供前端按 jobId 查询；恢复后写回库，保证库内
// 不再残留 running 状态。最后执行保留策略：清理 30 天前的终态任务。
func (js *jobStore) restore() {
	if js.db == nil {
		return
	}
	rows, err := js.db.Query(`SELECT id,kind,total,done,current,status,stage,stage_label,results,created_at FROM jobs`)
	if err != nil {
		return
	}
	defer rows.Close()
	for rows.Next() {
		var id, kind, current, status, stage, stageLabel, results, created string
		var total, done int
		if err := rows.Scan(&id, &kind, &total, &done, &current, &status, &stage, &stageLabel, &results, &created); err != nil {
			continue
		}
		var rs []scrapeResult
		_ = json.Unmarshal([]byte(results), &rs)
		if status == "running" {
			status = "interrupted"
			js.db.Exec(`UPDATE jobs SET status='interrupted' WHERE id=?`, id)
		}
		j := &scrapeJob{ID: id, Kind: kind, Total: total, Done: done, Current: current, Status: status, Stage: stage, StageLabel: stageLabel, Results: rs}
		if t, err := time.Parse(time.RFC3339, created); err == nil {
			j.CreatedAt = t
		}
		js.mu.Lock()
		js.jobs[id] = j
		js.mu.Unlock()
	}
	js.db.Exec(`DELETE FROM jobs WHERE status != 'running' AND updated_at < ?`,
		time.Now().AddDate(0, 0, -30).Format(time.RFC3339))
}

func (js *jobStore) create(total int, kind string) *scrapeJob {
	j := &scrapeJob{ID: randomToken(), Kind: kind, Total: total, Status: "running", CreatedAt: time.Now()}
	js.mu.Lock()
	js.jobs[j.ID] = j
	js.mu.Unlock()
	js.persist(j)
	return j
}

func (js *jobStore) get(id string) *scrapeJob {
	js.mu.Lock()
	defer js.mu.Unlock()
	if j, ok := js.jobs[id]; ok {
		return j
	}
	// 内存无此任务（进程重启后）：从库恢复历史任务
	if js.db == nil {
		return nil
	}
	var jid, kind, current, status, stage, stageLabel, results, created string
	var total, done int
	if err := js.db.QueryRow(
		`SELECT id,kind,total,done,current,status,stage,stage_label,results,created_at FROM jobs WHERE id=?`,
		id).Scan(&jid, &kind, &total, &done, &current, &status, &stage, &stageLabel, &results, &created); err != nil {
		return nil
	}
	var rs []scrapeResult
	_ = json.Unmarshal([]byte(results), &rs)
	j := &scrapeJob{ID: jid, Kind: kind, Total: total, Done: done, Current: current, Status: status, Stage: stage, StageLabel: stageLabel, Results: rs}
	if t, err := time.Parse(time.RFC3339, created); err == nil {
		j.CreatedAt = t
	}
	js.jobs[jid] = j
	return j
}

// cancel 请求取消任务：置位 Cancel，任务循环在下一步前检查并立即停止。
// 任务可能已在取消请求前完成（幂等，返回 false 表示无需取消）。
func (js *jobStore) cancel(id string) bool {
	j := js.get(id)
	if j == nil {
		return false
	}
	j.mu.Lock()
	if j.Status != "running" {
		j.mu.Unlock()
		return false
	}
	j.Cancel = true
	j.mu.Unlock()
	js.persist(j)
	return true
}

// jobCanceled 报告任务是否已被请求取消（供各任务循环在每步前检查）。
func jobCanceled(j *scrapeJob) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.Cancel
}

// cancelJobHandler: POST /api/jobs/{id}/cancel
func cancelJobHandler(js *jobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !js.cancel(id) {
			writeErr(w, http.StatusNotFound, "任务不存在或已结束")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"ok": "已请求取消"})
	}
}

// active returns snapshots of all running jobs. The frontend calls this on
// login/refresh so tasks keep showing progress across page reloads and devices
// (the job itself runs in this process; a container restart cancels it).
func (js *jobStore) active() []*scrapeJob {
	js.mu.Lock()
	defer js.mu.Unlock()
	out := []*scrapeJob{}
	for _, j := range js.jobs {
		if j.Status != "running" {
			continue
		}
		j.mu.Lock()
		out = append(out, &scrapeJob{
			ID: j.ID, Kind: j.Kind, Total: j.Total, Done: j.Done,
			Current: j.Current, Status: j.Status,
			Results: failResults(j.Results),
			Summary: summarize(j.Results),
		})
		j.mu.Unlock()
	}
	return out
}

// runProgressJob 启动通用后台任务（格式转换 / 音频识别 / 乱码修复 / 简繁转换）：
// 串行处理 total 项，每完成一项更新 Done/Current/Results，
// 前端通过 GET /api/jobs/{id} 轮询可视化进度。用户取消时立即停止。
func (js *jobStore) runProgressJob(kind string, total int, step func(j *scrapeJob, i int) scrapeResult) *scrapeJob {
	job := js.create(total, kind)
	go func() {
		for i := 0; i < total; i++ {
			if jobCanceled(job) {
				break
			}
			r := step(job, i)
			job.mu.Lock()
			job.Done++
			job.Results = append(job.Results, r)
			job.mu.Unlock()
		}
		job.mu.Lock()
		if job.Cancel {
			job.Status = "cancelled"
		} else {
			job.Status = "done"
		}
		job.Current = ""
		job.mu.Unlock()
		logJobDone(job) // 任务完成后统一写入操作日志
		js.persist(job)
	}()
	return job
}

// batchScrapeHandler starts a background job that scrapes the given track ids.
// 配置了 AcoustID Key 时，存疑曲目（Unknown/乱码/缺艺术家）先自动走音频识别。
func batchScrapeHandler(js *jobStore, store *store, ms *scrape.MultiSource, cfg *cstore.Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IDs         []string `json:"ids"`
			FetchCover  bool     `json:"fetchCover"`
			FetchLyrics bool     `json:"fetchLyrics"`
			Source      string   `json:"source"`
			Fields      []string `json:"fields"`
			SkipFilled  bool     `json:"skipFilled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if len(req.IDs) == 0 {
			writeErr(w, http.StatusBadRequest, "请至少勾选一首曲目")
			return
		}
		if req.Source == "" {
			req.Source = "auto"
		}
		fields := fieldsFromList(req.Fields, req.FetchCover, req.FetchLyrics)
		job := js.create(len(req.IDs), "scrape")
		go func() {
			runBatch(job, store, ms, cfg, req.Source, req.IDs, fields, req.SkipFilled)
			js.persist(job) // 完成/中断后落库，重启可查历史
		}()
		writeJSON(w, http.StatusOK, map[string]any{"jobId": job.ID, "total": job.Total})
	}
}

// activeJobsHandler lists all running jobs so the UI can resume polling after
// a page reload or when signing in from another device.
func activeJobsHandler(js *jobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": js.active()})
	}
}

// jobProgressHandler returns the current state of a batch job. Updated tracks
// are returned once and then cleared, so repeated polls deliver only the delta.
func jobProgressHandler(js *jobStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		j := js.get(r.PathValue("id"))
		if j == nil {
			writeErr(w, http.StatusNotFound, "job not found")
			return
		}
		j.mu.Lock()
		out := scrapeJob{
			ID:         j.ID,
			Total:      j.Total,
			Done:       j.Done,
			Current:    j.Current,
			Status:     j.Status,
			Stage:      j.Stage,
			StageLabel: j.StageLabel,
			HashDone:   j.HashDone,
			HashTotal:  j.HashTotal,
			StageDone:  j.StageDone,
			StageTotal: j.StageTotal,
			// 大任务瘦身：只回问题项（失败/跳过），成功项仅进 summary 计数。
			Results: failResults(j.Results),
			Summary: summarize(j.Results),
			// 增量曲目同样剥离歌词正文（列表合并只需展示字段）。
			Updated:   stripListLyrics(j.Updated),
			CreatedAt: j.CreatedAt,
		}
		if !out.CreatedAt.IsZero() {
			out.Duration = int(time.Since(out.CreatedAt).Seconds())
			if out.Duration < 1 {
				out.Duration = 1
			}
		}
		j.Updated = nil
		j.mu.Unlock()
		writeJSON(w, http.StatusOK, &out)
	}
}

func runBatch(job *scrapeJob, store *store, ms *scrape.MultiSource, cfg *cstore.Config, source string, ids []string, fields ScrapeFields, skipFilled bool) {
	defer func() {
		job.mu.Lock()
		if job.Cancel {
			job.Status = "cancelled"
		} else {
			job.Status = "done"
		}
		job.Current = ""
		job.mu.Unlock()
		logJobDone(job) // 批量刮削完成后写入操作日志
	}()
	multi := len(ids) > 1
	// 专辑级封面缓存：同一专辑的多首歌曲共享封面，避免逐首重复搜索/下载
	coverCache := map[string][]byte{}
	for _, id := range ids {
		if jobCanceled(job) {
			return
		}
		t := store.get(id)
		if t == nil {
			job.mu.Lock()
			job.Results = append(job.Results, scrapeResult{ID: id, OK: false, Message: "曲目不存在"})
			job.Done++
			job.mu.Unlock()
			continue
		}
		// 存疑自动识别：配了 AcoustID Key 才启用（识别含库内指纹回退；
		// 识别重命名产生新 ID 后，后续刮削基于新曲目继续）
		if cfg.GetDefault("acoustid_key", "") != "" && trackIsDoubtful(t) {
			if res := identifyDoubtfulTrack(store, ms, t, cfg); res.OK && res.NewID != "" {
				if nt := store.get(res.NewID); nt != nil {
					t = nt
				}
			}
		}
		job.mu.Lock()
		job.Current = t.FileName
		job.mu.Unlock()

		msg, ok := scrapeOneTrack(t, ms, source, fields, skipFilled, store, coverCache)
		skip := strings.Contains(msg, "智能跳过")
		// 完成后把最新曲目快照加入增量队列（前端据此实时更新列表，不整表刷新）
		job.mu.Lock()
		if nt := store.get(t.ID); nt != nil {
			job.Updated = append(job.Updated, nt)
		}
		job.Results = append(job.Results, scrapeResult{ID: t.ID, FileName: t.FileName, OK: ok, Skip: skip, Message: msg})
		job.Done++
		job.mu.Unlock()

		if multi {
			time.Sleep(1100 * time.Millisecond) // respect source rate limits
		}
	}
}

// scrapeOneTrack auto-scrapes a single track: search by its current title +
// artist on the given source, pick the best result, write only the enabled
// fields and optionally fetch the cover and/or lyrics. When skipFilled is set,
// fields the track already carries are left untouched (smart skip to save
// hardware); if nothing needs filling, the track is skipped without searching.
// 操作项（文件名/LRC/音频属性）不依赖源搜索，勾选后始终执行。
// Returns (message, ok).
func scrapeOneTrack(t *model.Track, ms *scrape.MultiSource, source string, fields ScrapeFields, skipFilled bool, store *store, coverCache map[string][]byte) (string, bool) {
	needOp := fields.FileName ||
		fields.Bitrate || fields.SampleRate || fields.BitDepth ||
		fields.Channels || fields.Duration || fields.FileSize
	if skipFilled {
		// Drop already-filled fields; if nothing remains (except ops), skip search.
		if !applySkipFilled(t, &fields) && !needOp {
			return "已有完整标签，智能跳过", true
		}
	}
	hasSearch := fields.Cover || fields.Title || fields.Artist || fields.Album ||
		fields.AlbumArtist || fields.Genre || fields.Year || fields.TrackNumber ||
		fields.Lyrics || fields.Composer || fields.Lyricist || fields.AlbumType ||
		fields.Language || fields.Comment || fields.DiscNumber ||
		fields.TrackTotal || fields.DiscTotal

	tags := map[string][]string{}
	var sr scrape.SearchResult
	searched := false // 搜索执行且命中结果（区分“源无结果”与“标签已完整”）
	query := ""
	var title string       // 函数级：封面纯标题兜底用（effectiveSearchTerms 的标题）
	var artist string      // 函数级：歌词兜底用（搜索 0 命中时仍按标题+艺术家直查歌词）
	var coverErrMsg string // 封面失败原因（写入结果消息便于排查）
	if hasSearch {
		// 搜索关键词：优先真实标签；标签为空/“Unknown”/乱码时回退文件名解析
		// （如 `Unknown - 蓝色蝴蝶.mp3` → 用“蓝色蝴蝶”去刮削）。
		title, artist = effectiveSearchTerms(t)
		query = title
		if artist != "" && !looksUnknown(artist) && !looksGarbled(artist) {
			query += " " + artist
		}
		if strings.TrimSpace(query) == "" && !needOp {
			return "无标题/艺术家，跳过", false
		}
		if strings.TrimSpace(query) != "" {
			// 源降级：首选源（或 auto 语言优选链）无结果时自动匹配下一条源，
			// 保证"这个源没搜到 → 自动换下一个源"。auto 时等价于语言优选链。
			results, err := ms.SearchWithFallback(source, query, 3)
			if err != nil {
				if !needOp {
					return "搜索失败: " + err.Error(), false
				}
			} else if len(results) > 0 {
				searched = true
				sr = pickBest(results, title)
				ms.Enrich(&sr) // 补全专辑艺术家/流派/年代/曲目号/语言/专辑类型/总数

				// 部分源会把 GBK 编码字节按 Latin-1 塞进 JSON，得到
				// "习惯了寂寞" → "Ï°¹ßÁË¼ÅÄ¯" 这类乱码。写入标签前统一修复，
				// 防止把乱码写进文件（fixMojibake 有得分保护，正常文本不受影响）。
				// 修复后统一转简体（与音频识别/乱码修复口径一致，整理结果应为简体）：
				// 否则刮削源（如 QQ/网易）返回的繁体标题会在 script 阶段之后覆盖
				// 已转简体的标签，导致繁体筛选始终有残留。
				fixSrc := func(s string) string {
					if s == "" {
						return s
					}
					if f := fixMojibake(s); f != s {
						s = f
					}
					return simpConverter(s)
				}
				sr.Title = fixSrc(sr.Title)
				sr.Album = fixSrc(sr.Album)
				sr.Language = fixSrc(sr.Language)
				sr.AlbumType = fixSrc(sr.AlbumType)
				sr.Comment = fixSrc(sr.Comment)
				for i := range sr.Artists {
					sr.Artists[i] = fixSrc(sr.Artists[i])
				}
				for i := range sr.AlbumArtist {
					sr.AlbumArtist[i] = fixSrc(sr.AlbumArtist[i])
				}
				for i := range sr.Genre {
					sr.Genre[i] = fixSrc(sr.Genre[i])
				}
				for i := range sr.Composer {
					sr.Composer[i] = fixSrc(sr.Composer[i])
				}
				for i := range sr.Lyricist {
					sr.Lyricist[i] = fixSrc(sr.Lyricist[i])
				}

				// 核心标签
				if fields.Title && stripAudioExt(strings.TrimSpace(sr.Title)) != "" {
					tags[taglibx.Title] = []string{stripAudioExt(strings.TrimSpace(sr.Title))}
				}
				if fields.Artist && len(sr.Artists) > 0 {
					tags[taglibx.Artist] = sr.Artists
				}
				if fields.Album && sr.Album != "" {
					tags[taglibx.Album] = []string{sr.Album}
				}
				if fields.AlbumArtist && len(sr.AlbumArtist) > 0 {
					tags[taglibx.AlbumArtist] = sr.AlbumArtist
				}
				if fields.Genre && len(sr.Genre) > 0 {
					tags[taglibx.Genre] = sr.Genre
				}
				if fields.Year && sr.Date != "" {
					tags[taglibx.Date] = []string{sr.Date}
				}
				if fields.TrackNumber && sr.TrackNumber > 0 {
					tags[taglibx.TrackNumber] = []string{strconv.Itoa(sr.TrackNumber)}
				}
				if sr.Source == "musicbrainz" && sr.SourceID != "" {
					tags[taglibx.MBTrackID] = []string{sr.SourceID}
				}
				if sr.ReleaseMBID != "" {
					tags[taglibx.MBAlbumID] = []string{sr.ReleaseMBID}
				}
				// 扩展标签（源提供才写，源没有的字段不写入）
				if fields.Composer && len(sr.Composer) > 0 {
					tags[taglibx.Composer] = sr.Composer
				}
				if fields.Lyricist && len(sr.Lyricist) > 0 {
					tags[taglibx.Lyricist] = sr.Lyricist
				}
				if fields.Language && sr.Language != "" {
					tags[taglibx.Language] = []string{sr.Language}
				}
				if fields.AlbumType && sr.AlbumType != "" {
					tags[taglibx.ReleaseType] = []string{sr.AlbumType}
				}
				if fields.Comment && sr.Comment != "" {
					tags[taglibx.Comment] = []string{sr.Comment}
				}
				if fields.DiscNumber && sr.DiscNumber > 0 {
					tags[taglibx.DiscNumber] = []string{strconv.Itoa(sr.DiscNumber)}
				}
				if fields.TrackTotal && sr.TotalTracks > 0 {
					tags[taglibx.TrackTotal] = []string{strconv.Itoa(sr.TotalTracks)}
				}
				if fields.DiscTotal && sr.TotalDiscs > 0 {
					tags[taglibx.DiscTotal] = []string{strconv.Itoa(sr.TotalDiscs)}
				}
			}
		}
	}
	// 歌词兜底：即使源搜索 0 命中（国内源偶发风控/限流），也用标签/文件名
	// 解析出的标题+艺术家直接查歌词——FetchLyrics 内部自带 netease→qq→lrclib
	// 多级兜底，不依赖刮削搜索是否命中（手动 /api/lyrics 同路径已验证可命中）。
	if fields.Lyrics {
		lyricSr := sr
		if lyricSr.Title == "" {
			lyricSr = scrape.SearchResult{Title: title}
			if artist != "" && !looksUnknown(artist) && !looksGarbled(artist) {
				lyricSr.Artists = []string{artist}
			}
		}
		if lyric, err := ms.FetchLyrics(lyricSr); err == nil && strings.TrimSpace(lyric) != "" {
			tags[taglibx.Lyrics] = []string{simpConverter(lyric)}
		}
	}
	// 音频属性写标签（本地探测值，无源依赖）
	if fields.Bitrate && t.Bitrate > 0 {
		tags[taglibx.Bitrate] = []string{strconv.Itoa(int(t.Bitrate))}
	}
	if fields.SampleRate && t.SampleRate > 0 {
		tags[taglibx.SampleRate] = []string{strconv.Itoa(int(t.SampleRate))}
	}
	if fields.BitDepth && t.BitDepth > 0 {
		tags[taglibx.BitDepth] = []string{strconv.Itoa(int(t.BitDepth))}
	}
	if fields.Channels && t.Channels > 0 {
		tags[taglibx.Channels] = []string{strconv.Itoa(int(t.Channels))}
	}
	if fields.Duration && t.Duration > 0 {
		tags[taglibx.Length] = []string{strconv.Itoa(int(t.Duration))}
	}
	if fields.FileSize && t.Size > 0 {
		tags[taglibx.FileSize] = []string{strconv.FormatInt(t.Size, 10)}
	}

	newPath := ""
	coverWritten := false
	if len(tags) > 0 {
		if err := taglibx.WriteTags(t.Path, tags, false); err != nil {
			return "写标签失败: " + err.Error(), false
		}
		// 标签与文件名不符时重命名「艺术家 - 标题.后缀」（如
		// Unknown - 开始恋爱.flac → E-kids - 开始恋爱.flac）。
		// 未写艺术家/标题标签时回退已有标签值，保证 FileName 操作可用。
		artist, title := "", ""
		if a := tags[taglibx.Artist]; len(a) > 0 {
			artist = a[0]
		}
		if tt := tags[taglibx.Title]; len(tt) > 0 {
			title = tt[0]
		}
		if artist == "" {
			artist = strings.TrimSpace(t.Tags[taglibx.Artist])
		}
		if title == "" {
			title = strings.TrimSpace(t.Tags[taglibx.Title])
		}
		if fields.FileName || (artist != "" && title != "") {
			newPath = renameFileToArtistTitle(store, t, artist, title)
		}
	}
	if fields.Cover {
		// 封面降级：当前源没有海报 → 自动匹配下一条源补一张。
		// 专辑级缓存：同专辑后续曲目直接复用已取到的封面（key 见 coverKey）。
		var img []byte
		key := ""
		if coverCache != nil {
			key = coverKey(sr)
			if key != "" {
				if b, ok := coverCache[key]; ok {
					img = b
				}
			}
		}
		if len(img) == 0 {
			// 封面降级：当前源无海报 → 自动匹配下一条源补一张；
			// 完整 query 全链失败再用纯歌名兜底一遍（iTunes/MusicBrainz 更易命中）。
			// 错误进入 coverErr 上报给结果消息，便于排查源/下载失败原因。
			var coverErr error
			img, coverErr = ms.FetchCover(sr)
			if len(img) == 0 {
				img, coverErr = ms.FetchCoverFallback(query, sr.Source, strings.TrimSpace(title))
			}
			if len(img) > 0 && key != "" && coverCache != nil {
				coverCache[key] = img
			}
			if len(img) == 0 && coverErr != nil {
				coverErrMsg = "海报失败: " + coverErr.Error()
			}
		}
		if len(img) > 0 {
			// 重命名可能已发生（t.Path 仍旧路径），封面必须写新路径，
			// 否则文件已被改名导致 "can't save file"。
			coverPath := t.Path
			if newPath != "" {
				coverPath = newPath
			}
			if err := taglibx.WriteCover(coverPath, img); err == nil {
				t.HasCover = true // 同步内存状态，列表“海报”筛选/统计立即生效
				coverWritten = true
			} else {
				coverErrMsg = "海报写入失败: " + err.Error()
			}
		}
	}
	// 只报告实际执行的动作（搜索失败/无属性值时不再误报"文件名"）
	var wrote []string
	if _, ok := tags[taglibx.Title]; ok {
		wrote = append(wrote, "歌名")
	}
	if coverWritten {
		wrote = append(wrote, "海报")
	}
	if _, ok := tags[taglibx.Lyrics]; ok {
		wrote = append(wrote, "歌词")
	}
	for _, k := range []string{taglibx.Bitrate, taglibx.SampleRate, taglibx.BitDepth,
		taglibx.Channels, taglibx.Length, taglibx.FileSize} {
		if _, ok := tags[k]; ok {
			wrote = append(wrote, "属性")
			break
		}
	}
	if newPath != "" {
		wrote = append(wrote, "文件名")
	}
	if len(wrote) == 0 {
		// 区分三种情况：封面失败（优先展示，便于排查）/ 标签已完整（智能跳过）/
		// 源无结果（真“无可用数据”）。
		if coverErrMsg != "" {
			return coverErrMsg, true
		}
		if searched {
			return "标签已完整，智能跳过", true
		}
		return "无可用数据，跳过", true
	}
	// 写过标签/封面且未重命名时，重新探测文件并把最新状态（含 hasCover）
	// 同步回内存与 SQLite——重命名路径已由 renameFileToArtistTitle 内部刷新。
	if newPath == "" {
		refreshTrack(store, t.Path)
	}
	msg := "刮削完成"
	if len(wrote) > 0 {
		msg += ": " + strings.Join(wrote, "+")
	}
	if coverErrMsg != "" {
		msg += "；" + coverErrMsg
	}
	return msg, true
}

// coverKey 生成专辑级封面缓存 key：优先源专辑 ID（同专辑曲目同 key），
// 否则用源+专辑名。搜索无结果时为空串（不缓存）。
func coverKey(sr scrape.SearchResult) string {
	if sr.AlbumID != "" {
		return sr.Source + "|" + sr.AlbumID
	}
	if sr.Album != "" {
		return sr.Source + "|album|" + sr.Album
	}
	return ""
}

// fileNameNeedsRename 报告文件名是否不是「艺术家 - 标题」格式（标签可用时）。
func fileNameNeedsRename(t *model.Track) bool {
	artist := strings.TrimSpace(t.Tags[taglibx.Artist])
	title := strings.TrimSpace(t.Tags[taglibx.Title])
	if artist == "" || title == "" || looksUnknown(artist) || looksGarbled(artist) || looksGarbled(title) {
		return false
	}
	want := sanitizeName(artist) + " - " + sanitizeName(stripAudioExt(title))
	return stripAudioExt(t.FileName) != want
}

// pickBest 从搜索结果中挑出与目标标题最匹配的一条：按标题相似度评分
// （归一化后去掉括号变体，Dice 二元组相似度），分数相同时优先带专辑的
// 完整结果；若全部低于阈值（源没返回真正匹配的曲目），退回"第一个有
// 专辑的结果，否则第一个"，避免拿完全无关的结果乱写标签。
func pickBest(results []scrape.SearchResult, wantTitle string) scrape.SearchResult {
	if len(results) == 0 {
		return scrape.SearchResult{}
	}
	best, bestScore := results[0], -1.0
	for i := range results {
		sc := titleSimilar(results[i].Title, wantTitle)
		hasAlbum := results[i].Album != ""
		if sc > bestScore || (sc == bestScore && hasAlbum && best.Album == "") {
			best, bestScore = results[i], sc
		}
	}
	if bestScore >= 0.35 {
		return best
	}
	for i := range results {
		if results[i].Album != "" {
			return results[i]
		}
	}
	return results[0]
}

// titleSimilar 返回两个标题的相似度（0~1）。先归一化：小写、去空白、
// 去掉 (…)（…）等括号变体（伴奏/现场/版本标注不影响匹配）。
func titleSimilar(a, b string) float64 {
	a, b = normTitle(a), normTitle(b)
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1
	}
	// Dice 二元组相似度：对中英文短标题都稳定
	ga := map[string]int{}
	for i := 0; i+2 <= len(a); i++ {
		ga[a[i:i+2]]++
	}
	gb := map[string]int{}
	for i := 0; i+2 <= len(b); i++ {
		gb[b[i:i+2]]++
	}
	inter := 0
	for k, v := range ga {
		if gb[k] > 0 {
			if v < gb[k] {
				inter += v
			} else {
				inter += gb[k]
			}
		}
	}
	ta, tb := len(a)-1, len(b)-1
	if ta <= 0 || tb <= 0 {
		return 0
	}
	return 2 * float64(inter) / float64(ta+tb)
}

// normTitle 归一化标题：小写、去空白、去掉括号及其内容。
func normTitle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	for {
		lo := strings.IndexAny(s, "([（")
		if lo < 0 {
			return strings.TrimSpace(s)
		}
		cl := strings.IndexAny(s[lo:], ")]）")
		if cl < 0 {
			return strings.TrimSpace(s[:lo])
		}
		s = s[:lo] + s[lo+cl+1:]
	}
}
