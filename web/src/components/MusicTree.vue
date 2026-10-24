<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { state, toast, refresh, loadSources, busy, startBatchPoll } from '../store.js'
import { api } from '../api.js'
import { t } from '../i18n.js'
const q = ref('');
const filter = ref('all');
const scanDir = ref(state.scanDir);
const scanMsg = ref('');
let poll = null

// 排序状态（默认不排序=保持扫描顺序；扫描进行中强制保持扫描顺序，暂停/完成后可点表头排序）
const sortKey = ref(null) // null = 扫描顺序
const sortDir = ref(1) // 1=升序 -1=降序
const pageSize = 200
const visible = ref(pageSize)

// 归一化：全角→半角、全角空格→普通空格、小写、去首尾空白（兼容中文输入习惯）
function norm(s) {
  return (s || '').replace(/[\uFF01-\uFF5E]/g, ch => String.fromCharCode(ch.charCodeAt(0) - 0xFEE0))
    .replace(/\u3000/g, ' ').trim().toLowerCase();
}

function isGarbled(t) {
  // 扫描时后端已判定（GBK/UTF-8 错乱 + 可修复性），优先用标志位；
  // 旧数据无标志位时回退到本地启发式
  if (typeof t.garbled === 'boolean') return t.garbled;
  const s = [t.fileName, t.tags.TITLE || '', t.tags.ARTIST || '', t.tags.ALBUM || ''].join(' ');
  return /[\uFFFD]/.test(s) || /[ÃÂ][\u0080-\u00BF]/.test(s) || /â€[™“”Œ]/.test(s) || /[åæçèéêëìíîï][\u0080-\u00BF]{2}/.test(s);
}
function isTrad(t) {
  return !!t.hasTrad;
}

// 表格列表：全部曲目（可按关键词过滤 + 按问题类型筛选）
const filtered = computed(() => {
  const kw = norm(q.value);
  let list = state.tracks;
  if (kw) {
    list = list.filter(t =>
      norm([t.tags.TITLE || '', t.tags.ARTIST || '', t.tags.ALBUM || '', t.fileName].join(' ')).includes(kw));
  }
  if (filter.value === 'garbled') list = list.filter(isGarbled);
  else if (filter.value === 'trad') list = list.filter(isTrad);
  else if (filter.value === 'nolyric') list = list.filter(t => !t.hasLyrics && !t.hasLrcFile);
  else if (filter.value === 'nocover') list = list.filter(t => !t.hasCover);
  else if (filter.value === 'needsid') list = list.filter(t => t.needsIdentify);
  return list;
});
const filterOpts = [
  { id: 'all', label: () => t('filterAll') },
  { id: 'garbled', label: () => t('filterMoji') },
  { id: 'trad', label: () => t('filterTrad') },
  { id: 'nolyric', label: () => t('filterNoLyric') },
  { id: 'nocover', label: () => t('filterNoCover') },
  { id: 'needsid', label: () => t('filterNeedIdentify') }
];
const filtering = computed(() => q.value.trim() !== '' || filter.value !== 'all');

// 音频后缀正则（文件名显示/解析共用）
const AUDIO_EXT = /\.(mp3|flac|wav|m4a|aac|ogg|opus|ape|wma|aiff|aif|mka|mpc|tta|dsf|dff)$/i

// 文件名显示名：去尾部音频后缀（发如雪.mp3.flac -> 发如雪）
function baseName(t) {
  let n = t.fileName || '';
  while (true) {
    const m = n.match(AUDIO_EXT);
    if (!m) break;
    n = n.slice(0, n.length - m[0].length);
  }
  return n;
}
// 从“艺术家 - 标题”文件名解析艺术家（标签为空时回退显示）
function nameArtist(t) {
  const m = (t.fileName || '').replace(AUDIO_EXT, '').match(/^(.*?)[\s\-—–]+([^\-—–]+)$/);
  return m ? m[1].trim() : '';
}
function displayTitle(t) { return (t.tags.TITLE || '').trim() || baseName(t); }
function displayArtist(t) { return (t.tags.ARTIST || '').trim() || nameArtist(t); }

// 排序
function sortVal(t, k) {
  switch (k) {
    case 'title': return displayTitle(t);
    case 'artist': return displayArtist(t);
    case 'album': return t.tags.ALBUM || '';
    case 'albumartist': return t.tags.ALBUMARTIST || '';
    case 'year': return parseInt(t.tags.DATE || '', 10) || 0;
    case 'genre': return t.tags.GENRE || '';
    case 'tracknumber': return parseInt(t.tags.TRACKNUMBER || '', 10) || 0;
    case 'size': return t.size || 0;
    case 'duration': return t.duration || 0;
    case 'bitrate': return t.bitrate || 0;
  }
  return '';
}
const sorted = computed(() => {
  const list = filtered.value.slice();
  // 扫描进行中（未暂停）：强制保持扫描顺序（先扫到的在前，递增在后）
  if (state.scanStatus.running && !state.scanStatus.paused) return list;
  const k = sortKey.value;
  if (!k) return list; // 未选择排序键：保持扫描顺序
  const d = sortDir.value;
  list.sort((a, b) => {
    const va = sortVal(a, k), vb = sortVal(b, k);
    let c;
    if (typeof va === 'number' && typeof vb === 'number') c = va - vb;
    else c = String(va).localeCompare(String(vb), 'zh-Hans-CN', { numeric: true, sensitivity: 'base' });
    return c * d;
  });
  return list;
});
function setSort(k) {
  if (sortKey.value === k) sortDir.value *= -1;
  else { sortKey.value = k; sortDir.value = 1; }
}
function sortIcon(k) { return sortKey.value === k ? (sortDir.value === 1 ? '▲' : '▼') : ''; }

// 分页渲染：大曲库不卡（滚动到底自动加载更多）
const shown = computed(() => sorted.value.slice(0, visible.value));
watch([q, filter, sortKey, sortDir], () => { visible.value = pageSize; });
function onScroll(e) {
  const el = e.target;
  if (el.scrollTop + el.clientHeight >= el.scrollHeight - 200 && visible.value < sorted.value.length) {
    visible.value += pageSize;
  }
}
// 滚动加载双通道：桌面 @scroll 监听容器；移动端浏览器视口可能超过断点导致滚动发生在 body（scroll 不冒泡到容器），
// 用 IntersectionObserver 哨兵兜底——哨兵进入视口即追加一页，兼容容器滚动与 body 滚动两种环境。
let io = null;
function setSentinel(el) {
  if (io) { io.disconnect(); io = null; }
  if (el) {
    io = new IntersectionObserver(entries => {
      if (entries[0] && entries[0].isIntersecting && visible.value < sorted.value.length) {
        visible.value += pageSize;
      }
    }, { rootMargin: '120px 0px' });
    io.observe(el);
  }
}
onBeforeUnmount(() => { if (io) io.disconnect(); });

function coverUrl(id) { return '/api/tracks/' + encodeURIComponent(id) + '/cover'; }
// 封面加载失败重试（列表大并发时偶发连接中断）：最多重试 2 次，带时间戳绕过缓存。
function retryCover(e, item) {
  const img = e.target;
  if (!img) return;
  const n = +(img.dataset.r || 0) + 1;
  img.dataset.r = n;
  if (n > 2) return;
  img.src = coverUrl(item.id) + '?t=' + Date.now();
}
// 列表瘦身：歌词正文不下发，以内嵌标志判断（含时间戳细分无法在列表层判断）。
function hasLyrics(t) { return !!t.hasLyrics; }
function lrcMark(it) {
  const a = hasLyrics(it), b = !!it.hasLrcFile;
  return a && b ? t('lyrBoth') : a ? t('lyrEmbed') : b ? t('lyrLrc') : t('lyrNone');
}
function fmtSize(s) {
  if (!s) return '';
  const mb = s / 1048576;
  return mb >= 1024 ? (mb / 1024).toFixed(1) + ' GB' : mb.toFixed(1) + ' MB';
}
function fmtDur(d) {
  if (!d) return '';
  const m = Math.floor(d / 60), s = Math.floor(d % 60);
  return m + ':' + String(s).padStart(2, '0');
}
function tag(t, k) { return (t.tags && t.tags[k]) || ''; }
function fmtKHz(v) { return v ? (v / 1000).toFixed(1) + ' kHz' : ''; }

function isAllSel() { return filtered.value.length > 0 && filtered.value.every(t => state.selected.has(t.id)); }
function toggleAll(e) {
  if (e.target.checked) filtered.value.forEach(t => state.selected.add(t.id));
  else filtered.value.forEach(t => state.selected.delete(t.id));
}
function toggleSel(t, ev) {
  if (ev.target.checked) state.selected.add(t.id); else state.selected.delete(t.id);
}
function openTrack(t) {
  state.currentId = t.id;
  state.activeTab = 'edit';
}

// 列表页选中删除文件（二次确认；同时删除磁盘文件并清理列表）
async function delSel() {
  if (state.selected.size === 0) { toast(t('selFirst'), 'err'); return; }
  if (!confirm(t('delConfirm', { n: state.selected.size }))) return;
  try {
    const r = await api('/api/tracks/delete', {
      method: 'POST', body: JSON.stringify({ ids: [...state.selected] })
    });
    const failed = r.failed || [];
    toast(t('delDone', { n: r.removed }) + (failed.length ? t('delFailed', { f: failed.length, first: failed[0] }) : ''), failed.length ? 'err' : 'ok');
    state.selected.clear();
    await refresh();
  } catch (e) { toast(t('delFail', { m: e.message }), 'err'); }
}

// 无歌词筛选：一键在线补歌词。勾选时只补所选，否则补当前筛选出的全部；
// 仅写 LYRICS 字段并智能跳过已有词的曲目，复用批量刮削任务与进度条。
async function fillLyricsBatch() {
  const ids = state.selected.size ? [...state.selected] : filtered.value.map(x => x.id);
  if (!ids.length) { toast(t('selFirst'), 'err'); return; }
  state.batchBusy = true;
  state.batchJob = null;
  try {
    const r = await api('/api/scrape/batch', {
      method: 'POST',
      body: JSON.stringify({
        ids, source: state.bSource || 'auto',
        fields: ['lyrics'], skipFilled: true
      })
    });
    startBatchPoll(r.jobId);
  } catch (e) {
    toast(t('batchStartFail', { m: e.message }), 'err');
    state.batchBusy = false;
  }
}

async function scan() {
  const dir = scanDir.value.trim();
  if (!dir) { toast(t('enterKeyword'), 'err'); return; }
  scanMsg.value = t('scanning') + '…';
  try {
    await api('/api/scan', { method: 'POST', body: JSON.stringify({ dir }) });
    toast(t('scanStarted'), 'ok');
    startPoll();
  } catch (e) { toast(t('searchFail', { m: e.message }), 'err'); scanMsg.value = ''; }
}

// 刷新列表：校验文件是否仍存在于磁盘（被其他程序删除的自动清理），
// 再同步最新列表与乱码/待识别标记。
async function refreshList() {
  try {
    const r = await api('/api/tracks/refresh');
    state.tracks = r.tracks;
    toast(r.removed ? t('refreshListDone', { n: r.removed }) : t('refreshListNone'), 'ok');
  } catch (e) { toast(t('delFail', { m: e.message }), 'err'); }
}
async function tick() {
  try {
    const st = await api('/api/scan/status');
    state.scanStatus = st;
    if (st.running) {
      try { state.tracks = await api('/api/tracks'); } catch (e) {}
      scanMsg.value = t('scanningMsg', {
        a: st.added, t: st.total, s: st.skipped ? t('skipped', { n: st.skipped }) : ''
      });
    } else {
      scanMsg.value = t('scanDone', {
        a: st.added,
        s: st.skipped ? t('skipped', { n: st.skipped }) : '',
        r: st.removed ? t('removedCleaned', { n: st.removed }) : '',
        e: st.error ? '（' + st.error + '）' : ''
      });
      stopPoll();
      await refresh();
    }
  } catch (e) { /* 轮询容错 */ }
}
function startPoll() { stopPoll(); poll = setInterval(tick, 800); }
function stopPoll() { if (poll) { clearInterval(poll); poll = null; } }

async function togglePause() {
  try {
    const r = await api('/api/scan/pause', { method: 'POST', body: JSON.stringify({ paused: !state.scanStatus.paused }) });
    state.scanStatus = r;
    toast(r.paused ? t('scanPaused') : t('scanResumed'), 'ok');
  } catch (e) { toast(t('delFail', { m: e.message }), 'err'); }
}

onMounted(async () => { await loadSources(); await refresh(); if (state.scanStatus.running) startPoll(); });
onBeforeUnmount(stopPoll);
</script>

<template>
  <div class="music-list">
    <div class="ml-toolbar">
      <div class="row">
        <input type="text" v-model="scanDir" :placeholder="t('scanPlaceholder')" @keydown.enter="scan" style="min-width:0" />
        <button class="sm" @click="scan" :disabled="state.scanStatus.running || busy()">{{ state.scanStatus.running ? t('scanning') : t('scan') }}</button>
        <button class="ghost sm" @click="refreshList" :disabled="state.scanStatus.running || busy()">{{ t('refreshList') }}</button>
        <button v-if="state.scanStatus.running" class="ghost sm" @click="togglePause">{{ state.scanStatus.paused ? t('resume') : t('pause') }}</button>
      </div>
      <div class="row" style="margin-top:8px">
        <input type="text" v-model="q" :placeholder="t('searchPlaceholder')" style="min-width:0" />
      </div>
      <div class="filters" style="margin-top:8px">
        <button v-for="f in filterOpts" :key="f.id" class="chip" :class="{ on: filter === f.id }" @click="filter = f.id">{{ f.label() }}</button>
      </div>
      <div v-if="filter === 'nolyric'" style="display:flex;gap:8px;align-items:center;margin-top:8px;flex-wrap:wrap">
        <button class="sm" :disabled="busy()" @click="fillLyricsBatch">
          {{ t('fillLyricsBatch', { n: state.selected.size || filtered.length }) }}
        </button>
        <span class="muted" style="font-size:12px">{{ t('fillLyricsHint') }}</span>
      </div>
      <div style="display:flex;gap:8px;align-items:center;margin-top:8px">
        <button v-if="state.selected.size > 0" class="ghost sm danger" :disabled="busy()" @click="delSel">{{ t('deleteSel', { n: state.selected.size }) }}</button>
        <button v-if="state.selected.size > 0" class="ghost sm" :disabled="busy()" @click="state.selected.clear()">{{ t('clearSel') }}</button>
      </div>
      <div class="loading" style="margin-top:6px">{{ scanMsg }}{{ state.scanStatus.paused ? t('pausedMark') : '' }}</div>
    </div>

    <div class="ml-head">
      {{ filtering ? t('matchInfo', { m: filtered.length, n: state.tracks.length }) : t('trackCount', { n: state.tracks.length }) }}
    </div>

    <div class="tree-scroll" @scroll.passive="onScroll">
      <table class="mtbl">
        <thead>
          <tr>
            <th style="width:30px"><input type="checkbox" class="tchk" :checked="isAllSel()" @change="toggleAll" /></th>
            <th style="width:46px">{{ t('cover') }}</th>
            <th class="th-sort" @click="setSort('title')">{{ t('hTitle') }} {{ sortIcon('title') }}</th>
            <th class="th-sort" @click="setSort('artist')">{{ t('hArtist') }} {{ sortIcon('artist') }}</th>
            <th class="th-sort col-extra" style="width:64px" @click="setSort('bitrate')">{{ t('bQuality') }} {{ sortIcon('bitrate') }}</th>
            <th class="th-sort" @click="setSort('album')">{{ t('hAlbum') }} {{ sortIcon('album') }}</th>
            <th style="width:52px" :title="t('lyrTitle')">{{ t('hLyric') }}</th>
            <th class="th-sort col-extra" @click="setSort('albumartist')">{{ t('hAlbumArtist') }} {{ sortIcon('albumartist') }}</th>
            <th class="th-sort col-extra" @click="setSort('genre')">{{ t('hGenre') }} {{ sortIcon('genre') }}</th>
            <th class="th-sort col-extra" style="width:60px" @click="setSort('year')">{{ t('hYear') }} {{ sortIcon('year') }}</th>
            <th class="th-sort col-extra" style="width:56px" @click="setSort('tracknumber')">{{ t('bTrack') }} {{ sortIcon('tracknumber') }}</th>
            <th class="col-extra">{{ t('bFileName') }}</th>
            <th class="col-extra">{{ t('bComposer') }}</th>
            <th class="col-extra">{{ t('bLyricist') }}</th>
            <th class="col-extra">{{ t('bAlbumType') }}</th>
            <th class="col-extra">{{ t('bLanguage') }}</th>
            <th class="col-extra">{{ t('bComment') }}</th>
            <th class="col-extra">{{ t('bDiscNumber') }}</th>
            <th class="col-extra">{{ t('bDiscTotal') }}</th>
            <th class="col-extra">{{ t('bTrackTotal') }}</th>
            <th class="col-extra">{{ t('bSampleRate') }}</th>
            <th class="col-extra">{{ t('bBitDepth') }}</th>
            <th class="th-sort col-extra" style="width:56px" @click="setSort('duration')">{{ t('bDuration') }} {{ sortIcon('duration') }}</th>
            <th class="th-sort col-extra" style="width:64px" @click="setSort('size')">{{ t('hSize') }} {{ sortIcon('size') }}</th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="item in shown" :key="item.id" :class="{ on: state.selected.has(item.id) }" @click="openTrack(item)">
            <td><input type="checkbox" class="tchk" :checked="state.selected.has(item.id)" @click.stop @change="toggleSel(item, $event)" /></td>
            <td @click.stop>
              <img v-if="item.hasCover" class="cov" :src="coverUrl(item.id)" loading="lazy" @error="retryCover($event, item)" @click.stop="openTrack(item)" />
              <span v-else class="covph">♪</span>
            </td>
            <td :title="item.tags.TITLE ? item.tags.TITLE : baseName(item)">{{ displayTitle(item) }}</td>
            <td :title="item.tags.ARTIST ? item.tags.ARTIST : nameArtist(item)">{{ displayArtist(item) }}</td>
            <td class="col-extra">{{ item.quality || '' }}</td>
            <td>{{ tag(item, 'ALBUM') }}</td>
            <td :title="item.hasLrcFile || hasLyrics(item) ? t('lyrTitle') : ''">{{ lrcMark(item) }}</td>
            <td class="col-extra">{{ tag(item, 'ALBUMARTIST') }}</td>
            <td class="col-extra">{{ tag(item, 'GENRE') }}</td>
            <td class="col-extra">{{ tag(item, 'DATE') }}</td>
            <td class="col-extra">{{ tag(item, 'TRACKNUMBER') }}</td>
            <td class="col-extra" :title="item.fileName">{{ item.fileName }}</td>
            <td class="col-extra">{{ tag(item, 'COMPOSER') }}</td>
            <td class="col-extra">{{ tag(item, 'LYRICIST') }}</td>
            <td class="col-extra">{{ tag(item, 'RELEASETYPE') }}</td>
            <td class="col-extra">{{ tag(item, 'LANGUAGE') }}</td>
            <td class="col-extra" :title="tag(item, 'COMMENT')">{{ tag(item, 'COMMENT') }}</td>
            <td class="col-extra">{{ tag(item, 'DISCNUMBER') }}</td>
            <td class="col-extra">{{ tag(item, 'DISCTOTAL') }}</td>
            <td class="col-extra">{{ tag(item, 'TRACKTOTAL') }}</td>
            <td class="col-extra">{{ fmtKHz(item.sampleRate) }}</td>
            <td class="col-extra">{{ item.bitDepth || '' }}</td>
            <td class="col-extra">{{ fmtDur(item.duration) }}</td>
            <td class="muted col-extra">{{ fmtSize(item.size) }}</td>
          </tr>
          <tr v-if="!shown.length">
            <td colspan="24" class="muted" style="padding:16px;text-align:center">
              {{ state.tracks.length ? t('noMatch') : t('noTracks') }}
            </td>
          </tr>
        </tbody>
      </table>
      <!-- 手机上使用卡片列表，避免桌面宽表格挤压歌名和操作控件。 -->
      <div class="mobile-track-list">
        <div class="mobile-sel-bar">
          <label class="mobile-sel-all">
            <input type="checkbox" class="tchk" :checked="isAllSel()" @change="toggleAll" />
            <span>{{ t('bSelectAll') }}</span>
          </label>
          <span v-if="state.selected.size" class="muted mobile-sel-count">{{ t('selCount', { n: state.selected.size }) }}</span>
        </div>
        <article v-for="item in shown" :key="item.id" class="mobile-track-card" :class="{ on: state.selected.has(item.id) }" @click="openTrack(item)">
          <input type="checkbox" class="tchk mobile-track-check" :checked="state.selected.has(item.id)" :aria-label="displayTitle(item)" @click.stop @change="toggleSel(item, $event)" />
          <img v-if="item.hasCover" class="mobile-track-cover" :src="coverUrl(item.id)" loading="lazy" @error="retryCover($event, item)" @click.stop="openTrack(item)" />
          <span v-else class="mobile-track-cover mobile-track-cover-empty">♪</span>
          <div class="mobile-track-meta">
            <strong class="mobile-track-title">{{ displayTitle(item) }}</strong>
            <span class="mobile-track-artist">{{ displayArtist(item) || t('unknown') }}</span>
            <span class="mobile-track-album">{{ tag(item, 'ALBUM') || t('unknown') }}</span>
            <span class="mobile-track-tags">{{ lrcMark(item) }}<template v-if="item.quality"> · {{ item.quality }}</template><template v-if="item.duration"> · {{ fmtDur(item.duration) }}</template></span>
          </div>
          <button class="ghost sm mobile-track-edit" @click.stop="openTrack(item)">{{ t('edit') }}</button>
        </article>
        <div v-if="!shown.length" class="muted mobile-empty">{{ state.tracks.length ? t('noMatch') : t('noTracks') }}</div>
      </div>
      <div v-if="visible < sorted.length" class="muted" style="padding:8px;text-align:center;font-size:12px">
        {{ t('moreLoading', { x: shown.length, n: sorted.length }) }}
      </div>
      <!-- 加载更多哨兵：滚到底触发下一页（桌面容器滚动靠 @scroll，移动端 body 滚动靠 IO 兜底） -->
      <div ref="setSentinel" style="height:1px" aria-hidden="true"></div>
    </div>
  </div>
</template>
