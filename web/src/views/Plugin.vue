<script setup>
import { computed, onMounted, ref } from 'vue'
import { toast } from '../store.js'
import { api } from '../api.js'
import { t } from '../i18n.js'

// 插件页：一个插件类型一张卡片，点击卡片展开显示该类全部插件；
// 每个插件直接展开显示配置表单、状态、删除。内置插件只有开关，第三方插件可删除。
const plugins = ref([])
const busy = ref(false)
const msg = ref('')
const ffStatus = ref({ available: false, path: '', version: '' })
const ffInstalling = ref(false)

// 每类卡片独立展开状态（默认：刮削源、工具组展开，其余收起）
// 工具组包含 FFmpeg 与导入的第三方工具插件，默认展开便于管理
const openGroups = ref({ scrape: true, tool: true, download: false, protocol: false })
const confirmDel = ref('') // 二次点击确认态：记录待删除插件名

const groupMeta = [
  { kind: 'scrape', key: 'srcPlugins', hint: 'srcPluginsHint' },
  { kind: 'tool', key: 'ffPlugins', hint: 'ffPluginsHint' },
  { kind: 'download', key: 'dlPlugins', hint: 'dlPluginsHint' },
  { kind: 'protocol', key: 'protoPlugins', hint: 'protoPluginsHint' },
]

// 需 Key 源 -> 设置页字段文案的 i18n 键（提示用户去「设置 → 数据库配置」填写）
const keyLabelMap = {
  lastfm_key: 'fLastfm',
  spotify_id: 'fSpotifyId',
  discogs_token: 'fDiscogs',
  jamendo_client_id: 'fJamendo',
}
function keyLabel(p) { return keyLabelMap[p.keyField] || '' }

const groups = computed(() => {
  const g = { scrape: [], tool: [], download: [], protocol: [] }
  for (const p of plugins.value) if (g[p.kind]) g[p.kind].push(p)
  return g
})

function ffPlugin() { return plugins.value.find(p => p.name === 'tool.ffmpeg') }

async function loadPlugins() {
  try { plugins.value = (await api('/api/plugins') || {}).plugins || []; }
  catch (e) { msg.value = t('cfgLoadFail', { m: e.message }); }
}
async function loadFFmpeg() {
  try { ffStatus.value = await api('/api/ffmpeg/status'); }
  catch (e) { /* 静默 */ }
}

function toggleGroup(kind) { openGroups.value[kind] = !openGroups.value[kind]; }

async function togglePlugin(p) {
  busy.value = true;
  try {
    await api('/api/plugins', { method: 'POST', body: JSON.stringify({ [p.name]: { enabled: !p.enabled } }) });
    p.enabled = !p.enabled;
    toast(t('srcSaved'), 'ok');
  } catch (e) { toast(t('srcSaveFail', { m: e.message }), 'err'); }
  finally { busy.value = false; }
}

async function savePluginConfig(p) {
  busy.value = true;
  msg.value = '';
  try {
    await api('/api/plugins', { method: 'POST', body: JSON.stringify({ [p.name]: { config: { ...p.config } } }) });
    toast(t('cfgSaved'), 'ok');
    if (p.name === 'tool.ffmpeg') { loadFFmpeg(); }
  } catch (e) { msg.value = t('cfgSaveFail', { m: e.message }); }
  finally { busy.value = false; }
}

async function deletePlugin(p) {
  // 二次点击确认：第一次点击进入确认态，4 秒内再点一次才执行删除（无原生弹窗，兼容移动端）
  if (confirmDel.value !== p.name) {
    confirmDel.value = p.name;
    setTimeout(() => { if (confirmDel.value === p.name) confirmDel.value = ''; }, 4000);
    return;
  }
  confirmDel.value = '';
  busy.value = true;
  try {
    await api('/api/plugins/' + encodeURIComponent(p.name), { method: 'DELETE' });
    toast(t('pluginDeleted', { label: p.label }), 'ok');
    await loadPlugins();
  } catch (e) { toast(t('pluginDeleteFail', { m: e.message }), 'err'); }
  finally { busy.value = false; }
}

async function probeFFmpeg() {
  msg.value = '';
  const fp = ffPlugin();
  if (fp && fp.config && fp.config.path) await savePluginConfig(fp);
  await loadFFmpeg();
  toast(ffStatus.value.available ? t('ffReady') + ' · ' + ffStatus.value.version : t('ffMissing'), ffStatus.value.available ? 'ok' : 'err');
}

async function installFFmpeg() {
  ffInstalling.value = true;
  msg.value = '';
  try {
    const r = await api('/api/ffmpeg/install', { method: 'POST', body: '{}' });
    toast(t('ffInstallOk'), 'ok');
    if (r.path) { const fp = ffPlugin(); if (fp) { fp.config = fp.config || {}; fp.config.path = r.path; } }
    await loadFFmpeg();
  } catch (e) {
    toast(t('ffInstallFail', { m: e.message }), 'err');
    msg.value = e.message;
  } finally { ffInstalling.value = false; }
}

onMounted(() => { loadPlugins(); loadFFmpeg(); });
</script>

<template>
  <div class="page-wrap">
    <h1 class="pg-title">{{ t('plugins') }}</h1>

    <!-- 插件类型卡片：一个类型一张卡片 -->
    <div class="card" v-for="gm in groupMeta" :key="gm.kind" v-show="groups[gm.kind].length">
      <div class="card-head" :class="{ open: openGroups[gm.kind] }" @click="toggleGroup(gm.kind)">
        <span class="card-title">{{ t(gm.key) }}</span>
        <span class="muted small">{{ t(gm.hint) }}</span>
        <span class="muted small cnt">（{{ groups[gm.kind].length }}）</span>
        <span class="chev">{{ openGroups[gm.kind] ? '▲' : '▼' }}</span>
      </div>

      <div v-if="openGroups[gm.kind]" class="card-body">
        <div class="pitem" v-for="p in groups[gm.kind]" :key="p.name">
          <div class="prow">
            <span class="plabel">
              {{ p.label }}
              <small class="muted">{{ p.name }}</small>
              <em v-if="p.builtin" class="builtin">{{ t('builtin') }}</em>
              <em v-if="!p.builtin" class="ext">ext</em>
              <em v-if="p.needsKey" class="key" :class="{ ok: p.keyConfigured }">🔑 {{ p.keyConfigured ? t('keySet') : t('keyNotSet') }}</em>
            </span>
            <span class="pacts">
              <label class="switch" :class="{ on: p.enabled }">
                <input type="checkbox" :checked="p.enabled" :disabled="busy || p.builtin" @change="togglePlugin(p)" />
                <span class="slider"></span>
              </label>
              <button v-if="!p.builtin" class="sm ghost danger" :class="{ confirm: confirmDel === p.name }" @click="deletePlugin(p)">{{ confirmDel === p.name ? t('pluginDeleteAsk') : t('pluginDelete') }}</button>
            </span>
          </div>

          <!-- 直接展开：状态 / 配置 -->
          <div class="pdetail">
            <p v-if="p.hint" class="muted small" style="margin:6px 0">{{ p.hint }}</p>

            <!-- 需 Key 源：未配置时引导去设置页填写 -->
            <div v-if="p.needsKey && !p.keyConfigured" class="key-tip">
              → {{ t('keyGoSetting') }}「{{ t(keyLabel(p)) }}」
            </div>
            <div v-else-if="p.needsKey && !p.enabled" class="key-tip dim">
              {{ t('keyReadyToEnable') }}
            </div>

            <!-- FFmpeg 实时状态 -->
            <div v-if="p.name === 'tool.ffmpeg'" class="ff-status" :class="{ ok: ffStatus.available }" style="margin:8px 0 2px">
              <span class="dot"></span>
              {{ ffStatus.available ? t('ffReady') : t('ffMissing') }}
              <span v-if="ffStatus.available" class="muted"> · {{ t('ffVersion') }}: {{ ffStatus.version }}</span>
            </div>

            <!-- 配置字段表单 -->
            <div v-if="p.fields && p.fields.length" class="pfields">
              <div class="field" v-for="f in p.fields" :key="f.key" style="margin-top:6px">
                <label>{{ t(f.label) }}</label>
                <input :type="f.type === 'password' ? 'password' : 'text'" v-model="(p.config || {})[f.key]" :placeholder="t(f.placeholder || '')" />
              </div>
              <div class="row" style="margin-top:8px">
                <button class="sm" :disabled="busy" @click="savePluginConfig(p)">{{ t('saveCfg') }}</button>
                <button v-if="p.installable" class="sm secondary" :disabled="ffInstalling" @click="installFFmpeg">{{ ffInstalling ? t('ffInstalling') : t('ffInstall') }}</button>
                <button v-if="p.name === 'tool.ffmpeg'" class="sm secondary" @click="probeFFmpeg">{{ t('ffProbe') }}</button>
              </div>
            </div>
          </div>
        </div>
      </div>
    </div>

    <div class="panel" v-if="!msg && !plugins.length" style="padding:24px;text-align:center">
      <p class="muted">{{ t('pluginsEmpty') }}</p>
    </div>

    <div class="panel" v-if="msg">
      <p class="muted">{{ msg }}</p>
    </div>
  </div>
</template>

<style scoped>
.pg-title { margin: 0 0 14px; font-size: 20px; }
.panel { padding: 14px; margin-bottom: 10px; border: 1px solid var(--line, rgba(128,128,128,.25)); border-radius: 12px; background: var(--panel, transparent); }
.small { font-size: 12px; }
/* 类型卡片 */
.card { border: 1px solid var(--line, rgba(128,128,128,.25)); border-radius: 12px; margin-bottom: 10px; overflow: hidden; background: var(--panel, transparent); }
.card-head { display: flex; align-items: center; gap: 10px; padding: 12px 14px; cursor: pointer; user-select: none; }
.card-head .card-title { font-weight: 600; font-size: 15px; flex: none; }
.card-head .muted { flex: 1; min-width: 0; }
.card-head .cnt { flex: none; }
.card-head .chev { flex: none; font-size: 12px; opacity: .7; }
.card-body { padding: 4px 14px 14px; border-top: 1px solid var(--line, rgba(128,128,128,.15)); }
.plist { display: flex; flex-direction: column; gap: 8px; }
.pitem { padding: 10px; border-radius: 8px; background: var(--bg2, rgba(128,128,128,.08)); }
.prow { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.plabel { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.plabel small { opacity: .6; }
.pacts { display: flex; align-items: center; gap: 8px; }
.builtin, .key, .ext { font-style: normal; font-size: 11px; padding: 1px 6px; border-radius: 6px; background: rgba(128,128,128,.18); }
.ext { background: rgba(61,126,230,.18); color: #3d7ee6; }
.key { background: rgba(230,148,61,.22); color: #e6943d; }
.key.ok { background: rgba(48,164,108,.2); color: #30a46c; }
.key-tip { margin-top: 6px; font-size: 12px; color: #e6943d; }
.key-tip.dim { color: rgba(128,128,128,.7); }
.pdetail { border-top: 1px dashed var(--line, rgba(128,128,128,.2)); margin-top: 8px; padding-top: 8px; }
.switch { position: relative; display: inline-block; width: 40px; height: 22px; flex: none; }
.switch input { opacity: 0; width: 0; height: 0; }
.slider { position: absolute; cursor: pointer; inset: 0; background: rgba(128,128,128,.35); border-radius: 22px; transition: .2s; }
.slider::before { content: ''; position: absolute; width: 16px; height: 16px; left: 3px; top: 3px; background: #fff; border-radius: 50%; transition: .2s; }
.switch.on .slider { background: var(--accent, #2f80ed); }
.switch.on .slider::before { transform: translateX(18px); }
.ff-status { display: flex; align-items: center; gap: 6px; font-weight: 600; }
.ff-status .dot { width: 10px; height: 10px; border-radius: 50%; background: #e5484d; }
.ff-status.ok .dot { background: #30a46c; }
button.danger { color: #e34d59; }
button.danger.confirm { background: #e34d59; color: #fff; }
</style>
