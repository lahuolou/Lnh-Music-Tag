<script setup>
import { computed, onMounted, ref, watch } from 'vue'
import { state, logout, resumeJobs, verState, checkVersion } from './store.js'
import { api } from './api.js'
import { t, lang, setLang } from './i18n.js'
import Home from './views/Home.vue'
import Login from './views/Login.vue'
import MusicTree from './components/MusicTree.vue'
import EditPanel from './components/EditPanel.vue'
import BatchPanel from './components/BatchPanel.vue'
import DedupPanel from './components/DedupPanel.vue'
import ExtPanel from './components/ExtPanel.vue'
import Settings from './views/Settings.vue'
import Plugins from './views/Plugin.vue'
import ActivityLog from './components/ActivityLog.vue'
import Toast from './components/Toast.vue'
import UpdateModal from './components/UpdateModal.vue'
import AboutModal from './components/AboutModal.vue'
import TrashModal from './components/TrashModal.vue'

const theme = ref(localStorage.getItem('lnh-theme') || (window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'))
function applyTheme() {
  document.documentElement.dataset.theme = theme.value;
  localStorage.setItem('lnh-theme', theme.value);
}
function toggleTheme() { theme.value = theme.value === 'dark' ? 'light' : 'dark'; applyTheme(); }

function tab(name) { state.activeTab = name; }
function toggleLang() { setLang(lang.value === 'zh' ? 'en' : 'zh'); }
function goHome() { state.view = 'home'; }

const aboutOpen = ref(false)
const gearOpen = ref(false)
const trashOpen = ref(false)

// 第三方插件（非内置且有 Web 面板）动态注册到「编辑/批量/去重」之后的 tab 区
const extPanels = ref([])
async function loadExtPanels() {
  try {
    extPanels.value = ((await api('/api/plugins'))?.plugins || []).filter(p => !p.builtin && p.panel);
    // 当前激活的插件 tab 已被删除时，自动切回编辑页
    if (state.activeTab && state.activeTab.startsWith('ext:') && !extPanels.value.some(p => 'ext:' + p.name === state.activeTab)) {
      state.activeTab = 'edit';
    }
  } catch (e) { extPanels.value = []; }
}
const extActive = computed(() => {
  if (!state.activeTab || !state.activeTab.startsWith('ext:')) return null;
  return extPanels.value.find(p => 'ext:' + p.name === state.activeTab) || null;
});

// 顶部全局任务徽标：同一时刻只显示一个正在运行的任务（优先批量刮削，
// 否则工具任务）；进入批量页时由页内统一进度条承担显示，顶部隐藏，避免双进度条。
const topJob = computed(() => {
  if (state.batchJob && state.batchJob.status !== 'done') return { type: 'batch', d: state.batchJob.done, t: state.batchJob.total };
  if (state.toolJob && state.toolJob.status === 'running') return { type: 'tool', d: state.toolJob.done, t: state.toolJob.total };
  return null;
});

// 登录成功后自动从公开首页进入后台
watch(() => state.authed, v => { if (v) { state.view = 'app'; loadExtPanels(); } })

onMounted(async () => {
  applyTheme();
  window.addEventListener('plugins-changed', loadExtPanels); // 插件导入/删除后实时刷新动态 tab
  try {
    const m = await api('/api/me');
    state.authed = true;
    state.user = m.user;
    state.view = 'app'; // 已有会话（刷新恢复）直接进后台
  } catch (e) {
    state.authed = false;
  }
  checkVersion(); // 已登录（如刷新恢复会话）时检查更新；未登录时静默失败，登录成功后由 Login.vue 再查
  if (state.authed) { resumeJobs(); loadExtPanels(); } // 刷新/换设备后恢复进行中的批量任务进度
});
</script>

<template>
  <!-- 公开首页：搜索占位，无需登录；登录/进入后台切到后台区 -->
  <Home v-if="state.view === 'home'" />

  <!-- 后台区：登录页 + 登录后的全部管理页面 -->
  <template v-else>
    <Login v-if="!state.authed" />
    <template v-else>
    <nav class="topnav">
      <span class="brand" :title="t('home')" @click="goHome">{{ t('brand') }}</span>
      <span class="spacer"></span>
      <span class="badge">{{ t('tracks', { n: state.tracks.length }) }}</span>
      <!-- 新版本徽标：点击打开更新弹窗 -->
      <a v-if="verState.ver" class="badge newver" :title="t('newVerTitle')" @click="verState.showUpdate = true">{{ t('newVer') }} v{{ verState.ver.latest }}</a>
      <!-- 全局任务进度（单个徽标）：批量/工具任务后台运行时任意页面可见；
           在批量页内由页内统一进度条显示，此处隐藏避免双进度条 -->
      <span v-if="state.activeTab !== 'batch' && topJob" class="badge batch" :title="t('batchRunningTitle')" @click="tab('list')">
        {{ topJob.type === 'batch' ? t('batchProgress', { d: topJob.d, t: topJob.t }) : t('jobRunning', { d: topJob.d, t: topJob.t, c: '' }) }}
      </span>
      <button class="ghost sm" :class="{ on: !['settings', 'plugins', 'logs'].includes(state.activeTab) }" @click="tab('list')">{{ t('list') }}</button>
      <!-- 齿轮二级导航（按用户指定顺序）：主题 / 语言 / 插件 / 日志 / 回收站 / 设置 / 更新 / 关于 / 退出 -->
      <div class="gear" :class="{ open: gearOpen }" @click="gearOpen = !gearOpen">
        <button class="ghost sm gear-btn" :title="t('menu')">
          <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="3"/><path d="M19.4 15a1.65 1.65 0 0 0 .33 1.82l.06.06a2 2 0 0 1 0 2.83 2 2 0 0 1-2.83 0l-.06-.06a1.65 1.65 0 0 0-1.82-.33 1.65 1.65 0 0 0-1 1.51V21a2 2 0 0 1-2 2 2 2 0 0 1-2-2v-.09A1.65 1.65 0 0 0 9 19.4a1.65 1.65 0 0 0-1.82.33l-.06.06a2 2 0 0 1-2.83 0 2 2 0 0 1 0-2.83l.06-.06a1.65 1.65 0 0 0 .33-1.82 1.65 1.65 0 0 0-1.51-1H3a2 2 0 0 1-2-2 2 2 0 0 1 2-2h.09A1.65 1.65 0 0 0 4.6 9a1.65 1.65 0 0 0-.33-1.82l-.06-.06a2 2 0 0 1 0-2.83 2 2 0 0 1 2.83 0l.06.06a1.65 1.65 0 0 0 1.82.33H9a1.65 1.65 0 0 0 1-1.51V3a2 2 0 0 1 2-2 2 2 0 0 1 2 2v.09a1.65 1.65 0 0 0 1 1.51 1.65 1.65 0 0 0 1.82-.33l.06-.06a2 2 0 0 1 2.83 0 2 2 0 0 1 0 2.83l-.06.06a1.65 1.65 0 0 0-.33 1.82V9a1.65 1.65 0 0 0 1.51 1H21a2 2 0 0 1 2 2 2 2 0 0 1-2 2h-.09a1.65 1.65 0 0 0-1.51 1z"/></svg>
        </button>
        <div class="gear-menu" @click.stop>
          <button @click="toggleTheme(); gearOpen = false">{{ theme === 'dark' ? t('darkMode') : t('lightMode') }}</button>
          <button @click="toggleLang(); gearOpen = false">{{ lang === 'zh' ? '切换EN' : 'Switch CN' }}</button>
          <button @click="tab('plugins'); gearOpen = false">{{ t('plugins') }}</button>
          <button @click="tab('logs'); gearOpen = false">{{ t('logs') }}</button>
          <button @click="trashOpen = true; gearOpen = false">{{ t('trash') }}</button>
          <button @click="tab('settings'); gearOpen = false">{{ t('settings') }}</button>
          <button @click="checkVersion(true); gearOpen = false">{{ t('checkUpdate') }}</button>
          <button @click="aboutOpen = true; gearOpen = false">{{ t('about') }}</button>
          <button class="danger" @click="logout">{{ t('logout') }}</button>
        </div>
      </div>
    </nav>

    <!-- 工作台：左 音乐列表（扫描/搜索/筛选/勾选），右 编辑/批量/去重 -->
    <div v-if="state.activeTab !== 'settings' && state.activeTab !== 'plugins' && state.activeTab !== 'logs'" class="page merge">
      <div class="split">
        <div class="left">
          <MusicTree />
        </div>
        <div class="right">
          <div class="subtabs">
            <button class="tab" :class="{ on: state.activeTab === 'edit' }" @click="tab('edit')">{{ t('edit') }}</button>
            <button class="tab" :class="{ on: state.activeTab === 'batch' }" @click="tab('batch')">{{ t('batch') }}</button>
            <button class="tab" :class="{ on: state.activeTab === 'dedup' }" @click="tab('dedup')">{{ t('dedup') }}</button>
            <!-- 第三方插件 Web 面板动态入口（编辑/批量/去重 之后） -->
            <button v-for="e in extPanels" class="tab ext" :class="{ on: state.activeTab === 'ext:' + e.name }" @click="tab('ext:' + e.name)">{{ e.label }}</button>
          </div>
          <EditPanel v-if="state.activeTab === 'edit' || state.activeTab === 'list'" />
          <BatchPanel v-else-if="state.activeTab === 'batch'" />
          <DedupPanel v-else-if="state.activeTab === 'dedup'" />
          <ExtPanel v-else-if="extActive" :plugin="extActive" />
        </div>
      </div>
    </div>

    <div v-else-if="state.activeTab === 'plugins'" class="page">
      <Plugins />
    </div>

    <div v-else-if="state.activeTab === 'logs'" class="page">
      <ActivityLog />
    </div>

    <div v-else class="page">
      <Settings />
    </div>
    </template>
  </template>
  <UpdateModal :open="verState.showUpdate" :data="verState.ver" @close="verState.showUpdate = false" @later="verState.showUpdate = false" @view="verState.showUpdate = false" />
  <AboutModal :open="aboutOpen" @close="aboutOpen = false" />
  <TrashModal :open="trashOpen" @close="trashOpen = false" />
  <Toast />
</template>
