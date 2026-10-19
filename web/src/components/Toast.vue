<script setup>
import { state } from '../store.js'
</script>

<template>
  <div class="toast" :class="[state.toast.show ? 'show' : '', state.toast.type]"
       :style="{ display: state.toast.show ? 'block' : 'none' }">{{ state.toast.msg }}</div>
</template>
