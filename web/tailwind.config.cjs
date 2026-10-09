function okVar(name) {
  return 'oklch(var(--' + name + ') / <alpha-value>)';
}

module.exports = {
  darkMode: 'class',
  content: ['./web/**/*.html', './web/assets/*.{js,mjs}'],
  theme: {
    extend: {
      colors: {
        paper: okVar('paper'),
        ink: okVar('ink'),
        card: okVar('card'),
        sand: okVar('sand'),
        muted: okVar('muted'),
        live: okVar('live'),
        danger: okVar('danger'),
        ok: okVar('ok'),
        warn: okVar('warn'),
        hit: okVar('hit'),
      },
      fontFamily: {
        display: ['ui-sans-serif', 'system-ui', '-apple-system', 'BlinkMacSystemFont', 'PingFang SC', 'Microsoft YaHei', 'sans-serif'],
        sans: ['Public Sans', 'PingFang SC', 'Hiragino Sans GB', 'Microsoft YaHei', 'ui-sans-serif', 'system-ui', 'sans-serif'],
        mono: ['JetBrains Mono', 'ui-monospace', 'SFMono-Regular', 'Menlo', 'Consolas', 'monospace'],
      },
      borderRadius: {
        page: '0',
        doc: '6px',
        control: '8px',
        surface: '10px',
        float: '12px',
      },
      maxWidth: { page: '1440px' },
    },
  },
};
