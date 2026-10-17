<script setup>
import { state } from '../store.js'
import { t } from '../i18n.js'

defineProps({ open: Boolean })
const emit = defineEmits(['close'])
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-mask" @click.self="emit('close')">
      <div class="modal small">
        <div class="h3">🎵 Lnh-Music-Tag</div>
        <p class="muted" style="margin:10px 0;line-height:1.8;white-space:pre-wrap">{{ t('aboutInfo', { v: state.version || '' }) }}</p>
        <div class="modal-foot">
          <button @click="emit('close')">{{ t('close') }}</button>
        </div>
      </div>
    </div>
  </Teleport>
</template>
