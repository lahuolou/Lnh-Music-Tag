// 音乐解锁插件面板：扫描加密音乐 → 勾选 → 批量解锁。
// 调用宿主接口：POST /api/fs/scan-dir（列目录文件）、POST /api/plugins/tool.unlock/run（执行解锁）。
(function () {
  'use strict';
  var PLUGIN = 'tool.unlock';
  // 常见加密音乐后缀（QQ音乐/网易云/酷狗/酷我/虾米等）
  var EXTS = ['.qmc0','.qmc3','.qmcflac','.qmcogg','.qmcwma','.tkm','.mflac','.mflac0','.mgg','.mgg1','.mggl','.ncm','.kgm','.kgma','.vpr','.kwm','.xm','.tm0','.tm1','.tm2','.tm3','.tm4','.tm5','.tm6','.tm7','.tm8','.tm9','.ac3','.bcstm'];
  var files = [];
  var busy = false;

  var $ = function (id) { return document.getElementById(id); };
  function log(msg, cls) {
    var d = $('logs');
    var div = document.createElement('div');
    div.className = cls || '';
    div.textContent = msg;
    d.appendChild(div);
    d.scrollTop = d.scrollHeight;
  }
  function fmtSize(n) {
    if (!n) return '0 B';
    if (n > 1 << 30) return (n / (1 << 30)).toFixed(1) + ' GB';
    if (n > 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB';
    if (n > 1 << 10) return (n / (1 << 10)).toFixed(1) + ' KB';
    return n + ' B';
  }
  function api(path, body) {
    var opt = { method: body === undefined ? 'GET' : 'POST', headers: { 'X-Requested-With': 'LNH-MusicTag' } };
    if (body !== undefined) { opt.headers['Content-Type'] = 'application/json'; opt.body = JSON.stringify(body); }
    return fetch(path, opt).then(function (r) {
      return r.json().catch(function () { return null; }).then(function (d) {
        if (!r.ok) throw new Error((d && d.error) || ('HTTP ' + r.status));
        return d;
      });
    });
  }
  function render() {
    var tb = $('tbody');
    tb.innerHTML = '';
    $('stats').textContent = '共 ' + files.length + ' 个加密文件';
    $('empty').style.display = files.length ? 'none' : '';
    $('ops').style.display = files.length ? '' : 'none';
    files.forEach(function (f, i) {
      var tr = document.createElement('tr');
      var ck = document.createElement('input');
      ck.type = 'checkbox'; ck.checked = true; ck.dataset.i = i;
      ck.onchange = function () { f.checked = ck.checked; };
      var td1 = document.createElement('td'); td1.appendChild(ck);
      var td2 = document.createElement('td');
      td2.textContent = f.fileName;
      var sub = document.createElement('div'); sub.className = 'path'; sub.textContent = f.path;
      td2.appendChild(sub);
      var td3 = document.createElement('td'); td3.textContent = fmtSize(f.size);
      var td4 = document.createElement('td'); td4.id = 'st' + i; td4.textContent = f.state || '';
      tr.appendChild(td1); tr.appendChild(td2); tr.appendChild(td3); tr.appendChild(td4);
      tb.appendChild(tr);
    });
    var h = $('headCk');
    h.checked = files.every(function (f) { return f.checked; });
    h.onchange = function () { files.forEach(function (f) { f.checked = h.checked; }); render(); };
  }
  $('scanBtn').onclick = function () {
    var dir = $('dir').value.trim();
    if (!dir) { log('请先输入音乐目录', 'bad'); return; }
    busy = true; $('scanBtn').disabled = true;
    $('logs').innerHTML = '';
    api('/api/fs/scan-dir', { dir: dir, exts: EXTS }).then(function (d) {
      files = (d.files || []).map(function (f) { return { path: f.path, fileName: f.fileName, ext: f.ext, size: f.size, checked: true, state: '' }; });
      render();
      log('扫描完成：' + dir + ' → 发现 ' + files.length + ' 个加密文件');
    }).catch(function (e) {
      log('扫描失败：' + e.message, 'bad');
    }).finally(function () { busy = false; $('scanBtn').disabled = false; });
  };
  $('allBtn').onclick = function () { files.forEach(function (f) { f.checked = true; }); render(); };
  $('invBtn').onclick = function () { files.forEach(function (f) { f.checked = !f.checked; }); render(); };
  $('unlockBtn').onclick = function () {
    var sel = files.filter(function (f) { return f.checked; });
    if (!sel.length) { log('未勾选任何文件', 'bad'); return; }
    if (busy) return;
    busy = true; $('unlockBtn').disabled = true;
    $('bar').style.display = '';
    var done = 0, okN = 0, failN = 0;
    function step(i) {
      if (i >= sel.length) {
        busy = false; $('unlockBtn').disabled = false;
        $('barIn').style.width = '100%';
        log('批量解锁完成：成功 ' + okN + ' 个，失败 ' + failN + ' 个', okN === sel.length ? 'ok' : 'bad');
        return;
      }
      var f = sel[i];
      var cell = $('st' + files.indexOf(f));
      if (cell) cell.textContent = '解锁中…';
      api('/api/plugins/' + PLUGIN + '/run', { op: 'unlock', file: f.path }).then(function (d) {
        okN++;
        f.state = '✓ 已解锁';
        if (cell) { cell.textContent = '✓ 已解锁'; cell.className = 'ok'; }
        log('成功：' + f.fileName + (d.ext ? ' → ' + d.ext : ''), 'ok');
      }).catch(function (e) {
        failN++;
        f.state = '✗ ' + (e.message || '失败');
        if (cell) { cell.textContent = '✗ 失败'; cell.className = 'bad'; }
        log('失败：' + f.fileName + ' — ' + e.message, 'bad');
      }).finally(function () {
        done++;
        $('barIn').style.width = Math.round(done / sel.length * 100) + '%';
        $('stats').textContent = '解锁进度：' + done + '/' + sel.length + '（成功 ' + okN + '，失败 ' + failN + '）';
        step(i + 1);
      });
    }
    step(0);
  };
  // 默认目录取主应用配置
  api('/api/settings').then(function (d) { if (d && d.scanDir) $('dir').value = d.scanDir; }).catch(function () {});
})();
