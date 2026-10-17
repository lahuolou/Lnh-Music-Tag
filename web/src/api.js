// 统一 API 封装：自动附带 CSRF 头（后端 csrfGuard 校验 X-Requested-With），
// 401 时通知全局登出；请求体由后端 limitBody 限制。
// 注意：调用方自定义 headers 与默认头合并，绝不覆盖 X-Requested-With（否则
// 大请求体会在 csrfGuard 提前返回时因未读 body 被 Go 直接断开，报 Failed to fetch）。
export async function api(path, opts = {}) {
  const headers = Object.assign(
    { 'Content-Type': 'application/json', 'X-Requested-With': 'Lnh-Music-Tag' },
    opts.headers || {}
  );
  const r = await fetch(path, Object.assign({}, opts, { headers }));
  if (r.status === 401 && !/\/login|\/me$/.test(path)) {
    window.dispatchEvent(new CustomEvent('unauth'));
    throw new Error('未登录或会话过期');
  }
  const data = await r.json().catch(() => null);
  if (!r.ok) throw new Error((data && data.error) || ('HTTP ' + r.status));
  return data;
}
