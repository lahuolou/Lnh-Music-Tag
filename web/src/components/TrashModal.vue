<script setup>
import { ref, watch } from 'vue'
import { toast } from '../store.js'
import { api } from '../api.js'
import { t } from '../i18n.js'

const props = defineProps({ open: Boolean })
const emit = defineEmits(['close'])

const items = ref([])
const loading = ref(false)
const busy = ref(false)

function formatSize(n) {
  if (!n && n !== 0) return '-'
  if (n < 1024) return n + ' B'
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB'
  if (n < 1024 * 1024 * 1024) return (n / 1024 / 1024).toFixed(1) + ' MB'
  return (n / 1024 / 1024 / 1024).toFixed(2) + ' GB'
}

async function load() {
  loading.value = true
  try {
    const r = await api('/api/trash') || {};
    items.value = r.items || [];
  } catch (e) { toast(t('trashLoadFail', { m: e.message }), 'err'); }
  finally { loading.value = false; }
}

async function restore(it) {
  busy.value = true;
  try {
    const r = await api('/api/trash/restore', { method: 'POST', body: JSON.stringify({ name: it.name }) });
    items.value = items.value.filter(x => x.name !== it.name);
    toast(t('trashRestoreOk', { p: r.restored }), 'ok');
  } catch (e) { toast(t('trashRestoreFail', { m: e.message }), 'err'); }
  finally { busy.value = false; }
}

async function delOne(it) {
  if (!confirm(t('delConfirmTrash', { n: 1 }))) return;
  busy.value = true;
  try {
    await api('/api/trash/delete', { method: 'POST', body: JSON.stringify({ name: it.name }) });
    items.value = items.value.filter(x => x.name !== it.name);
    toast(t('trashDeleteOk'), 'ok');
  } catch (e) { toast(t('trashDeleteFail', { m: e.message }), 'err'); }
  finally { busy.value = false; }
}

async function clearAll() {
  if (!confirm(t('trashClearConfirm', { n: items.value.length }))) return;
  busy.value = true;
  try {
    await api('/api/trash/clear', { method: 'POST', body: '{}' });
    items.value = [];
    toast(t('trashClearOk'), 'ok');
  } catch (e) { toast(t('trashClearFail', { m: e.message }), 'err'); }
  finally { busy.value = false; }
}

watch(() => props.open, v => { if (v) load(); });
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-mask" @click.self="emit('close')">
      <div class="modal wide">
        <div class="modal-head">
          <div class="h3">🗑 {{ t('trashTitle') }}</div>
          <button class="ghost sm danger" :disabled="busy || !items.length" @click="clearAll">{{ t('trashClear') }} ({{ items.length }})</button>
        </div>

        <p class="muted small" style="margin:6px 0 10px">{{ t('trashRestoreHint') }}</p>

        <div v-if="loading" class="muted" style="padding:24px;text-align:center">…</div>
        <div v-else-if="!items.length" class="muted" style="padding:24px;text-align:center">{{ t('trashEmpty') }}</div>
        <div v-else class="trash-list">
          <div class="trash-row" v-for="it in items" :key="it.name">
            <div class="trash-info">
              <div class="trash-name" :title="it.name">{{ it.name }}</div>
              <div class="trash-origin" :class="{ unknown: !it.originalPath }" :title="it.originalPath">
                {{ it.originalPath || t('trashOriginUnknown') }}
              </div>
            </div>
            <span class="muted small" style="flex:none">{{ formatSize(it.size) }}</span>
            <span class="muted small" style="flex:none">{{ it.modTime }}</span>
            <span class="trash-acts" @click.stop>
              <button class="sm" :disabled="busy" @click="restore(it)">{{ t('trashRestore') }}</button>
              <button class="sm danger" :disabled="busy" @click="delOne(it)">{{ t('trashDelete') }}</button>
            </span>
          </div>
        </div>

        <div class="modal-foot">
          <button class="ghost" @click="emit('close')">{{ t('cancel') }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>

<style scoped>
.modal.wide { width: min(720px, 94vw); }
.modal-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.trash-list { max-height: 52vh; overflow-y: auto; border: 1px solid var(--line, rgba(128,128,128,.25)); border-radius: 10px; }
.trash-row { display: flex; align-items: center; gap: 10px; padding: 8px 12px; border-bottom: 1px solid var(--line, rgba(128,128,128,.12)); }
.trash-row:last-child { border-bottom: none; }
.trash-info { flex: 1; min-width: 0; }
.trash-name { font-size: 13px; font-weight: 600; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.trash-origin { font-size: 12px; opacity: .65; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.trash-origin.unknown { color: #e5484d; opacity: .9; }
.trash-acts { display: flex; gap: 6px; flex: none; }
button.danger { color: #e34d59; }
</style>
