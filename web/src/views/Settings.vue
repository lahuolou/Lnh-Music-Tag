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

onMounted(loadCfg);
</script>

<template>
  <div class="grid2" style="align-items:start">
    <div>
      <div class="panel">
        <h2>{{ t('changePass') }}</h2>
        <p class="muted" style="margin:0 0 10px">{{ t('changePassHint') }}</p>
        <div class="field"><label>{{ t('oldPass') }}</label><input type="password" v-model="oldPass" autocomplete="current-password" /></div>
        <div class="field"><label>{{ t('newPass') }}</label><input type="password" v-model="newPass" autocomplete="new-password" /></div>
        <div class="field"><label>{{ t('confirmPass') }}</label><input type="password" v-model="confirm" autocomplete="new-password" @keydown.enter="doChange" /></div>
        <div class="row">
          <button @click="doChange">{{ t('confirmBtn') }}</button>
        </div>
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
    </div>
  </div>
</template>
