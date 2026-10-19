<script setup>
// 第三方插件 Web 面板容器：以 iframe 嵌入插件包自带的 UI（解耦、隔离）。
// 插件页面通过同源 fetch 调用 /api/plugins/{name}/run 与 /api/fs/scan-dir 等接口。
import { computed } from 'vue'
const props = defineProps({ plugin: { type: Object, required: true } })
const src = computed(() => '/api/plugins/' + encodeURIComponent(props.plugin.name) + '/panel/' + String(props.plugin.panel || '').replace(/^\//, ''))
</script>

<template>
  <iframe class="ext-frame" :src="src" frameborder="0" sandbox="allow-scripts allow-same-origin"></iframe>
</template>

<style scoped>
.ext-frame {
  width: 100%;
  height: calc(100vh - 190px);
  min-height: 420px;
  border: 1px solid var(--line, rgba(128, 128, 128, .25));
  border-radius: 10px;
  background: var(--panel, #fff);
}
</style>
