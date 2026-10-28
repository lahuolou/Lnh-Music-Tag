<script setup>
import { onUnmounted, ref } from 'vue'
import { state, toast, refresh, startToolJob, stopToolJob } from '../store.js'
import { api } from '../api.js'
import { t } from '../i18n.js'

const props = defineProps({ open: Boolean })
const emit = defineEmits(['close'])

let _fmtTimer = null // 转码完成轮询句柄：对话框卸载时兜底清理

// 对话框关闭/组件卸载时清理残留轮询（任务完成/超时仍各自清理，行为不变）
onUnmounted(() => { if (_fmtTimer) { clearInterval(_fmtTimer); _fmtTimer = null; } })

const inputFormats = ref([]) // 空 = 全部格式
const target = ref('keep')
const bitrate = ref('keep')
const sampleRate = ref('keep')
const busy = ref(false)

const inFormats = ['FLAC', 'MP3', 'APE', 'WAV', 'AIFF', 'AIF', 'TTA', 'M4A', 'OGG', 'OPUS', 'WMA', 'DSF', 'DFF', 'WMV', 'DTS', 'AAC']
const outFormats = [
  { v: 'keep', label: () => t('keep') }, { v: 'mp3', label: 'MP3' }, { v: 'flac', label: 'FLAC' },
  { v: 'm4a', label: 'M4A' }, { v: 'ogg', label: 'OGG' }, { v: 'opus', label: 'OPUS' },
  { v: 'wav', label: 'WAV' }, { v: 'aiff', label: 'AIFF' }, { v: 'aac', label: 'AAC' },
  { v: 'wma', label: 'WMA' }, { v: 'tta', label: 'TTA' }
]
const bitrates = [
  { v: 'keep', label: () => t('keepOrig') }, { v: '64k', label: '64 kbps' }, { v: '128k', label: '128 kbps' },
  { v: '192k', label: '192 kbps' }, { v: '256k', label: '256 kbps' }, { v: '320k', label: '320 kbps' }
]
const rates = [
  { v: 'keep', label: () => t('keepOrig') }, { v: '44100', label: '44.1 kHz' }, { v: '48000', label: '48 kHz' },
  { v: '88200', label: '88.2 kHz' }, { v: '96000', label: '96 kHz' }
]

function toggleIn(f) {
  const i = inputFormats.value.indexOf(f);
  if (i >= 0) inputFormats.value.splice(i, 1); else inputFormats.value.push(f);
}

async function run() {
  if (!state.selected.size) { toast(t('selFirst'), 'err'); return; }
  busy.value = true;
  try {
    const r = await api('/api/convert', {
      method: 'POST',
      body: JSON.stringify({
        ids: [...state.selected],
        target: target.value,
        inputFormats: inputFormats.value.map(x => x.toLowerCase()),
        bitrate: bitrate.value,
        sampleRate: sampleRate.value,
        removeSource: false
      })
    });
    await startToolJob(r.jobId, r.total);
    // 等待后台任务完成（进度在批量页全局可见）
    const t0 = Date.now();
    if (_fmtTimer) { clearInterval(_fmtTimer); _fmtTimer = null; }
    const iv = setInterval(() => {
      const tj = state.toolJob;
      if (tj && tj.finished) {
        clearInterval(iv); _fmtTimer = null;
        if (tj.status === 'failed') { toast(t('fmtFail', { m: tj.message || t('jobLost') }), 'err'); }
        else { toast(t('fmtDone', { ok: tj.ok, t: tj.total }), tj.ok === tj.total ? 'ok' : 'err'); }
        state.selected.clear();
        busy.value = false;
        refresh();
        emit('close');
      } else if (!tj || Date.now() - t0 > 120000) { // 任务丢失或 2 分钟未完成：解锁避免卡死
        clearInterval(iv); _fmtTimer = null;
        busy.value = false;
      }
    }, 500);
    _fmtTimer = iv;
  } catch (e) { toast(t('fmtFail', { m: e.message }), 'err'); busy.value = false; }
}
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-mask" @click.self="emit('close')">
      <div class="modal fmt">
        <div class="h3">{{ t('fmtTitle') }}</div>
        <div class="muted" style="margin-bottom:10px">{{ t('fmtDesc') }}</div>
        <div class="muted" style="font-size:12px;line-height:1.7">
          {{ t('fmtNote') }}
        </div>

        <div class="h4">{{ t('fmtInput') }}</div>
        <div class="fl">{{ t('fmtInFormat') }}</div>
        <div class="muted" style="font-size:12px">{{ t('fmtInFormatHint') }}</div>
        <div class="fmt-chips">
          <button v-for="f in inFormats" :key="f" class="chip" :class="{ on: inputFormats.includes(f) }" @click="toggleIn(f)">{{ f }}</button>
        </div>
        <div class="muted" style="font-size:12px">{{ t('fmtEmptyAll') }}</div>

        <div class="h4">{{ t('fmtTarget') }}</div>
        <div class="fmt-grid">
          <div>
            <div class="fl">{{ t('fmtTarget') }}</div>
            <div class="muted" style="font-size:12px">{{ t('fmtHint2') }}</div>
            <select v-model="target" class="sel">
              <option v-for="o in outFormats" :key="o.v" :value="o.v">{{ typeof o.label === 'function' ? o.label() : o.label }}</option>
            </select>
          </div>
          <div>
            <div class="fl">{{ t('fmtBitrate') }}</div>
            <div class="muted" style="font-size:12px">{{ t('fmtHint3') }}</div>
            <select v-model="bitrate" class="sel">
              <option v-for="o in bitrates" :key="o.v" :value="o.v">{{ typeof o.label === 'function' ? o.label() : o.label }}</option>
            </select>
          </div>
        </div>
        <div style="margin-top:10px">
          <div class="fl">{{ t('fmtSample') }}</div>
          <div class="muted" style="font-size:12px">{{ t('fmtHint4') }}</div>
          <select v-model="sampleRate" class="sel" style="min-width:180px">
            <option v-for="o in rates" :key="o.v" :value="o.v">{{ typeof o.label === 'function' ? o.label() : o.label }}</option>
          </select>
        </div>

        <div class="modal-foot">
          <span class="muted" style="font-size:12px">{{ t('willConvert', { n: state.selected.size }) }}</span>
          <button class="ghost" @click="emit('close')">{{ t('cancel') }}</button>
          <button :disabled="busy || state.selected.size === 0" @click="run">{{ busy ? t('converting') : state.selected.size === 0 ? t('selBtn') : t('confirm') }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
