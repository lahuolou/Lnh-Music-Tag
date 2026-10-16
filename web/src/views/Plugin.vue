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

// 每类卡片独立展开状态（默认：刮削源折叠，工具组、第三方插件组展开）
const openGroups = ref({ scrape: false, tool: true, third: true })
// 第三方插件逐卡片折叠：卡片头（名称+开关+卸载）常驻，配置区默认折叠
const openExt = ref({})
const uninstallTarget = ref(null) // 弹窗确认态：待卸载插件

function toggleExt(p) { openExt.value[p.name] = !openExt.value[p.name]; }

const groupMeta = [
  { kind: 'scrape', key: 'srcPlugins', hint: 'srcPluginsHint' },
  { kind: 'tool', key: 'ffPlugins', hint: 'ffPluginsHint' },
  { kind: 'third', key: 'extPlugins', hint: 'extPluginsHint' },
]

// 第三方插件 = 所有非内置插件（导入的 ZIP 插件包），统一在「第三方插件」组管理
const groups = computed(() => {
  const g = { scrape: [], tool: [], third: [] }
  for (const p of plugins.value) {
    if (p.builtin) { if (g[p.kind]) g[p.kind].push(p) }
    else g.third.push(p)
  }
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
  const target = !p.enabled;
  try {
    await api('/api/plugins', { method: 'POST', body: JSON.stringify({ [p.name]: { enabled: target } }) });
    p.enabled = target; // 仅成功后提交 UI 状态，失败保持原状
    toast(t('cfgSaved'), 'ok');
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
    await loadPlugins(); // 刷新 keyConfigured，保存 Key 后开关立即可用，无需刷新页面
  } catch (e) { msg.value = t('cfgSaveFail', { m: e.message }); }
  finally { busy.value = false; }
}

// FFmpeg 绝对路径回填：配置了路径显示配置值，否则显示探测到的路径（留空 = 自动探测）
const ffPath = computed({
  get: () => { const fp = ffPlugin(); return (fp && fp.config && fp.config.path) || ffStatus.value.path || ''; },
  set: v => { const fp = ffPlugin(); if (fp) { fp.config = fp.config || {}; fp.config.path = v; } },
});

// 卸载第三方插件：弹窗确认后删除（文件/注册/配置一并清理）
function askUninstall(p) { uninstallTarget.value = p; }
async function doUninstall(p) {
  uninstallTarget.value = null;
  busy.value = true;
  try {
    await api('/api/plugins/' + encodeURIComponent(p.name), { method: 'DELETE' });
    toast(t('pluginDeleted', { label: p.label }), 'ok');
    await loadPlugins();
    window.dispatchEvent(new CustomEvent('plugins-changed')); // 主界面动态插件 tab 实时刷新
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

// 卸载 FFmpeg：停用工具插件并清空配置（容器内安装的会顺带移除包）
async function uninstallFFmpeg() {
  ffInstalling.value = true;
  msg.value = '';
  try {
    await api('/api/ffmpeg/uninstall', { method: 'POST', body: '{}' });
    toast(t('ffUninstallOk'), 'ok');
    await loadFFmpeg();
    await loadPlugins();
  } catch (e) {
    toast(t('ffUninstallFail', { m: e.message }), 'err');
  } finally { ffInstalling.value = false; }
}

// 导入第三方 ZIP 插件包：直接把 zip 传给后端，导入即解压注册即用
async function importZip(ev) {
  const file = ev.target.files && ev.target.files[0];
  ev.target.value = '';
  if (!file) return;
  if (!/\.zip$/i.test(file.name)) { toast(t('extImportZipOnly'), 'err'); return; }
  if (file.size > 256 * 1024 * 1024) { toast(t('extTooBig'), 'err'); return; }
  msg.value = '';
  busy.value = true;
  try {
    const buf = await file.arrayBuffer();
    await api('/api/plugins/import', {
      method: 'POST',
      body: new Uint8Array(buf),
      headers: { 'X-File-Name': file.name, 'Content-Type': 'application/octet-stream' },
    });
    toast(t('extImportOk', { name: file.name }), 'ok');
    await loadPlugins();
    window.dispatchEvent(new CustomEvent('plugins-changed')); // 主界面动态插件 tab 实时刷新
  } catch (e) {
    toast(t('extImportFail', { m: e.message }), 'err');
    msg.value = e.message;
  } finally { busy.value = false; }
}

onMounted(() => { loadPlugins(); loadFFmpeg(); });
</script>

<template>
  <div class="page-wrap">
    <h1 class="pg-title">{{ t('plugins') }}</h1>

    <!-- 插件类型卡片：一个类型一张卡片；第三方插件组始终显示（提供导入入口） -->
    <div class="card" v-for="gm in groupMeta" :key="gm.kind" v-show="gm.kind === 'third' || groups[gm.kind].length">
      <div class="card-head" :class="{ open: openGroups[gm.kind] }" @click="toggleGroup(gm.kind)">
        <span class="card-title">{{ t(gm.key) }}</span>
        <span class="muted small">{{ t(gm.hint) }}</span>
        <span class="chev">{{ openGroups[gm.kind] ? '▲' : '▼' }}</span>
      </div>

      <div v-if="openGroups[gm.kind]" class="card-body">
        <!-- 第三方插件组：导入 ZIP 插件包入口 -->
        <div v-if="gm.kind === 'third'" class="ext-import" style="padding:8px 0 10px">
          <label class="sm secondary btn-upload" :class="{ disabled: busy }">
            {{ busy ? t('extImporting') : t('extImportZip') }}
            <input type="file" accept=".zip,application/zip" :disabled="busy" @change="importZip" style="display:none" />
          </label>
          <span class="muted small" style="margin-left:10px">{{ t('extImportHint') }}</span>
        </div>
        <p v-if="gm.kind === 'third' && !groups.third.length" class="muted small" style="padding:10px 2px">{{ t('extEmpty') }}</p>
        <!-- 第三方插件：插件开发说明（折叠展示） -->
        <details v-if="gm.kind === 'third'" class="dev-guide" style="margin-top:12px">
          <summary class="muted small" style="cursor:pointer;user-select:none">{{ t('extDevGuide') }}</summary>
          <pre class="muted" style="font-size:12px;line-height:1.7;white-space:pre-wrap;word-break:break-word;margin:10px 0 4px">{{ t('extDevGuideText') }}</pre>
        </details>
        <div class="pitem" v-for="p in groups[gm.kind]" :key="p.name">
          <div class="prow" :class="{ clickable: !p.builtin }" @click="!p.builtin && toggleExt(p)">
            <span class="plabel">
              {{ p.label }}
              <small class="muted">{{ p.name }}</small>
              <em v-if="p.builtin" class="builtin">{{ t('builtin') }}</em>
              <em v-if="!p.builtin" class="ext">ext</em>
              <em v-if="p.needsKey" class="key" :class="{ ok: p.keyConfigured }">🔑 {{ p.keyConfigured ? t('keySet') : t('keyNotSet') }}</em>
            </span>
            <span class="pacts" @click.stop>
              <label class="switch" :class="{ on: p.enabled }">
                <input type="checkbox" :checked="p.enabled" :disabled="busy || (p.needsKey && !p.keyConfigured) || p.name === 'scrape.auto'" @change="togglePlugin(p)" />
                <span class="slider"></span>
              </label>
              <button v-if="!p.builtin" class="sm ghost danger" @click="askUninstall(p)">{{ t('pluginDelete') }}</button>
              <span v-if="!p.builtin" class="chev small">{{ openExt[p.name] ? '▲' : '▼' }}</span>
            </span>
          </div>

          <!-- 第三方插件配置区可折叠（默认折叠，点卡片头展开）；内置插件直接展开 -->
          <div class="pdetail" v-show="p.builtin || openExt[p.name]">
            <p v-if="p.hint" class="muted small" style="margin:10px 0 6px">{{ t(p.hint) }}</p>

            <!-- FFmpeg 实时状态 -->
            <div v-if="p.name === 'tool.ffmpeg'" class="ff-status" :class="{ ok: ffStatus.available }" style="margin:10px 0 4px">
              <span class="dot"></span>
              {{ ffStatus.available ? t('ffReady') : t('ffMissing') }}
              <span v-if="ffStatus.available" class="muted"> · {{ t('ffVersion') }}: {{ ffStatus.version }}</span>
            </div>

            <!-- 配置字段表单 -->
            <div v-if="p.fields && p.fields.length" class="pfields">
              <div class="field" v-for="f in p.fields" :key="f.key">
                <label>{{ t(f.label) }}</label>
                <input v-if="p.name === 'tool.ffmpeg' && f.key === 'path'" type="text" v-model="ffPath" :placeholder="t(f.placeholder || '')" />
                <input v-else :type="f.type === 'password' ? 'password' : 'text'" v-model="(p.config || {})[f.key]" :placeholder="t(f.placeholder || '')" />
              </div>
              <div class="row" style="margin-top:12px">
                <button class="sm" :disabled="busy" @click="savePluginConfig(p)">{{ t('saveCfg') }}</button>
                <button v-if="p.installable && !ffStatus.available" class="sm secondary" :disabled="ffInstalling" @click="installFFmpeg">{{ ffInstalling ? t('ffInstalling') : t('ffInstall') }}</button>
                <button v-if="p.installable && ffStatus.available" class="sm secondary danger" :disabled="ffInstalling" @click="uninstallFFmpeg">{{ ffInstalling ? t('ffInstalling') : t('ffUninstall') }}</button>
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

    <!-- 卸载确认弹窗 -->
    <div v-if="uninstallTarget" class="modal-mask" @click.self="uninstallTarget = null">
      <div class="modal small">
        <div class="h3">🗑 {{ t('pluginUninstallTitle') }} · {{ uninstallTarget.label }}</div>
        <p class="muted" style="margin:10px 0 0;line-height:1.7">{{ t('pluginUninstallMsg') }}</p>
        <div class="modal-foot">
          <button class="ghost" @click="uninstallTarget = null">{{ t('cancel') }}</button>
          <button class="danger" @click="doUninstall(uninstallTarget)">{{ t('pluginDelete') }}</button>
        </div>
      </div>
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
.card-head .chev { flex: none; font-size: 12px; opacity: .7; }
.card-body { padding: 4px 14px 14px; border-top: 1px solid var(--line, rgba(128,128,128,.15)); }
.pitem { padding: 12px 14px; border-radius: 10px; background: var(--bg2, rgba(128,128,128,.08)); }
.pitem + .pitem { margin-top: 12px; }
.dev-guide { margin-top: 12px; margin-bottom: 14px; }
.prow { display: flex; align-items: center; justify-content: space-between; gap: 8px; flex-wrap: wrap; }
.prow.clickable { cursor: pointer; user-select: none; }
.prow.clickable .plabel { flex: 1; min-width: 0; }
.prow.clickable .pacts .chev { opacity: .6; }
.plabel { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.plabel small { opacity: .6; }
.pacts { display: flex; align-items: center; gap: 8px; }
.builtin, .key, .ext { font-style: normal; font-size: 11px; padding: 1px 6px; border-radius: 6px; background: rgba(128,128,128,.18); }
.ext { background: rgba(61,126,230,.18); color: #3d7ee6; }
.key { background: rgba(230,148,61,.22); color: #e6943d; }
.key.ok { background: rgba(48,164,108,.2); color: #30a46c; }
.pdetail { border-top: 1px dashed var(--line, rgba(128,128,128,.2)); margin-top: 12px; padding-top: 10px; }
.pdetail .field + .field { margin-top: 10px; }
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
.btn-upload { display: inline-block; padding: 6px 12px; border-radius: 8px; cursor: pointer; border: 1px solid var(--line, rgba(128,128,128,.35)); background: var(--panel2, rgba(128,128,128,.12)); color: var(--text); font-size: 13px; }
.btn-upload.disabled { opacity: .5; pointer-events: none; }
</style>
