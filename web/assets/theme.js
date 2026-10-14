// Theme and language preferences are shared with the homepage and documentation site on this origin.
(function () {
  try {
    document.documentElement.classList.toggle('dark', localStorage.getItem('context-theme') === 'dark');
    var saved = localStorage.getItem('context-locale');
    document.documentElement.lang = (saved ? /^zh/i.test(saved) : navigator.language.startsWith('zh')) ? 'zh-CN' : 'en';
  } catch (_) { document.documentElement.lang = 'zh-CN'; }
})();
