import { createApp } from 'vue'
import App from './App.vue'
import './style.css'

// 全局错误捕获：任何运行时错误都输出到控制台并在页面右下角提示，
// 避免“黑屏/白屏”时无从排查。不阻断正常渲染。
window.addEventListener('error', (e) => {
  console.error('[Lnh-Music-Tag] uncaught error:', e.message, e.filename, e.lineno)
  try {
    const div = document.createElement('div')
    div.textContent = '⚠ ' + (e.message || '页面运行出错')
    div.style.cssText = 'position:fixed;right:12px;bottom:12px;z-index:99999;max-width:70vw;' +
      'background:#c62828;color:#fff;padding:8px 12px;border-radius:8px;font-size:12px;' +
      'font-family:system-ui,sans-serif;box-shadow:0 2px 10px rgba(0,0,0,.35)'
    document.body.appendChild(div)
    setTimeout(() => div.remove(), 12000)
  } catch (err) { /* 忽略提示自身的错误 */ }
})

const app = createApp(App)
app.config.errorHandler = (err, _instance, info) => {
  console.error('[Lnh-Music-Tag] vue error:', err, info)
}
app.mount('#app')
