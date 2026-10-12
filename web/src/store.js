import { reactive } from 'vue'
import { api } from './api.js'
import { t } from './i18n.js'

export const state = reactive({
  authed: false,
  user: '',
  view: 'home', // home=公开首页（搜索占位，无需登录）；app=后台（登录后）
  route: location.hash.slice(1) || '/',
  activeTab: 'list',
  tracks: [],
  selected: new Set(),
  sources: [],
  mbRes: [],
  currentId: null,
  dupGroups: [],
  scanDir: '/music',
  scanMsg: '',
  scanStatus: { running: false, paused: false, added: 0, total: 0, done: 0, skipped: 0, error: '' },
  bSource: 'auto',
  batchBusy: false,
  batchJob: null,
  toolJob: null, // 通用工具任务（音频识别/格式转换/乱码修复/简繁转换）进度
  version: '', // 当前运行版本（来自 /api/version）
  toast: { msg: '', type: '', show: false, _tm: null }
})

export function go(p) { location.hash = p }

// 是否有后台任务在跑（批量/一键刮削 或 音频识别/乱码/简繁/格式转换）。
// 任务进行中时，前端一律禁用会改库/改文件的操作按钮，避免并发冲突。
export function busy() {
  return state.batchBusy || !!(state.toolJob && state.toolJob.status === 'running');
}

export function cur() { return state.tracks.find(t => t.id === state.currentId) || null }

// 新版本检查（三态：有新版 / 已是最新 / 查询失败）。
// - force=true（手动“检查更新”）：调 /api/version/check 强制后端实时重查；
//   有新版弹更新窗；已是最新 toast 提示；查询失败 toast 提示失败原因。
// - force=false（自动：登录成功/刷新恢复会话）：调 /api/version 读缓存；
//   有新版弹更新窗（同一版本号每个会话只提醒一次，避免反复打扰）；
//   已是最新或查询失败均静默。
export const verState = { ver: null, showUpdate: false }

// verNewer 数值化逐段比较版本：latest 是否比 current 新（1.4.50 → 1.5.0 判新，
// "1.4.3" vs "1.4.7" 判旧）。与后端 hasUpdate 口径一致，避免字符串比较误判。
function verNewer(latest, current) {
  const pa = String(latest || '').replace(/^v/i, '').split('.').map(x => parseInt(x, 10) || 0);
  const pb = String(current || '').replace(/^v/i, '').split('.').map(x => parseInt(x, 10) || 0);
  for (let i = 0; i < Math.max(pa.length, pb.length); i++) {
    const a = pa[i] || 0, b = pb[i] || 0;
    if (a !== b) return a > b;
  }
  return false;
}

export async function checkVersion(force = false) {
  const url = force ? '/api/version/check' : '/api/version';
  try {
    const v = await api(url);
    state.version = v.current || '';
    const newer = !!(v.latest && v.status === 'ok' && verNewer(v.latest, v.current));
    if (newer) {
      verState.ver = v;
      // 自动检查：同一版本号每个会话只弹一次，避免刷新反复打扰；
      // 手动检查（force）每次有新版都弹。
      const key = 'lnh-upd-notified-' + v.latest;
      if (force || !sessionStorage.getItem(key)) {
        verState.showUpdate = true;
        sessionStorage.setItem(key, '1');
      }
    } else {
      verState.ver = null;
      if (force) {
        if (v.status === 'fail') toast(t('checkUpdateFail'), 'err');
        else if (v.status === 'ok') toast(t('upToDate'));
        else toast(t('checkUpdateFail'));
      }
    }
  } catch (e) {
    verState.ver = null;
    if (force) toast(t('checkUpdateFail') + ' (' + (e.message || '') + ')', 'err');
  }
}

export function toast(msg, type = '') {
  state.toast.msg = msg;
  state.toast.type = type;
  state.toast.show = true;
  clearTimeout(state.toast._tm);
  state.toast._tm = setTimeout(() => (state.toast.show = false), 2600);
}

// 把批量完成的最新曲目增量合并进列表：按 id 就地替换，
// 不重拉全量列表，因此不打断滚动/勾选/编辑中的状态。
export function mergeTracks(list = []) {
  if (!list.length) return;
  const byId = new Map(list.map(t => [t.id, t]));
  state.tracks = state.tracks.map(t => byId.get(t.id) || t);
  // 合并可能带来从未见过的曲目（如新增）
  const known = new Set(state.tracks.map(t => t.id));
  for (const t of list) if (!known.has(t.id)) state.tracks.push(t);
}

let _batchTimer = null;

// 全局批量轮询：批量任务在后台运行，切到任意页面进度都保持可见。
// 每次轮询把后端返回的增量 updated 合并进列表，列表实时更新、不整表刷新。
export async function startBatchPoll(jobId) {
  stopBatchPoll();
  state.batchBusy = true;
  const poll = async () => {
    try {
      const job = await api('/api/scrape/jobs/' + encodeURIComponent(jobId));
      state.batchJob = job;
      mergeTracks(job.updated || []);
      if (job.status === 'done') {
        stopBatchPoll();
        const rs = job.results || [];
        const ok = rs.filter(r => r.ok).length;
        const fail = rs.filter(r => !r.ok && !r.skip).length;
        const skip = rs.filter(r => r.skip).length;
        toast(t('batchFinish', { ok, fail, skip, t: job.total }), fail === 0 ? 'ok' : 'err');
        state.selected.clear();
        state.batchJob = null;
        state.batchBusy = false; // 完成必须解锁，否则批量/识别等按钮一直禁用
        await refresh(); // 兜底同步一次，保证与后端完全一致
      } else if (job.status === 'cancelled') {
        stopBatchPoll();
        toast(t('jobCancelled'), 'err');
        state.selected.clear();
        state.batchJob = null;
        state.batchBusy = false;
        await refresh();
      }
    } catch (e) {
      stopBatchPoll();
      state.batchJob = null;
      state.batchBusy = false;
      toast(t('batchPollFail', { m: e.message }), 'err');
    }
  };
  await poll();
  if (state.batchJob) _batchTimer = setInterval(poll, 1000);
}

export function stopBatchPoll() {
  if (_batchTimer) { clearInterval(_batchTimer); _batchTimer = null; }
  state.batchBusy = false;
}

let _toolTimer = null;

// 通用工具任务轮询：音频识别 / 格式转换 / 乱码修复 / 简繁转换。
// 与批量补全共用“后台运行、进度全局可见”的模式，任务完成后置 finished，
// 由发起组件负责 toast、清勾选与刷新。
export async function startToolJob(jobId, total, kind = 'tool') {
  stopToolJob();
  state.toolJob = { id: jobId, kind, total, done: 0, current: '', status: 'running', ok: 0, fail: 0, skip: 0, errors: [], finished: false };
  const poll = async () => {
    try {
      const j = await api('/api/jobs/' + encodeURIComponent(jobId));
      const tj = state.toolJob;
      if (!tj) return;
      tj.done = j.done;
      tj.current = j.current || '';
      if (j.status === 'done') {
        stopToolJob();
        tj.status = 'done';
        tj.finished = true;
        tj.skip = (j.results || []).filter(r => r.skip).length;
        tj.ok = (j.results || []).filter(r => r.ok).length;
        tj.fail = (j.results || []).filter(r => !r.ok && !r.skip).length;
        tj.errors = (j.results || []).filter(r => !r.ok && !r.skip && r.message);
      } else if (j.status === 'cancelled') {
        stopToolJob();
        tj.status = 'cancelled';
        tj.finished = true;
      }
    } catch (e) { /* 瞬时网络抖动忽略，下一轮再试 */ }
  };
  await poll();
  if (state.toolJob && !state.toolJob.finished) _toolTimer = setInterval(poll, 800);
}

export function stopToolJob() {
  if (_toolTimer) { clearInterval(_toolTimer); _toolTimer = null; }
}

// 取消进行中的任务（批量刮削 / 一键刮削 / 工具任务通用）。
// 后端置位 Cancel，任务循环在下一步前检查并立即停止。
export async function cancelJob(jobId) {
  const r = await api('/api/jobs/' + encodeURIComponent(jobId) + '/cancel', { method: 'POST', body: '{}' });
  return r.ok || r.error;
}

// 恢复进行中的任务：页面刷新或换设备登录后，把后端仍在运行的任务接回来，
// 继续展示进度直到完成。kind 决定完成提示的文案。
export async function resumeJobs() {
  try {
    const r = await api('/api/jobs');
    const jobs = r.jobs || [];
    for (const j of jobs) {
      if (j.kind === 'scrape') {
        await startBatchPoll(j.id);
      } else if (j.status === 'running') {
        await startToolJob(j.id, j.total, j.kind);
        // 只接管第一个工具任务，其余保持可见于服务端记录
        break;
      }
    }
  } catch (e) { /* 静默：无任务或接口异常不影响使用 */ }
}

export async function loadSources() {
  try { state.sources = await api('/api/scrape/sources'); }
  catch (e) {
    state.sources = [
      { name: 'auto', label: '自动' }, { name: 'musicbrainz', label: 'MusicBrainz' },
      { name: 'itunes', label: 'iTunes' }, { name: 'netease', label: '网易云' }, { name: 'qq', label: 'QQ音乐' }
    ];
  }
}

export async function refresh() {
  // 静默刷新：更新列表不弹窗、不打断操作
  try { state.tracks = await api('/api/tracks'); } catch (e) { /* 静默 */ }
  await loadDups();
}

export async function loadDups() {
  try {
    state.dupGroups = (await api('/api/duplicates')) || [];
  } catch (e) { /* dedup is best-effort */ }
}

export async function logout() {
  try { await api('/api/logout', { method: 'POST' }); } catch (e) { /* ignore */ }
  state.authed = false;
  state.view = 'home'; // 退出回到公开首页
  state.route = '/';
  location.hash = '/';
}
