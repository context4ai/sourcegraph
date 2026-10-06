(function () {
  const A = window.App;
  const $ = id => document.getElementById(id);
  let state = 'loading';
  let providers = [];
  const params = new URLSearchParams(location.search);
  let returnTo = params.get('return_to') || params.get('return') || '/sourcegraph/';
  try {
    const target = new URL(returnTo, location.origin);
    if (target.origin !== location.origin || !target.pathname.startsWith('/sourcegraph/') || /\/(?:auth|login)(?:\/|$)/.test(target.pathname)) throw Error();
    returnTo = target.pathname + target.search + target.hash;
  } catch (_) { returnTo = '/sourcegraph/'; }
  $('mark').innerHTML = A.MARK;
  function render() {
    A.applyLang();
    const en = A.lang() === 'en';
    $('login-lang').textContent = en ? '中文' : 'EN';
    $('login-lang').setAttribute('aria-label', en ? '切换为中文' : 'Switch to English');
    document.title = A.t('登录', 'Sign in') + ' · Context Source Graph';
    $('login-title').textContent = state === 'empty' ? A.t('登录尚未配置', 'Sign-in isn’t configured yet') : state === 'error' ? A.t('登录暂时不可用', 'Sign-in is unavailable') : A.t('欢迎回来', 'Welcome back');
    $('login-detail').textContent = state === 'loading' ? A.t('正在获取登录方式…', 'Loading sign-in options…') : state === 'empty' ? A.t('本站尚未配置 Google 或 GitHub 登录。站点管理员可按照配置手册启用登录；配置完成后，首个成功登录的用户将成为管理员。', 'Google or GitHub sign-in hasn’t been configured for this site. Follow the setup guide to enable it. The first user to sign in successfully will become the administrator.') : state === 'error' ? A.t('暂时无法加载登录方式。你可以重试，或继续匿名浏览公开代码。', 'We couldn’t load sign-in options. Try again, or continue exploring public code without signing in.') : A.t('使用你的账号登录，继续探索代码与知识。', 'Sign in with your account to continue exploring code and knowledge.');
    $('login-progress').hidden = state !== 'loading';
    $('login-retry').hidden = state !== 'error';
    $('login-guide').hidden = state !== 'empty';
    $('login-providers').replaceChildren();
    $('login-providers').hidden = !providers.length;
    for (const provider of providers) {
      const link = document.createElement('a');
      link.className = 'flex items-center justify-center gap-3 rounded-control border rule px-4 py-3.5 text-[14px] font-medium hover:bg-sand';
      link.href = '/sourcegraph/auth/' + provider + '/login?return_to=' + encodeURIComponent(returnTo);
      link.textContent = A.t('使用 ', 'Continue with ') + (provider === 'github' ? 'GitHub' : 'Google') + A.t(' 登录', '');
      $('login-providers').append(link);
    }
  }
  async function load() {
    state = 'loading'; providers = []; render();
    try {
      const response = await fetch('/sourcegraph/auth/providers', { cache: 'no-store', signal: AbortSignal.timeout(10000) });
      if (!response.ok) throw Error();
      const data = await response.json();
      if (!Array.isArray(data.providers)) throw Error();
      providers = [...new Set(data.providers.filter(p => p === 'github' || p === 'google'))];
      state = providers.length ? 'ready' : 'empty';
    } catch (_) { state = 'error'; }
    render();
  }
  $('login-lang').onclick = function () {
    try { localStorage.setItem('cs.lang', A.lang() === 'en' ? 'zh' : 'en'); } catch (_) {}
    render();
  };
  $('login-retry').onclick = load;
  load();
})();
