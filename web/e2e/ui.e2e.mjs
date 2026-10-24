// web/e2e/ui.e2e.mjs — 桌面 + 移动端 UI 冒烟测试（Playwright + 系统 Edge，不下载浏览器）。
//
// 用法（先启动后端，如 :10248）：
//   cd web && node e2e/ui.e2e.mjs
// 环境变量：LNH_E2E_BASE（默认 http://localhost:10248）、LNH_E2E_USER/PASS（默认 admin/admin123）
//
// 覆盖（文档 P2 移动端 E2E 的核心验收）：
//   - Web 登录（表单提交 → 进入曲库）
//   - 桌面视口：列表渲染 + 「品质」列存在
//   - 移动端视口（420×900）：卡片式歌曲展示、品质文本、筛选标签、全选条、滚动加载
import { chromium } from 'playwright-core';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const BASE = process.env.LNH_E2E_BASE || 'http://localhost:10248';
const USER = process.env.LNH_E2E_USER || 'admin';
const PASS = process.env.LNH_E2E_PASS || 'admin123';
const OUT_DIR = path.dirname(fileURLToPath(import.meta.url));

let failures = 0;
const ok = (name, cond, detail = '') => {
  console.log(`${cond ? 'PASS' : 'FAIL'} ${name}${detail ? ' — ' + detail : ''}`);
  if (!cond) failures++;
};

async function enterLibrary(page) {
  // 登录后可能仍在营销首页：只匹配明确的「进入后台」入口（避免误匹配「插件管理」）
  const entry = page.locator('button:has-text("进入后台"), a:has-text("进入后台")').first();
  if ((await entry.count()) > 0) {
    await entry.click();
    await page.waitForTimeout(1000);
  }
  // 等待曲库主体（表格/卡片）渲染；返回是否已进入列表
  try {
    await page.waitForFunction(
      () => document.querySelectorAll('tr, [class*="card"], [class*="row"]').length > 0,
      { timeout: 10000 }
    );
    await page.waitForTimeout(600);
    return true;
  } catch { return false; }
}

async function doLogin(page) {
  await page.goto(BASE + '/', { waitUntil: 'domcontentloaded', timeout: 20000 });
  await page.waitForTimeout(600);
  // 未登录时首页有「登录后台」入口，点开登录弹窗
  const entry = page.locator('button:has-text("登录后台"), a:has-text("登录后台")').first();
  if ((await entry.count()) > 0) {
    await entry.click();
    await page.waitForTimeout(600);
  }
  const pw = page.locator('input[type="password"]');
  if ((await pw.count()) === 0) return enterLibrary(page);
  const userInput = page.locator('input[type="text"], input:not([type="password"])').first();
  await userInput.fill('');
  await userInput.fill(USER);
  await page.waitForTimeout(200);
  await pw.fill('');
  await pw.fill(PASS);
  await page.waitForTimeout(300); // 等待 Vue 模型更新、按钮解除禁用
  const btn = page.locator('.login-btn, button[type="submit"]').first();
  await btn.click().catch(() => {});
  if (await enterLibrary(page)) return true;
  // 按钮点击未生效时按 Enter 提交表单兜底
  await page.keyboard.press('Enter');
  return enterLibrary(page);
}

const browser = await chromium.launch({ channel: 'msedge', headless: true });
const ctx = await browser.newContext();
try {
  // ——— 桌面（1280×800）———
  const page = await ctx.newPage();
  await page.setViewportSize({ width: 1280, height: 800 });
  ok('桌面：登录成功', await doLogin(page));
  // 列表主体渲染
  const bodyText = (await page.locator('body').innerText({ timeout: 8000 })).slice(0, 400);
  ok('桌面：页面有内容', bodyText.trim().length > 50, bodyText.slice(0, 80).replace(/\n/g, ' | '));
  // 「品质」列头
  const headText = await page.locator('th').allInnerTexts().catch(() => []);
  ok('桌面：「品质」列存在', headText.some((t) => t.includes('品质')), headText.join('/'));
  // 歌名/歌手/专辑列
  const essential = ['歌', '艺', '专'].filter((k) => headText.some((t) => t.includes(k)));
  ok('桌面：歌名/歌手/专辑列存在', essential.length >= 2, headText.join('/'));
  // 筛选按钮组
  const filters = ['全部', '乱码', '繁体', '无歌词', '无海报', '待识别', '待确认'];
  const bodyFull = await page.locator('body').innerText();
  const present = filters.filter((f) => bodyFull.includes(f));
  ok('桌面：筛选标签渲染', present.length >= 4, present.join('/'));

  // 待确认模态：点击标签进入筛选 → 点「待确认列表」按钮打开模态（空队列显示空提示），再点遮罩关闭
  await page.locator('button.chip', { hasText: '待确认' }).first().click();
  await page.waitForTimeout(400);
  await page.locator('button', { hasText: '待确认列表' }).first().click();
  await page.waitForTimeout(600);
  const modalOpen = await page.evaluate(() => !!document.querySelector('.modal-mask'));
  ok('桌面：待确认模态打开', modalOpen);
  const modalBody = await page.locator('.modal-mask').innerText().catch(() => '');
  ok('桌面：待确认空提示', modalBody.includes('没有待确认的刮削结果'), modalBody.slice(0, 60));
  await page.evaluate(() => {
    const m = document.querySelector('.modal-mask');
    if (m) m.click(); // @click.self 关闭
  });
  await page.waitForTimeout(300);
  const modalClosed = await page.evaluate(() => !document.querySelector('.modal-mask'));
  ok('桌面：待确认模态关闭', modalClosed);

  // ——— 移动端（420×900）———
  const mp = await ctx.newPage();
  await mp.setViewportSize({ width: 420, height: 900 });
  ok('移动端：登录成功', await doLogin(mp));
  const mpBody = (await mp.locator('body').innerText({ timeout: 8000 })).slice(0, 600);
  ok('移动端：页面有内容', mpBody.trim().length > 50, mpBody.slice(0, 80).replace(/\n/g, ' | '));
  // 品质文本（任意档位出现）
  const qualityWords = ['流畅', '普通', '高品', '无损', 'Hi-Res', '未知'];
  const shown = qualityWords.filter((q) => mpBody.includes(q));
  ok('移动端：品质文本渲染', shown.length >= 1, shown.join('/'));
  // 卡片视图（移动端断点下应无横向滚动需求：body 宽度 ≤ 视口）
  const scrollW = await mp.evaluate(() => document.documentElement.scrollWidth);
  ok('移动端：无横向溢出', scrollW <= 420, `scrollWidth=${scrollW}`);
  // 全选条（「全选」按钮）
  const mpFull = await mp.locator('body').innerText();
  ok('移动端：全选条存在', mpFull.includes('全选'));
  // 滚动加载：滚到实际可滚动容器底部后行数增加（哨兵触发）
  const rowSel = 'tr, [class*="card"], [class*="row"], [class*="item"]';
  const before = await mp.evaluate((s) => document.querySelectorAll(s).length, rowSel);
  await mp.evaluate(() => {
    // 找到最后一个可滚动容器（移动端列表可能是内部容器而非 window）
    const scrollers = [...document.querySelectorAll('*')].filter(
      (el) => el.scrollHeight > el.clientHeight + 50
    );
    const target = scrollers[scrollers.length - 1] || document.documentElement;
    target.scrollTop = target.scrollHeight;
  });
  await mp.waitForTimeout(2000);
  const after = await mp.evaluate((s) => document.querySelectorAll(s).length, rowSel);
  ok('移动端：滚动加载触发', after > before, `${before} → ${after}`);

  // 截图证据（保存到 web/e2e/ 下）
  await page.screenshot({ path: path.join(OUT_DIR, 'desktop.png') });
  await mp.screenshot({ path: path.join(OUT_DIR, 'mobile.png') });
  console.log('screenshots: web/e2e/desktop.png, web/e2e/mobile.png');
} catch (e) {
  failures++;
  console.log(`FAIL 执行异常 — ${e.message}`);
} finally {
  await browser.close();
}
console.log(failures === 0 ? '\nALL PASS' : `\n${failures} FAILURES`);
process.exit(failures === 0 ? 0 : 1);
