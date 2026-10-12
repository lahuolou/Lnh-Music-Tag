<script setup>
import { onMounted, ref } from 'vue'
import { toast, busy } from '../store.js'
import { api } from '../api.js'
import { t } from '../i18n.js'

const logs = ref([])
const sel = ref(new Set())
const loading = ref(false)

async function load() {
  loading.value = true
  try {
    const r = await api('/api/logs')
    logs.value = r.logs || []
  } catch (e) { toast(t('logsLoadFail', { m: e.message }), 'err'); }
  loading.value = false
}

// 日志文案：主模板 + 跳过数 + 自由补充（scan 的 removed/skipped 单独解析）
function logText(l) {
  let s = t(l.action, { n: l.count, f: l.fail, d: l.detail || '' })
  if (l.action === 'act_scan') {
    const m = /removed:(\d+) skipped:(\d+)/.exec(l.detail || '')
    if (m) s += t('actScanExt', { k: m[2], r: m[1] })
  } else if (l.skip > 0) {
    s += t('actSkip', { s: l.skip })
  } else if (l.detail && (l.action === 'act_save' || l.action === 'act_scrapeone')) {
    s += ' · ' + l.detail
  }
  return s
}

function tog(id, ev) {
  if (ev.target.checked) sel.value.add(id); else sel.value.delete(id)
}
async function delSel() {
  if (!sel.value.size) { toast(t('selFirst'), 'err'); return; }
  try {
    const r = await api('/api/logs/delete', { method: 'POST', body: JSON.stringify({ ids: [...sel.value] }) })
    toast(t('logsDelDone', { n: r.removed }), 'ok')
    sel.value.clear()
    await load()
  } catch (e) { toast(t('logsDelFail', { m: e.message }), 'err'); }
}
async function clearAll() {
  if (!logs.value.length) return
  if (!confirm(t('logsClearConfirm'))) return
  try {
    await api('/api/logs/clear', { method: 'POST', body: '{}' })
    toast(t('logsCleared'), 'ok')
    await load()
  } catch (e) { toast(t('logsDelFail', { m: e.message }), 'err'); }
}

onMounted(load)
</script>

<template>
  <div>
    <div style="display:flex;gap:8px;align-items:center;flex-wrap:wrap">
      <button class="ghost sm" :disabled="busy() || loading" @click="load">{{ t('logsRefresh') }}</button>
      <button class="ghost sm danger" :disabled="busy() || !sel.size" @click="delSel">{{ t('logsDel') }} ({{ sel.size }})</button>
      <button class="ghost sm danger" :disabled="busy() || !logs.length" @click="clearAll">{{ t('logsClear') }}</button>
      <span class="muted" style="font-size:12px">{{ t('logsCount', { n: logs.length }) }}</span>
    </div>

    <div v-if="!logs.length" class="muted" style="margin-top:14px">{{ t('logsEmpty') }}</div>
    <div class="logs">
      <div v-for="l in logs" :key="l.id" class="logitem" :class="{ on: sel.has(l.id) }">
        <input type="checkbox" class="tchk" :checked="sel.has(l.id)" @change="tog(l.id, $event)" />
        <span class="tag" :class="l.ok ? 'ok' : 'err'">{{ l.ok ? t('logsOK') : t('logsFail') }}</span>
        <span class="lg-time">{{ l.time }}</span>
        <span class="lg-text">{{ logText(l) }}</span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.logs { margin-top: 10px; display: flex; flex-direction: column; gap: 4px; }
.logitem {
  display: flex; align-items: center; gap: 8px; padding: 6px 8px;
  border: 1px solid var(--line); border-radius: 8px; background: var(--card, transparent);
}
.logitem.on { outline: 1px solid var(--acc); }
.lg-time { color: var(--mut); font-size: 12px; white-space: nowrap; font-variant-numeric: tabular-nums; }
.lg-text { font-size: 13px; word-break: break-all; }
</style>
