<script setup>
import { onMounted, ref } from 'vue'
import { toast } from '../store.js'
import { api } from '../api.js'
import { t } from '../i18n.js'
import { FIELDS, DEFAULT_EDIT_FIELDS } from '../fields.js'

const oldPass = ref('')
const newPass = ref('')
const confirm = ref('')
const msg = ref('')

const cfg = ref({ adminUser: '', autoFixTitle: true, autoRenameFile: true, scanDir: '', editFields: [] })
const cfgMsg = ref('')

// 任务计划（自动刮削）
const sched = ref({ enabled: false, cron: '0 3 * * *', mode: 'onekey', scanFirst: true, running: false, lastRun: '', lastMsg: '', nextRun: '' })
const schedMsg = ref('')

async function loadSched() {
  try {
    sched.value = await api('/api/scheduler');
  } catch (e) { schedMsg.value = t('schedLoadFail', { m: e.message }); }
}

async function saveSched() {
  schedMsg.value = '';
  try {
    await api('/api/scheduler', {
      method: 'POST',
      body: JSON.stringify({
        enabled: sched.value.enabled,
        cron: sched.value.cron,
        mode: sched.value.mode,
        scanFirst: sched.value.scanFirst
      })
    });
    toast(t('schedSaved'), 'ok');
    schedMsg.value = t('schedSavedDb');
    await loadSched();
  } catch (e) { schedMsg.value = t('schedSaveFail', { m: e.message }); }
}

async function runSched() {
  schedMsg.value = '';
  try {
    await api('/api/scheduler/run', { method: 'POST', body: '{}' });
    toast(t('schedRunOk'), 'ok');
    setTimeout(loadSched, 800);
  } catch (e) { schedMsg.value = e.message; }
}

async function doChange() {
  msg.value = '';
  if (newPass.value !== confirm.value) { msg.value = t('passMismatch'); return; }
  if (newPass.value.length < 6) { msg.value = t('passTooShort'); return; }
  try {
    await api('/api/change-password', { method: 'POST', body: JSON.stringify({ oldPass: oldPass.value, newPass: newPass.value }) });
    toast(t('passChanged'), 'ok');
    oldPass.value = newPass.value = confirm.value = '';
    msg.value = '';
  } catch (e) { msg.value = e.message; }
}

async function loadCfg() {
  try {
    cfg.value = await api('/api/settings');
    if (!Array.isArray(cfg.value.editFields) || !cfg.value.editFields.length) cfg.value.editFields = [...DEFAULT_EDIT_FIELDS];
  }
  catch (e) { toast(t('cfgLoadFail', { m: e.message }), 'err'); }
}

async function saveCfg() {
  cfgMsg.value = '';
  try {
    await api('/api/settings', {
      method: 'POST',
      body: JSON.stringify({
        adminUser: cfg.value.adminUser,
        autoFixTitle: cfg.value.autoFixTitle,
        autoRenameFile: cfg.value.autoRenameFile,
        scanDir: cfg.value.scanDir,
        editFields: cfg.value.editFields
      })
    });
    toast(t('cfgSaved'), 'ok');
    cfgMsg.value = t('cfgSavedDb');
  } catch (e) { cfgMsg.value = t('cfgSaveFail', { m: e.message }); }
}

function resetEditFields() { cfg.value.editFields = [...DEFAULT_EDIT_FIELDS]; }

onMounted(() => { loadCfg(); loadSched(); });
</script>

<template>
  <div class="grid2" style="align-items:start">
    <div>
      <div class="panel">
        <h2>{{ t('changePass') }}</h2>
        <p class="muted" style="margin:0 0 10px">{{ t('changePassHint') }}</p>
        <form @submit.prevent="doChange">
          <input type="text" autocomplete="username" style="display:none" tabindex="-1" aria-hidden="true" />
          <div class="field"><label>{{ t('oldPass') }}</label><input type="password" v-model="oldPass" autocomplete="current-password" /></div>
          <div class="field"><label>{{ t('newPass') }}</label><input type="password" v-model="newPass" autocomplete="new-password" /></div>
          <div class="field"><label>{{ t('confirmPass') }}</label><input type="password" v-model="confirm" autocomplete="new-password" /></div>
          <div class="row">
            <button type="submit">{{ t('confirmBtn') }}</button>
          </div>
        </form>
        <div class="loading login-msg">{{ msg }}</div>
      </div>

      <div class="panel">
        <h2>{{ t('tip') }}</h2>
        <p class="muted">{{ t('tipText') }}</p>
      </div>
    </div>

    <div>
      <div class="panel">
        <h2>{{ t('dbCfg') }}</h2>
        <div class="field"><label>{{ t('fAdmin') }}</label><input type="text" v-model="cfg.adminUser" /></div>
        <div class="field"><label>{{ t('fScanDir') }}</label><input type="text" v-model="cfg.scanDir" placeholder="/music" /></div>
        <div class="chk" style="margin:6px 0 14px">
          <input type="checkbox" id="autoFix" class="tchk" v-model="cfg.autoFixTitle" />
          <label for="autoFix">{{ t('optAutoFix') }}</label>
        </div>
        <div class="chk" style="margin:0 0 14px">
          <input type="checkbox" id="autoRename" class="tchk" v-model="cfg.autoRenameFile" />
          <label for="autoRename">{{ t('optAutoRename') }}</label>
        </div>
        <div style="margin:0 0 8px"><strong>{{ t('editFields') }}</strong></div>
        <p class="muted" style="margin:0 0 8px">{{ t('editFieldsHint') }}</p>
        <div class="field-grid" style="max-height:220px;overflow:auto">
          <label v-for="f in FIELDS" :key="f.id" class="chk">
            <input type="checkbox" class="tchk" :value="f.id" v-model="cfg.editFields" />
            {{ t(f.label) }}
          </label>
        </div>
        <div class="row" style="margin:8px 0 14px">
          <button class="ghost sm" @click="resetEditFields">{{ t('editFieldsReset') }}</button>
        </div>
        <div class="row">
          <button @click="saveCfg">{{ t('saveCfg') }}</button>
        </div>
        <div class="loading login-msg">{{ cfgMsg }}</div>
      </div>

      <div class="panel">
        <h2>{{ t('schedTitle') }}</h2>
        <p class="muted" style="margin:0 0 12px">{{ t('schedHint') }}</p>
        <div class="chk" style="margin:0 0 12px">
          <input type="checkbox" id="schedOn" class="tchk" v-model="sched.enabled" />
          <label for="schedOn">{{ t('schedEnable') }}</label>
        </div>
        <div class="field">
          <label>{{ t('schedCron') }}</label>
          <input type="text" v-model="sched.cron" placeholder="0 3 * * *" />
        </div>
        <p class="muted" style="margin:2px 0 10px">{{ t('schedCronHint') }}</p>
        <div class="field">
          <label>{{ t('schedMode') }}</label>
          <select v-model="sched.mode">
            <option value="onekey">{{ t('schedModeOnekey') }}</option>
            <option value="batch">{{ t('schedModeBatch') }}</option>
          </select>
        </div>
        <div class="chk" style="margin:6px 0 12px">
          <input type="checkbox" id="schedScan" class="tchk" v-model="sched.scanFirst" />
          <label for="schedScan">{{ t('schedScanFirst') }}</label>
        </div>
        <div class="row" style="margin:0 0 8px">
          <button @click="saveSched">{{ t('saveCfg') }}</button>
          <button class="ghost sm" @click="runSched">{{ t('schedRunNow') }}</button>
        </div>
        <p class="muted" style="margin:0 0 4px" v-if="sched.nextRun">{{ t('schedNext') }}: {{ sched.nextRun }}</p>
        <p class="muted" style="margin:0 0 4px" v-if="sched.lastRun">{{ t('schedLast') }}: {{ sched.lastRun }}（{{ sched.lastMsg }}）</p>
        <p class="muted" style="margin:0 0 4px;color:#c0392b" v-if="sched.running">{{ t('schedRunning') }}</p>
        <div class="loading login-msg">{{ schedMsg }}</div>
      </div>
    </div>
  </div>
</template>
