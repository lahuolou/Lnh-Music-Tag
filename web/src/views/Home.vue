<script setup>
import { ref } from 'vue'
import { state } from '../store.js'
import { t } from '../i18n.js'
import Login from './Login.vue'

// 公开首页：仅展示项目介绍。
const showLogin = ref(false)

function enterApp() {
  if (state.authed) { state.view = 'app'; }
  else { showLogin.value = true; }
}
</script>

<template>
  <div class="home">
    <div class="home-hero">
      <div class="home-logo">🎵</div>
      <h1 class="home-title">LNH-MusicTag</h1>
      <p class="home-tag">{{ t('homeTagline') }}</p>

      <div class="home-actions">
        <button class="primary" @click="enterApp">{{ state.authed ? t('enterAdmin') : t('loginAdmin') }}</button>
        <button v-if="state.authed" class="ghost" @click="state.view = 'home'">{{ t('home') }}</button>
      </div>
    </div>

    <div class="home-feats">
      <div class="feat" v-for="f in ['featTags', 'featScrape', 'featDedup', 'featBatch', 'featConvert', 'featIdentify']" :key="f">
        {{ t(f) }}
      </div>
    </div>

    <p class="home-foot muted">{{ t('homeFoot') }}</p>

    <!-- 登录（后台）弹层 -->
    <Login v-if="showLogin" @done="showLogin = false" />
  </div>
</template>

<style scoped>
.home { min-height: 100vh; display: flex; flex-direction: column; align-items: center; justify-content: center; gap: 28px; padding: 24px; }
.home-hero { text-align: center; display: flex; flex-direction: column; align-items: center; gap: 14px; }
.home-logo { font-size: 56px; line-height: 1; }
.home-title { margin: 0; font-size: 34px; letter-spacing: 1px; }
.home-tag { margin: 0; color: var(--muted, rgba(128,128,128,.8)); }
.home-actions { display: flex; gap: 10px; margin-top: 6px; }
.home-actions .primary { padding: 9px 24px; border-radius: 10px; border: 0; background: var(--accent, #2f80ed); color: #fff; font-weight: 600; cursor: pointer; }
.home-actions .ghost { padding: 9px 20px; border-radius: 10px; border: 1px solid rgba(128,128,128,.35); background: transparent; color: inherit; cursor: pointer; }
.home-feats { display: flex; flex-wrap: wrap; justify-content: center; gap: 10px; max-width: 720px; }
.feat { padding: 8px 16px; border-radius: 20px; background: var(--bg2, rgba(128,128,128,.08)); font-size: 13px; }
.home-foot { font-size: 12px; }
</style>
