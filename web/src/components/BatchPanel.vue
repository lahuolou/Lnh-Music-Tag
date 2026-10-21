<script setup>
import { computed, reactive, ref } from 'vue'
import { state, toast, refresh, startBatchPoll, startToolJob, stopToolJob, cancelJob, busy } from '../store.js'
import { api } from '../api.js'
import { t } from '../i18n.js'
import FormatDialog from './FormatDialog.vue'

const skipFilled = ref(true)
const fmtOpen = ref(false)
const oneKeyDedup = ref(false) // 一键刮削自动去重（默认不勾选）
const starting = ref(false) // 工具任务“启动中”防连点：点击立即禁用并显示启动中，避免点了没反应

// 统一任务进度：批量刮削(batchJob) 与 工具任务(toolJob) 共用一个进度条。
// 同一时刻只有一个任务在跑（busy 互斥），优先显示运行中的；都结束后保留
// 最近完成的批量任务统计。
const curJob = computed(() => {
  if (state.batchJob && state.batchJob.status !== 'done') return state.batchJob;
  if (state.toolJob && state.toolJob.status === 'running') return state.toolJob;
  if (state.batchJob) return state.batchJob;
  return state.toolJob;
});
const curIsBatch = computed(() => {
  const k = curJob.value && curJob.value.kind;
  return k === 'scrape' || k === 'onekey';
});
const curFails = computed(() => {
  if (!curJob.value) return [];
  const list = curJob.value.results || curJob.value.errors || [];
  return list.filter(x => !x.ok && x.message);
});

// 取消当前任务：调用后端取消接口，任务在下一步前停止并置 cancelled。
const canceling = ref(false)
async function doCancel() {
  const j = curJob.value;
  if (!j || canceling.value) return;
  canceling.value = true;
  try {
    await cancelJob(j.id);
  } catch (e) {
    toast(t('cancelFail', { m: e.message }), 'err');
  } finally {
    canceling.value = false;
  }
}

// 简繁转换（默认繁体 → 简体，常见于整理港台唱片）
const scriptTo = ref('simp')

function needsSel() { if (state.selected.size === 0) { toast(t('selFirst'), 'err'); return false; } return true; }

// 通用后台任务：音频识别 / 乱码修复 / 简繁转换 / 格式转换 / 修正文件后缀共用。
// 启动 job 后轮询进度，完成时提示成功/失败计数 + 清勾选 + 静默刷新列表。
// allowAll=true 时未勾选 = 处理全部曲目（如修正文件后缀）。
const failKeys = { identify: 'identifyFail', fixEnc: 'fixMojiFail', script: 'scriptFail', convert: 'fmtFail', renameClean: 'renameCleanFail' }

async function runToolJob(kind, url, body, allowAll = false) {
  if (!allowAll && !needsSel()) return;
  if (allowAll && state.selected.size === 0 && !state.tracks.length) { toast(t('selFirst'), 'err'); return; }
  if (starting.value) return; // 已有任务启动中，忽略连点
  starting.value = true;
  stopToolJob();
  try {
    const r = await api(url, { method: 'POST', body: JSON.stringify(body) });
    starting.value = false;
    if (!r.jobId) { // 无目标（如识别：库内无待识别曲目）
      toast(t('jobDone', { ok: 0, fail: 0, t: 0 }), 'err');
      return;
    }
    await startToolJob(r.jobId, r.total, kind);
    // 等待任务完成（startToolJob 立即返回，完成信号通过状态轮询到位）。
    // jobId 匹配检查：用户在任务完成前点了别的任务时，旧轮询立即退出，
    // 避免"识别完成后点乱码，旧任务提示覆盖新任务"的串扰。
    const t0 = Date.now();
    const iv = setInterval(() => {
      const tj = state.toolJob;
      if (tj && tj.id !== r.jobId) { clearInterval(iv); return; }
      if (tj && tj.finished) {
        clearInterval(iv);
        if (tj.status === 'cancelled') {
          toast(t('jobCancelled'), 'err');
        } else if (tj.status === 'failed') {
          toast(t(failKeys[kind], { m: t('jobLost') }), 'err');
        } else {
          toast(t('jobDone', { ok: tj.ok, fail: tj.fail, skip: tj.skip, t: tj.total }), tj.fail === 0 ? 'ok' : 'err');
        }
        state.selected.clear();
        refresh();
        if (kind === 'convert') fmtOpen.value = false;
      } else if (!tj || Date.now() - t0 > 120000) { // 任务丢失或 2 分钟未完成：解锁避免卡死
        clearInterval(iv);
        starting.value = false;
      }
    }, 500);
  } catch (e) {
    // 启动失败：清除可能残留的旧任务展示，避免“点了 A 却看到 B 的进度”
    starting.value = false;
    stopToolJob();
    state.toolJob = null;
    toast(t(failKeys[kind], { m: e.message }), 'err');
  }
}

async function fixEnc() { await runToolJob('fixEnc', '/api/fix-encoding', { ids: [...state.selected] }); }
async function convertScript() { await runToolJob('script', '/api/convert-script', { ids: [...state.selected], to: scriptTo.value }); }
async function fixExt() { await runToolJob('renameClean', '/api/rename-clean', { ids: [...state.selected] }, true); }

// 音频内容识别：无标签/无文件名信息的曲目（track01.mp3 这类），按音频指纹
// 与库内已标注曲目匹配，命中后写入标签并抓取歌词
async function identify() {
  if (state.selected.size === 0) { toast(t('identifySelFirst'), 'err'); return; }
  await runToolJob('identify', '/api/identify', { ids: [...state.selected] });
}

// 批量补全可选字段（默认全选；已有该标签时智能跳过）。
// 顺序即常用优先，与后端 fieldNames() 一致；key 与后端 fieldsFromList 对应。
const fields = reactive({
  title: true, artist: true, album: true, albumArtist: true,
  genre: true, year: true, trackNumber: true, lyrics: true, cover: true,
  composer: true, lyricist: true, albumType: true, language: true,
  discNumber: true, discTotal: true, trackTotal: true, comment: true,
  trackGain: true, albumGain: true, trackPeak: true, albumPeak: true,
  fileName: true,
  bitrate: true, sampleRate: true, bitDepth: true, channels: true, duration: true, fileSize: true,
})

// 字段展示顺序（常用优先：海报 → 核心标签 → 操作项 文件名 → 扩展标签 → 音频属性）
const fieldList = [
  ['cover', 'bCover'], ['title', 'bTitle'], ['artist', 'bArtist'], ['album', 'bAlbum'],
  ['albumArtist', 'bAlbumArtist'], ['genre', 'bGenre'], ['year', 'bYear'], ['trackNumber', 'bTrack'],
  ['lyrics', 'bLyrics'], ['fileName', 'bFileName'],
  ['composer', 'bComposer'], ['lyricist', 'bLyricist'],
  ['albumType', 'bAlbumType'], ['language', 'bLanguage'], ['comment', 'bComment'],
  ['discNumber', 'bDiscNumber'], ['discTotal', 'bDiscTotal'], ['trackTotal', 'bTrackTotal'],
  ['trackGain', 'bTrackGain'], ['albumGain', 'bAlbumGain'], ['trackPeak', 'bTrackPeak'],
  ['albumPeak', 'bAlbumPeak'],
  ['bitrate', 'bBitrate'], ['sampleRate', 'bSampleRate'], ['bitDepth', 'bBitDepth'],
  ['channels', 'bChannels'], ['duration', 'bDuration'], ['fileSize', 'bFileSize'],
]

// 全选 / 反选（对批量刮削元素全部字段生效）
function selectAll() { for (const [k] of fieldList) fields[k] = true; }
function invertSel() { for (const [k] of fieldList) fields[k] = !fields[k]; }

function clearSel() { state.selected.clear(); }

// 字段 key → 后端字段名（fieldsFromList 识别的名称）
const fieldNamesMap = {
  title: 'title', artist: 'artist', album: 'album', albumArtist: 'albumartist',
  genre: 'genre', year: 'year', trackNumber: 'tracknumber', lyrics: 'lyrics', cover: 'cover',
  composer: 'composer', lyricist: 'lyricist', albumType: 'albumtype', language: 'language',
  discNumber: 'discnumber', discTotal: 'disctotal', trackTotal: 'tracktotal', comment: 'comment',
  trackGain: 'trackgain', albumGain: 'albumgain', trackPeak: 'trackpeak', albumPeak: 'albumpeak',
  fileName: 'filename',
  bitrate: 'bitrate', sampleRate: 'samplerate', bitDepth: 'bitdepth', channels: 'channels',
  duration: 'duration', fileSize: 'filesize',
}

function selectedFieldNames() {
  const names = [];
  for (const [k] of fieldList) { if (fields[k]) names.push(fieldNamesMap[k]); }
  return names;
}

async function startBatch() {
  if (state.selected.size === 0) { toast(t('selFirst'), 'err'); return; }
  const names = selectedFieldNames();
  if (names.length === 0) { toast(t('batchNoField'), 'err'); return; }
  state.batchBusy = true;
  state.batchJob = null;
  try {
    const r = await api('/api/scrape/batch', {
      method: 'POST',
      body: JSON.stringify({
        ids: [...state.selected], source: state.bSource,
        fields: names, skipFilled: skipFilled.value
      })
    });
    // 全局轮询：后台运行、任意页面可见进度，并增量合并列表（不整表刷新）
    startBatchPoll(r.jobId);
  } catch (e) {
    toast(t('batchStartFail', { m: e.message }), 'err');
    state.batchBusy = false;
  }
}

// 一键刮削流水线：乱码修复 → 繁体转简体 → 文件名补艺术家 → 刮削元素。
// 勾选优先；未勾选 = 全部曲目。源无结果/无海报自动降级下一条源（后端处理）。
async function oneKey() {
  const ids = [...state.selected];
  if (!ids.length && !state.tracks.length) { toast(t('noTracks'), 'err'); return; }
  if (state.batchBusy) return;
  state.batchBusy = true;
  state.batchJob = null;
  try {
    const r = await api('/api/scrape/onekey', { method: 'POST', body: JSON.stringify({ ids, dedup: oneKeyDedup.value }) });
    startBatchPoll(r.jobId);
  } catch (e) {
    toast(t('batchStartFail', { m: e.message }), 'err');
    state.batchBusy = false;
  }
}
</script>

<template>
  <div class="muted" style="margin-bottom:10px">{{ t('batchHint') }}</div>

  <!-- 一键刮削流水线：自动去重(可选) → 乱码修复 → 繁转简 → 补艺术家 → 刮削 -->
  <div class="panel onekey" style="border-color:var(--accent, #2f80ed)">
    <div class="h3" style="margin-bottom:4px">⚡ {{ t('oneKey') }}</div>
    <div class="muted small" style="margin-bottom:10px">{{ t('oneKeyHint') }}</div>
    <label class="chk" style="margin-bottom:8px;display:block">
      <input type="checkbox" class="tchk" v-model="oneKeyDedup" />
      {{ t('oneKeyDedup') }}
    </label>
    <div style="display:flex;gap:10px;flex-wrap:wrap;align-items:center">
      <button :disabled="busy() || !state.tracks.length" @click="oneKey">
        {{ state.selected.size ? t('oneKeyRunSel', { n: state.selected.size }) : t('oneKeyRunAll', { n: state.tracks.length }) }}
      </button>
      <span class="muted small">{{ t('oneKeySelHint') }}</span>
    </div>
  </div>

  <!-- 批量刮削元素 -->
  <div class="panel" style="margin-top:0">
    <div class="h3" style="margin-bottom:6px">{{ t('batchScrape') }}</div>
    <div style="display:flex;gap:8px;margin-bottom:8px;flex-wrap:wrap;align-items:center">
      <button class="ghost sm" @click="selectAll">{{ t('bSelectAll') }}</button>
      <button class="ghost sm" @click="invertSel">{{ t('bInvert') }}</button>
      <label class="chk" style="margin-left:4px">
        <input type="checkbox" class="tchk" v-model="skipFilled" />
        {{ t('smartSkip') }}
      </label>
    </div>
    <div class="field-grid">
      <label class="chk" v-for="[k, label] in fieldList" :key="k">
        <input type="checkbox" class="tchk" v-model="fields[k]" /> {{ t(label) }}
      </label>
    </div>
  </div>

  <div style="display:flex;gap:10px;flex-wrap:wrap;align-items:center">
    <button class="ghost sm" :disabled="busy()" @click="clearSel">{{ t('clearSel') }}</button>
    <select v-model="state.bSource" style="padding:7px 10px;border-radius:8px;border:1px solid var(--line);background:var(--panel2);color:var(--text)">
      <!-- 只显示设置页开启的源插件；“自动”始终可用 -->
      <option v-for="s in state.sources" :key="s.name" :value="s.name" v-show="s.name === 'auto' || s.enabled !== false">{{ s.label }}</option>
    </select>
    <button :disabled="busy() || state.selected.size === 0" @click="startBatch">{{ state.selected.size === 0 ? t('selBtn') : t('batchRun', { n: state.selected.size }) }}</button>
  </div>

  <!-- 统一任务进度：批量刮削与工具任务共用一个进度条（curJob 优先显示运行中的任务）。
       进度显示当前阶段：去重哈希 → hashDone/hashTotal；其余阶段 → stageDone/stageTotal；
       无阶段信息时回退全局曲目进度 done/total。 -->
  <div v-if="curJob" style="margin-top:12px">
    <div class="progbar"><div class="progfill" :style="{ width: (curJob.status === 'running' && curJob.hashTotal && (curJob.hashDone || 0) < curJob.hashTotal
      ? Math.round((curJob.hashDone || 0) / curJob.hashTotal * 100)
      : (curJob.status === 'running' && curJob.stageTotal
          ? Math.round((curJob.stageDone || 0) / curJob.stageTotal * 100)
          : (curJob.total ? Math.round(curJob.done / curJob.total * 100) : 0))) + '%' }"></div></div>
    <div style="display:flex;align-items:center;gap:10px;flex-wrap:wrap">
      <div class="muted" style="flex:1;min-width:0">
        <template v-if="curJob.status === 'running' && curJob.hashTotal && (curJob.hashDone || 0) < curJob.hashTotal">
          {{ t('hashProgress', { d: curJob.hashDone || 0, t: curJob.hashTotal, c: curJob.current || t('processing') }) }}
        </template>
        <template v-else-if="curJob.status === 'running' && curJob.stageTotal">
          {{ t('stageProgress', { l: curJob.stageLabel || '', d: curJob.stageDone || 0, t: curJob.stageTotal, c: curJob.current || t('processing') }) }}
        </template>
        <template v-else-if="curJob.status === 'done'">
          {{ curIsBatch
            ? t('batchDone', { ok: (curJob.summary || {}).ok || 0, fail: (curJob.summary || {}).fail || 0, skip: (curJob.summary || {}).skip || 0, t: curJob.total })
            : t('jobDone', { ok: curJob.ok, fail: curJob.fail, skip: curJob.skip, t: curJob.total }) }}
        </template>
        <template v-else-if="curJob.status === 'cancelled'">{{ t('jobCancelled') }}</template>
        <template v-else>
          {{ curIsBatch
            ? t('batchProgressBar', { d: curJob.done, t: curJob.total, c: curJob.current || t('processing') })
            : t('jobRunning', { d: curJob.done, t: curJob.total, c: curJob.current || t('processing') }) }}
        </template>
      </div>
      <button v-if="curJob.status === 'running'" class="ghost sm danger" :disabled="canceling" @click="doCancel">{{ canceling ? t('canceling') : t('cancel') }}</button>
    </div>
    <div v-if="curJob.status === 'done' && curFails.length" class="err" style="white-space:pre-wrap">
      <div v-for="(r, i) in curFails" :key="i">{{ r.fileName }}: {{ r.message }}</div>
    </div>
  </div>

  <div style="border-top:1px solid var(--line);margin-top:16px;padding-top:12px">
    <div class="h3">{{ t('identify') }}</div>
    <div style="display:flex;gap:10px;flex-wrap:wrap;align-items:center;margin-top:6px">
      <button class="ghost sm" :disabled="busy() || starting || state.selected.size === 0" @click="identify">{{ starting ? t('starting') : busy() ? t('busyRunning') : state.selected.size === 0 ? t('selBtn') : t('identifyRun', { n: state.selected.size }) }}</button>
      <span class="muted">{{ t('identifyHint') }}</span>
    </div>
  </div>

  <div style="border-top:1px solid var(--line);margin-top:16px;padding-top:12px">
    <div class="h3">{{ t('fmtConvert') }}</div>
    <div style="display:flex;gap:10px;flex-wrap:wrap;align-items:center;margin-top:6px">
      <button class="ghost sm" :disabled="busy()" @click="fmtOpen = true">{{ t('fmtRun') }}</button>
      <span class="muted">{{ t('fmtHint') }}</span>
    </div>
  </div>
  <FormatDialog :open="fmtOpen" @close="fmtOpen = false" />

  <div style="border-top:1px solid var(--line);margin-top:16px;padding-top:12px">
    <div class="h3">{{ t('fixMoji') }}</div>
    <div style="display:flex;gap:10px;flex-wrap:wrap;align-items:center;margin-top:6px">
      <button class="ghost sm" :disabled="busy() || starting || state.selected.size === 0" @click="fixEnc">{{ starting ? t('starting') : busy() ? t('busyRunning') : state.selected.size === 0 ? t('selBtn') : t('fixMojiRun', { n: state.selected.size }) }}</button>
      <span class="muted">{{ t('fixMojiHint') }}</span>
    </div>
  </div>

  <div style="border-top:1px solid var(--line);margin-top:16px;padding-top:12px">
    <div class="h3">{{ t('renameClean') }}</div>
    <div style="display:flex;gap:10px;flex-wrap:wrap;align-items:center;margin-top:6px">
      <button class="ghost sm" :disabled="busy() || starting" @click="fixExt">{{ starting ? t('starting') : busy() ? t('busyRunning') : state.selected.size ? t('renameCleanRun', { n: state.selected.size }) : t('renameCleanAll', { n: state.tracks.length }) }}</button>
      <span class="muted">{{ t('renameCleanHint') }}</span>
    </div>
  </div>

  <div style="border-top:1px solid var(--line);margin-top:16px;padding-top:12px">
    <div class="h3">{{ t('scriptConv') }}</div>
    <div style="display:flex;gap:10px;flex-wrap:wrap;align-items:center;margin-top:6px">
      <select v-model="scriptTo" style="padding:7px 10px;border-radius:8px;border:1px solid var(--line);background:var(--panel2);color:var(--text)">
        <option value="trad">{{ t('scriptToTrad') }}</option>
        <option value="simp">{{ t('scriptToSimp') }}</option>
      </select>
      <button class="ghost sm" :disabled="busy() || starting || state.selected.size === 0" @click="convertScript">{{ starting ? t('starting') : busy() ? t('busyRunning') : state.selected.size === 0 ? t('selBtn') : t('scriptRun', { n: state.selected.size }) }}</button>
    </div>
  </div>
</template>
