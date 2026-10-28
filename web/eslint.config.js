// web/eslint.config.js — 前端 ESLint 配置（flat config, ESLint 9）
// 规则取向：只保留“正确性”检查（未用变量、未定义引用、Vue 语法错误），
// 关闭纯风格类规则（属性换行、组件命名、缩进），避免与现有代码风格冲突。
import pluginVue from 'eslint-plugin-vue';

export default [
  {
    ignores: ['dist/**', 'node_modules/**', 'e2e/**', '*.png'],
  },
  ...pluginVue.configs['flat/recommended'],
  {
    rules: {
      // 风格类规则关闭（与现有代码保持一致，不引入大规模格式整改）
      'vue/multi-word-component-names': 'off',
      'vue/max-attributes-per-line': 'off',
      'vue/html-indent': 'off',
      'vue/singleline-html-element-content-newline': 'off',
      'vue/multiline-html-element-content-newline': 'off',
      'vue/first-attribute-linebreak': 'off',
      'vue/html-closing-bracket-newline': 'off',
      'vue/attributes-order': 'off',
      'vue/html-self-closing': 'off',
      // 正确性规则：保持 recommended 默认，另加未用变量警告
      'no-unused-vars': ['warn', { argsIgnorePattern: '^_' }],
    },
  },
];
