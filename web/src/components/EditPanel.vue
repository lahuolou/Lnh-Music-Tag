<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { state, cur, toast, refresh, busy } from '../store.js'
import { api } from '../api.js'
import { t } from '../i18n.js'
import { FIELDS, DEFAULT_EDIT_FIELDS } from '../fields.js'

// 可编辑字段 → 标签键
const TAG_KEYS = {
  title: 'TITLE', artist: 'ARTIST', album: 'ALBUM', albumartist: 'ALBUMARTIST',
  genre: 'GENRE', year: 'DATE', tracknumber: 'TRACKNUMBER', tracktotal: 'TRACKTOTAL',
  composer: 'COMPOSER', lyricist: 'LYRICIST', language: 'LANGUAGE',
  albumtype: 'RELEASETYPE', comment: 'COMMENT', discnumber: 'DISCNUMBER',
  disctotal: 'DISCTOTAL',
}

// 编辑面板显示字段（设置页配置，默认常用 12 项）
const editFields = ref([...DEFAULT_EDIT_FIELDS])
async function loadEditFields() {
  try {
    const s = await api('/api/settings');
    if (Array.isArray(s.editFields) && s.editFields.length) editFields.value = s.editFields;
    else editFields.value = [...DEFAULT_EDIT_FIELDS];
  } catch (e) { /* 配置拉取失败用默认 */ }
}

const f = ref(Object.fromEntries(Object.keys(TAG_KEYS).map(k => [k, ''])))
f.value.lyrics = ''
const sQuery = ref('')
const sSource = ref('auto')
const searching = ref(false)
let lastId = null

// 音频后缀正则（loadTrack 文件名解析共用）
const AUDIO_EXT = /\.(mp3|flac|m4a|aac|ogg|opus|wav|wma|ape|mpc|aiff|mka)$/i

// 手动刮削要写入的字段（可选）
const sf = reactive({
  title: true, artist: true, albumArtist: true, genre: true, year: true, trackNumber: true, lyrics: true
})
const sfCover = ref(true)

function selFields(withCover) {
  const names = [];
  const m = { title: 'title', artist: 'artist', albumArtist: 'albumArtist', genre: 'genre', year: 'year', trackNumber: 'trackNumber', lyrics: 'lyrics' };
  for (const k in m) { if (sf[k]) names.push(m[k]); }
  if (withCover) names.push('cover');
  return names;
}

async function loadTrack(t) {
  if (!t) return;
  lastId = t.id;
  // 标题/艺术家为空时默认从源文件名载入（如“陈小春 - 街角的晚风.flac” → 艺术家=陈小春，标题=街角的晚风）
  let title = t.tags.TITLE || '';
  let artist = t.tags.ARTIST || '';
  if (!title) {
    const base = (t.fileName || '').replace(AUDIO_EXT, '');
    const m = base.match(/^(.*?)\s*-\s*(.+)$/);
    title = m ? m[2].trim() : base.trim();
    if (!artist && m) artist = m[1].trim();
  }
  const tv = {};
  for (const k in TAG_KEYS) tv[k] = t.tags[TAG_KEYS[k]] || '';
  f.value = { ...tv, title, artist };
  f.value.lyrics = t.tags.LYRICS || '';
  sQuery.value = title + ' ' + artist;
  sSource.value = 'auto';
  // 列表瘦身后歌词正文不下发：编辑时经单曲接口按需拉取。
  if (!f.value.lyrics && (t.hasLyrics || t.tags.LYRICS === undefined)) {
    try {
      const full = await api('/api/track/' + encodeURIComponent(t.id));
      if (full && full.tags) {
        for (const k in TAG_KEYS) if (full.tags[TAG_KEYS[k]] && !f.value[k]) f.value[k] = full.tags[TAG_KEYS[k]];
        f.value.lyrics = full.tags.LYRICS || '';
      }
    } catch (e) { /* 单曲拉取失败不影响其余字段编辑 */ }
  }
}

watch(() => state.currentId, (id) => {
  if (id === lastId) return;
  loadTrack(state.tracks.find(t => t.id === id));
});

async function save() {
  if (!cur()) return;
  const tags = {};
  for (const k in TAG_KEYS) {
    const v = String(f.value[k] || '').trim();
    if (v !== '') tags[TAG_KEYS[k]] = v;
  }
  const body = { clear: false, tags };
  try {
    const r = await api('/api/tracks/' + encodeURIComponent(state.currentId) + '/tags', { method: 'POST', body: JSON.stringify(body) });
    toast(t('saved') + (r.renamed ? t('renamed', { name: r.name }) : ''), 'ok');
    await refresh();
    if (r.renamed) loadTrack(state.tracks.find(t => t.id === state.currentId) || cur());
  } catch (e) { toast(t('saveFail', { m: e.message }), 'err'); }
}

async function getLyrics() {
  if (!cur()) return;
  const title = f.value.title.trim(), artist = f.value.artist.trim();
  if (!title) { toast(t('fillTitle'), 'err'); return; }
  f.value.lyrics = t('fetchingLyrics');
  const artists = artist ? artist.split('/').map(s => s.trim()).filter(Boolean) : [];
  try {
    const r = await api('/api/lyrics?source=auto&title=' + encodeURIComponent(title) + '&artists=' + encodeURIComponent(artists.join('||')));
    f.value.lyrics = r.lyric || '';
    if (!r.lyric) toast(t('noLyric'), 'err');
  } catch (e) { f.value.lyrics = ''; toast(t('lyricFail', { m: e.message }), 'err'); }
}
function clearLyrics() { f.value.lyrics = ''; }

function fmtSize(s) {
  if (!s) return '';
  const mb = s / 1048576;
  return mb >= 1024 ? (mb / 1024).toFixed(1) + ' GB' : mb.toFixed(1) + ' MB';
}
function fmtDur(d) {
  if (!d) return '';
  const m = Math.floor(d / 60), s = Math.floor(d % 60);
  return m + ':' + String(s).padStart(2, '0');
}

// 按设置配置渲染：可编辑字段（除歌词单独处理）+ 只读信息字段
const shownEditable = computed(() =>
  FIELDS.filter(x => x.editable && x.id !== 'lyrics' && editFields.value.includes(x.id)));
const shownReadonly = computed(() =>
  FIELDS.filter(x => !x.editable && x.id !== 'cover' && editFields.value.includes(x.id)));
const showCover = computed(() => editFields.value.includes('cover'));

function readVal(id) {
  const t = cur(); if (!t) return '';
  switch (id) {
    case 'filename': return t.fileName;
    case 'lrcfile': return t.hasLrcFile ? '✓' : '—';
    case 'bitrate': return t.bitrate ? t.bitrate + ' kbps' : '—';
    case 'samplerate': return t.sampleRate ? (t.sampleRate / 1000).toFixed(1) + ' kHz' : '—';
    case 'bitdepth': return t.bitDepth ? t.bitDepth + ' bit' : '—';
    case 'channels': return t.channels ? t.channels + ' ch' : '—';
    case 'duration': return fmtDur(t.duration) || '—';
    case 'filesize': return fmtSize(t.size) || '—';
    case 'sha256': return t.sha256 ? t.sha256.slice(0, 20) + '…' : '—';
    case 'path': return t.path;
    case 'mbtrack': return t.tags.MUSICBRAINZ_TRACKID || '—';
    case 'mbalbum': return t.tags.MUSICBRAINZ_ALBUMID || '—';
    default: return '';
  }
}

// 导入同目录外挂 .lrc 歌词到标签
async function importLrc() {
  if (!cur()) return;
  try {
    const r = await api('/api/tracks/' + encodeURIComponent(state.currentId) + '/lrcfile');
    f.value.lyrics = r.content || '';
    toast(t('lrcImported', { name: r.name }), 'ok');
  } catch (e) { toast(t('lrcFail', { m: e.message }), 'err'); }
}

async function clearCover() {
  if (!cur()) return;
  try {
    await api('/api/tracks/' + encodeURIComponent(state.currentId) + '/cover', { method: 'POST', body: JSON.stringify({ clear: true }) });
    toast(t('coverCleared'), 'ok');
    await refresh();
  } catch (e) { toast(t('coverFail', { m: e.message }), 'err'); }
}

async function mbSearch() {
  const q = sQuery.value.trim();
  if (!q) { toast(t('enterKeyword'), 'err'); return; }
  searching.value = true;
  state.mbRes = [];
  try {
    // auto/空源：后端并发查询全部启用源并合并结果（多源同时采集）
    const res = await api('/api/scrape/search?q=' + encodeURIComponent(q) + '&source=' + encodeURIComponent(sSource.value) + '&limit=8');
    state.mbRes = res || [];
  } catch (e) { toast(t('searchFail', { m: e.message }), 'err'); }
  searching.value = false;
}

async function apply(i, withCover) {
  const r = state.mbRes[i];
  if (!r || !cur()) return;
  const body = {
    source: r.source, sourceId: r.sourceId, title: r.title, artists: r.artists || [],
    album: r.album, albumArtist: r.albumArtist || [], genre: r.genre || [],
    trackNumber: r.trackNumber || 0, date: r.date,
    coverURL: r.coverURL, albumID: r.albumID || '',
    releaseMBID: r.releaseMBID || '', fetchCover: withCover,
    fields: selFields(withCover)
  };
  try {
    await api('/api/tracks/' + encodeURIComponent(state.currentId) + '/scrape', { method: 'POST', body: JSON.stringify(body) });
    toast(t('scraped'), 'ok');
    await refresh();
  } catch (e) { toast(t('scrapeFail', { m: e.message }), 'err'); }
}

function srcLabel(name) { return (state.sources.find(s => s.name === name) || { label: name }).label; }

onMounted(loadEditFields);
</script>

<template>
  <div v-if="cur()" class="form">
    <div class="row" style="margin-bottom:10px">
      <img v-if="showCover" class="cover-lg" :src="`/api/tracks/${encodeURIComponent(cur().id)}/cover`" onerror="this.style.visibility='hidden'" />
      <div style="flex:1;min-width:0">
        <div style="font-weight:600">{{ cur().tags.TITLE || cur().fileName }}</div>
        <div class="muted" style="font-size:13px;word-break:break-all">{{ cur().path }}</div>
        <div class="muted" style="font-size:12px">
          {{ fmtSize(cur().size) }} · {{ fmtDur(cur().duration) }} · {{ cur().bitrate || '?' }} kbps · {{ cur().sampleRate ? (cur().sampleRate / 1000).toFixed(1) + ' kHz' : '' }} · SHA256: {{ cur().sha256 ? cur().sha256.slice(0, 20) + '…' : '—' }}
        </div>
      </div>
    </div>

    <template v-for="field in shownEditable" :key="field.id">
      <label>{{ t(field.label) }}</label>
      <input type="text" v-model="f[field.id]" />
    </template>

    <div v-if="editFields.includes('lyrics')" style="display:flex;align-items:center;justify-content:space-between;margin-top:14px">
      <label style="margin:0">{{ t('fLyrics') }}</label>
      <div class="row" style="gap:6px">
        <button v-if="cur().hasLrcFile" class="ghost sm" :disabled="busy()" @click="importLrc" :title="t('lrcTitle')">{{ t('importLrc') }}</button>
        <button class="ghost sm" :disabled="busy()" @click="getLyrics">{{ t('getLyrics') }}</button>
        <button class="ghost sm" :disabled="busy()" @click="clearLyrics">{{ t('clearLyrics') }}</button>
      </div>
    </div>
    <textarea v-if="editFields.includes('lyrics')" rows="8" v-model="f.lyrics"></textarea>

    <template v-for="field in shownReadonly" :key="field.id">
      <label>{{ t(field.label) }}</label>
      <div class="ro-val">{{ readVal(field.id) }}</div>
    </template>

    <div class="row" style="margin-top:12px">
      <button class="sm" :disabled="busy()" @click="save">{{ t('saveTags') }}</button>
      <button v-if="showCover" class="ghost sm" :disabled="busy()" @click="clearCover">{{ t('clearCover') }}</button>
    </div>

    <label style="margin-top:14px">{{ t('searchScrape') }}</label>
    <div class="row">
      <select v-model="sSource" style="padding:7px 10px;border-radius:8px;border:1px solid var(--line);background:var(--panel2);color:var(--text);min-width:120px">
        <option v-for="s in state.sources" :key="s.name" :value="s.name" v-show="s.name === 'auto' || s.enabled !== false">{{ s.label }}</option>
      </select>
      <input type="text" v-model="sQuery" :placeholder="t('searchPlaceholder')" @keydown.enter="mbSearch" />
      <button class="sm" :disabled="busy() || searching" @click="mbSearch">{{ t('search') }}</button>
    </div>

    <div v-if="searching" class="loading" style="margin-top:8px">{{ t('searching') }}</div>
    <div v-else-if="state.mbRes.length === 0" class="muted" style="margin-top:8px">{{ t('noResults') }}</div>
    <div v-else>
      <label style="margin:10px 0 4px">{{ t('applyFields') }}</label>
      <div class="field-grid">
        <label class="chk"><input type="checkbox" class="tchk" v-model="sf.title" /> {{ t('bTitle') }}</label>
        <label class="chk"><input type="checkbox" class="tchk" v-model="sf.artist" /> {{ t('bArtist') }}</label>
        <label class="chk"><input type="checkbox" class="tchk" v-model="sf.albumArtist" /> {{ t('bAlbumArtist') }}</label>
        <label class="chk"><input type="checkbox" class="tchk" v-model="sf.genre" /> {{ t('bGenre') }}</label>
        <label class="chk"><input type="checkbox" class="tchk" v-model="sf.year" /> {{ t('bYear') }}</label>
        <label class="chk"><input type="checkbox" class="tchk" v-model="sf.trackNumber" /> {{ t('bTrack') }}</label>
        <label class="chk"><input type="checkbox" class="tchk" v-model="sf.lyrics" /> {{ t('bLyrics') }}</label>
        <label class="chk"><input type="checkbox" class="tchk" v-model="sfCover" /> {{ t('bCover') }}</label>
      </div>
    </div>
    <div v-for="(r, i) in state.mbRes" :key="i" class="res-item">
      <div class="row" style="gap:10px;align-items:flex-start">
        <img v-if="r.coverURL" class="thumb" :src="r.coverURL" onerror="this.style.visibility='hidden'" />
        <div style="flex:1;min-width:0">
          <div class="t"><span class="tag src">{{ srcLabel(r.source) }}</span> {{ r.title }} <span class="muted">{{ r.artists ? r.artists.join(', ') : '' }}</span></div>
          <div class="m">{{ r.album }} <span class="muted">({{ r.date || t('unknown') }})</span></div>
          <div class="acts">
            <button class="sm" :disabled="busy()" @click="apply(i, true)">{{ t('applyWithCover') }}</button>
            <button class="ghost sm" :disabled="busy()" @click="apply(i, false)">{{ t('applyTagsOnly') }}</button>
          </div>
        </div>
      </div>
    </div>
  </div>
  <div v-else class="muted">{{ t('selectTrackHint') }}</div>
</template>
