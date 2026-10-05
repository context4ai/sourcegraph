(function () {
  try {
    var saved = localStorage.getItem('cs.theme');
    document.documentElement.classList.toggle('dark', saved === 'dark');
    document.documentElement.lang = (localStorage.getItem('cs.lang') || (navigator.language.startsWith('zh') ? 'zh' : 'en')) === 'en' ? 'en' : 'zh-CN';
  } catch (_) { document.documentElement.lang = 'zh-CN'; }
})();
