<script setup>
import { ref } from 'vue'
import { state, resumeJobs, checkVersion } from '../store.js'
import { api } from '../api.js'
import { t } from '../i18n.js'

const emit = defineEmits(['done'])
const user = ref('')
const pass = ref('')
const msg = ref('')
const showPw = ref(false)
const busy = ref(false)

async function doLogin() {
  if (busy.value) return;
  msg.value = '';
  if (!user.value.trim() || !pass.value) { msg.value = t('loginEmpty'); return; }
  busy.value = true;
  msg.value = t('loggingIn');
  try {
    await api('/api/login', { method: 'POST', body: JSON.stringify({ user: user.value.trim(), pass: pass.value }) });
    state.authed = true;
    state.user = user.value.trim();
    emit('done'); // 关闭登录弹层（App 监听到 authed 自动切到后台）
    resumeJobs(); // 换设备登录：接回服务端仍在运行的批量任务
    checkVersion(); // 登录后检查新版本，有更新则弹出提醒（无更新不弹）
  } catch (e) { msg.value = e.message; }
  finally { busy.value = false; }
}
</script>

<template>
  <div class="login-overlay">
    <div class="login-box">
      <div class="login-brand">🎵</div>
      <h2>LNH-MusicTag</h2>
      <p class="login-sub">{{ t('loginSub') }}</p>
      <form class="login-form" @submit.prevent="doLogin">
        <div class="field">
          <label>{{ t('username') }}</label>
          <input type="text" v-model="user" autocomplete="username" />
        </div>
        <div class="field">
          <label>{{ t('password') }}</label>
          <div class="pw-row">
            <input :type="showPw ? 'text' : 'password'" v-model="pass" autocomplete="current-password" />
            <button class="ghost sm" type="button" @click="showPw = !showPw" title="show/hide">👁</button>
          </div>
        </div>
        <button class="login-btn" type="submit" :disabled="busy">{{ busy ? t('loggingIn') : t('loginBtn') }}</button>
      </form>
      <div class="loading login-msg">{{ msg }}</div>
    </div>
  </div>
</template>
