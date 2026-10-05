// Shared application chrome; browser identity is loaded from the server session.
(function () {
  var NAV = [
    { key: 'search', href: '/sourcegraph/', zh: '搜索', en: 'Search' },
    { key: 'connect', href: '/sourcegraph/connect', zh: '接入', en: 'Connect' },
    { key: 'repos', href: '/sourcegraph/repos', zh: '仓库', en: 'Repositories' },
    { key: 'ops', href: '/sourcegraph/operations', zh: '状态', en: 'Status' },
  ];

  var svg = function (d, cls) { return '<svg viewBox="0 0 24 24" class="' + (cls || 'h-4 w-4') + '" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round">' + d + '</svg>'; };
  // Where this site's own source lives; configured by deployment defaults so forks can point elsewhere.
  var SOURCE_REPO = /^https:\/\//.test((window.DEPLOYMENT && window.DEPLOYMENT.source_repository) || '') ? (window.DEPLOYMENT && window.DEPLOYMENT.source_repository) || '' : '';
  var ICON = {
    // sun / moon: Lucide "sun" and "moon" (ISC License, lucide.dev).
    sun: svg('<circle cx="12" cy="12" r="4"/><path d="M12 2v2M12 20v2M4.93 4.93l1.41 1.41M17.66 17.66l1.41 1.41M2 12h2M20 12h2M6.34 17.66l-1.41 1.41M19.07 4.93l-1.41 1.41"/>'),
    moon: svg('<path d="M12 3a6 6 0 0 0 9 9 9 9 0 1 1-9-9Z"/>'),
    // code: Lucide "code" (ISC License, lucide.dev).
    source: svg('<path d="m16 18 6-6-6-6"/><path d="m8 6-6 6 6 6"/>'),
    lock: svg('<rect x="5" y="11" width="14" height="9" rx="2"/><path d="M8 11V8a4 4 0 0 1 8 0v3"/>', 'h-3.5 w-3.5'),
    copy: svg('<rect x="9" y="9" width="11" height="11" rx="2"/><path d="M5 15V5a1 1 0 0 1 1-1h10"/>', 'h-3.5 w-3.5'),
    arrow: svg('<path d="M7 17 17 7M8 7h9v9"/>', 'h-3.5 w-3.5'),
    chevron: svg('<path d="m7 10 5 5 5-5"/>', 'h-3.5 w-3.5'),
    x: svg('<path d="M18 6 6 18M6 6l12 12"/>', 'h-3.5 w-3.5'),
    enter: svg('<path d="M9 10 4 15l5 5"/><path d="M20 4v7a4 4 0 0 1-4 4H4"/>', 'h-3.5 w-3.5'),
  };

  // Brand mark: a 5x5 dot matrix, the same vocabulary as the search field.
  var MARK = (function () {
    var on = '0111010001100011000101110'.split('');
    var dots = '';
    for (var i = 0; i < 25; i++) {
      var x = 2.4 + (i % 5) * 4.8, y = 2.4 + Math.floor(i / 5) * 4.8;
      dots += '<circle cx="' + x + '" cy="' + y + '" r="1.35" opacity="' + (on[i] === '1' ? 1 : 0.22) + '"/>';
    }
    return '<svg viewBox="0 0 24 24" class="h-5 w-5" fill="currentColor">' + dots + '<circle cx="21.6" cy="21.6" r="1.35" fill="oklch(var(--live))"/></svg>';
  })();

  function lang() { try { return localStorage.getItem('cs.lang') || (navigator.language.startsWith('zh') ? 'zh' : 'en'); } catch (_) { return 'zh'; } }
  function t(zh, en) { return lang() === 'en' ? en : zh; }
  function user() { return window.API.session && window.API.session.user || null; }
  function canManage() { return !!(window.API.session && window.API.session.permissions.manage); }
  function esc(s) { return String(s).replace(/[&<>"']/g, function (c) { return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]; }); }
  function isDark() { return document.documentElement.classList.contains('dark'); }

  function applyLang() {
    var en = lang() === 'en';
    document.documentElement.lang = en ? 'en' : 'zh-CN';
    document.querySelectorAll('[data-en]').forEach(function (el) {
      if (el.dataset.zh === undefined) el.dataset.zh = el.innerHTML;
      el.innerHTML = en ? el.dataset.en : el.dataset.zh;
    });
    document.querySelectorAll('[data-en-ph]').forEach(function (el) {
      if (el.dataset.zhPh === undefined) el.dataset.zhPh = el.getAttribute('placeholder') || '';
      el.setAttribute('placeholder', en ? el.dataset.enPh : el.dataset.zhPh);
    });
  }

  function toggleTheme() {
    var next = isDark() ? 'light' : 'dark';
    try { localStorage.setItem('cs.theme', next); } catch (_) {}
    var apply = function () { document.documentElement.classList.toggle('dark', next === 'dark'); renderHeader(); };
    if (document.startViewTransition && !matchMedia('(prefers-reduced-motion: reduce)').matches) document.startViewTransition(apply);
    else apply();
    document.dispatchEvent(new Event('themechange'));
  }

  function initial(u) {
    var m = /[A-Za-z]/.exec(u.name || '') || /[A-Za-z0-9]/.exec(u.email || '');
    return (m ? m[0] : (u.name || '?').slice(0, 1)).toUpperCase();
  }

  function avatar(u) {
    var letter = '<span class="grid h-7 w-7 place-items-center rounded-full bg-ink text-[12px] font-medium text-paper">' + esc(initial(u)) + '</span>';
    if (!u.picture || !/^https:\/\//.test(u.picture)) return letter;
    return '<img src="' + esc(u.picture) + '" alt="" class="h-7 w-7 rounded-full object-cover" onerror="this.outerHTML=this.dataset.fallback" data-fallback="' + esc(letter) + '">';
  }

  function renderHeader() {
    var host = document.getElementById('site-header');
    if (!host) return;
    var active = document.body.dataset.page;
    var u = user();
    var nav = NAV.map(function (n) {
      var on = n.key === active;
      return '<a href="' + n.href + '" class="px-2.5 py-1 text-[13.5px] transition-colors ' + (on ? 'text-ink' : 'text-muted hover:text-ink') + '"' + (on ? ' aria-current="page"' : '') + '>' + t(n.zh, n.en) + '</a>';
    }).join('');
    var account = u
      ? '<details id="account" class="relative"><summary class="grid h-8 w-8 cursor-pointer list-none place-items-center rounded-full hover:bg-sand" title="' + esc(u.name) + '" aria-label="' + esc(u.name) + '">' +
          avatar(u) + '</summary>' +
          '<div class="lift absolute right-0 z-40 mt-2 w-[200px] max-w-[200px] rounded-surface bg-card px-3 py-1.5 text-[13px]">' +
            '<div class="px-2 pb-2 pt-1.5"><div class="flex min-w-0 items-baseline gap-1.5"><span class="truncate font-medium">' + esc(u.name) + '</span><span class="shrink-0 text-[12px] font-normal text-muted">· ' + t(u.role === 'admin' ? '管理员' : '普通用户', u.role === 'admin' ? 'Admin' : 'User') + '</span></div><div class="truncate font-mono text-[12px] text-muted" title="' + esc(u.email) + '">' + esc(u.email) + '</div></div>' +
            '<a href="/sourcegraph/user-settings" class="block rounded-control px-2 py-1.5 hover:bg-sand">'+t('用户设置','User settings')+'</a>' +
            '<a href="/sourcegraph/repos" class="block rounded-control px-2 py-1.5 hover:bg-sand">' + t(canManage() ? '管理仓库' : '浏览仓库', canManage() ? 'Manage repositories' : 'Browse repositories') + '</a>' +
            (canManage() ? '<a href="/sourcegraph/users" class="block rounded-control px-2 py-1.5 hover:bg-sand"' + (active === 'users' ? ' aria-current="page"' : '') + '>' + t('管理用户', 'Manage users') + '</a><a href="/sourcegraph/settings" class="block rounded-control px-2 py-1.5 hover:bg-sand"' + (active === 'settings' ? ' aria-current="page"' : '') + '>' + t('系统设置', 'System settings') + '</a>' : '') +
            '<button id="logout" class="block w-full rounded-control px-2 py-1.5 text-left hover:bg-sand">' + t('退出登录', 'Sign out') + '</button>' +
          '</div></details>'
      : '<button id="login" class="rounded-control bg-ink px-3 py-1 text-[13px] font-medium text-paper hover:opacity-90">' + t('登录', 'Sign in') + '</button>';

    var minimal = document.body.dataset.header === 'minimal';
    var tools =
          '<button id="theme" class="grid h-8 w-8 place-items-center rounded-control text-muted hover:bg-sand hover:text-ink" aria-label="' + t('切换深浅色', 'Toggle theme') + '" title="' + t('切换深浅色', 'Toggle theme') + '">' + (isDark() ? ICON.sun : ICON.moon) + '</button>' +
          '<button id="lang" class="h-8 rounded-control px-2 font-mono text-[12px] text-muted hover:bg-sand hover:text-ink" title="' + t('Switch to English', '切换到中文') + '">' + (lang() === 'zh' ? 'EN' : '中') + '</button>' +
          (SOURCE_REPO && !minimal ? '<a id="source-repo" href="' + esc(SOURCE_REPO) + '" target="_blank" rel="noopener" class="hidden h-8 w-8 place-items-center rounded-control text-muted hover:bg-sand hover:text-ink sm:grid" aria-label="' + t('源代码仓库', 'Source repository') + '" title="' + t('源代码仓库', 'Source repository') + '">' + ICON.source + '</a>' : '');
    // The portal page carries only the brand and the shared theme and language switches.
    if (minimal) host.innerHTML =
      '<header class="relative z-30">' +
      '<div class="flex h-14 items-center justify-between px-5 sm:px-8">' +
        '<a href="/" class="flex items-center gap-2 text-[15px] font-medium tracking-tight">' + MARK + '<span>Context for AI</span><span class="rounded-[4px] px-1.5 py-px font-mono text-[10px] font-medium leading-4 tracking-[0.08em] text-muted ring-1 ring-inset ring-ink/15">LABS</span></a>' +
        '<div class="flex items-center gap-1">' + tools + '</div>' +
      '</div></header>';
    else host.innerHTML =
      '<header class="sticky top-0 z-30 bg-paper">' +
      '<div class="mx-auto flex h-14 max-w-page items-center justify-between px-5 sm:px-8 md:grid md:grid-cols-[1fr_auto_1fr]">' +
        '<a href="/sourcegraph/" class="flex items-center gap-2 justify-self-start text-[15px] font-medium tracking-tight">' + MARK +
          '<span class="whitespace-nowrap">Context Source Graph</span></a>' +
        '<nav class="hidden items-center md:flex">' + nav + '</nav>' +
        '<div class="flex items-center gap-1 justify-self-end">' +
          tools + '<span class="w-1"></span>' + account +
        '</div>' +
      '</div>' +
      '<nav class="flex justify-center gap-2 overflow-x-auto px-3 pb-2 text-[15px] md:hidden">' + nav.replace(/text-\[13\.5px\]/g, 'md:text-[13.5px]') + '</nav>' +
      '</header>';

    host.querySelector('#theme').onclick = toggleTheme;
    host.querySelector('#lang').onclick = function () {
      try { localStorage.setItem('cs.lang', lang() === 'en' ? 'zh' : 'en'); } catch (_) {}
      renderHeader(); applyLang(); footer(); document.dispatchEvent(new Event('langchange'));
    };
    var login = host.querySelector('#login');
    if (login) login.onclick = function () { requireLogin(location.pathname + location.search + location.hash); };
    var menu = host.querySelector('#account');
    if (menu && matchMedia('(hover: hover)').matches) {
      var timer;
      menu.addEventListener('mouseenter', function () { clearTimeout(timer); menu.open = true; });
      menu.addEventListener('mouseleave', function () { timer = setTimeout(function () { if (!menu.contains(document.activeElement)) menu.open = false; }, 150); });
      menu.addEventListener('focusin', function () { clearTimeout(timer); });
      menu.addEventListener('focusout', function (e) { if (!menu.contains(e.relatedTarget)) menu.open = false; });
    }
    if (menu) menu.addEventListener('keydown', function (e) { if (e.key === 'Escape') { e.preventDefault(); menu.open = false; menu.querySelector('summary').focus(); } });
    var logout = host.querySelector('#logout');
    if (logout) logout.onclick = async function () { logout.disabled = true; try { await API.request('/auth/logout', { method: 'POST' }); await API.loadSession(); renderHeader(); toast(t('已退出登录', 'Signed out')); } catch (e) { toast(e.message, 'bad'); } finally { logout.disabled = false; } };
  }

  function requireLogin(returnTo) {
    if (typeof returnTo === 'function') { if (user()) { returnTo(); return; } returnTo = location.pathname + location.search + location.hash; }
    var target = new URL(returnTo || location.href, document.baseURI);
    if (target.origin !== location.origin || !target.pathname.startsWith('/sourcegraph/')) target = new URL('/sourcegraph/', location.origin);
    if (target.hash === '#new') { target.searchParams.set('new', '1'); target.hash = ''; }
    location.href = '/sourcegraph/login?return=' + encodeURIComponent(target.pathname + target.search + target.hash);
  }

  function toast(msg, kind) {
    var el = document.createElement('div');
    el.className = 'lift fade-in fixed bottom-6 left-1/2 z-50 -translate-x-1/2 rounded-float bg-card px-4 py-2.5 text-[13px] ' + (kind === 'bad' ? 'text-danger' : '');
    el.textContent = msg;
    document.body.appendChild(el);
    setTimeout(function () { el.remove(); }, 2200);
  }

  function copy(text, okMsg) {
    var done = function () { toast(okMsg || t('已复制', 'Copied')); };
    if (navigator.clipboard) navigator.clipboard.writeText(text).then(done, function () { toast(t('复制失败，请手动选择', 'Copy failed, select manually'), 'bad'); });
    else { var field = document.createElement('textarea'); field.value = text; field.style.position = 'fixed'; field.style.opacity = '0'; document.body.appendChild(field); field.select(); try { if (!document.execCommand('copy')) throw new Error('copy'); done(); } catch (_) { toast(t('复制失败，请手动选择', 'Copy failed, select manually'), 'bad'); } field.remove(); }
  }

  // Status is a dot plus words. Only an in-progress state is blue.
  var STATUS = {
    ready: ['text-ok', '可查询', 'Searchable'],
    stale: ['text-warn', '更新失败，旧版本可查', 'Update failed, older version searchable'],
    preparing: ['dot-live', '首次准备中', 'Preparing'],
    disabled: ['text-muted/60', '已停用', 'Disabled'],
    succeeded: ['text-ok', '成功', 'Succeeded'],
    failed: ['text-danger', '失败', 'Failed'],
    running: ['dot-live', '运行中', 'Running'],
    queued: ['text-muted/60', '排队中', 'Queued'],
    canceled: ['text-muted/60', '已取消', 'Canceled'],
    cancelled: ['text-muted/60', '已取消', 'Canceled'],
    pending: ['text-muted/60', '尚未准备', 'Not prepared'],
    missing: ['text-danger', '需要重新同步', 'Needs resync'],
    deleted: ['text-muted/60', '已标记删除', 'Marked for deletion'],
  };
  var validTime = function (x) { return !!x && !String(x).startsWith('0001-') && Number.isFinite(Date.parse(x)); };
  // Shared display model over a catalog row or an availability record (which also carries versions and jobs).
  var Repo = {
    indexed: function (v) { return typeof v.indexed === 'boolean' ? v.indexed : v.shards > 0; },
    head: function (r) { var bs = (r.policy && r.policy.branches) || [], h = r.heads || {}; var b = (r.policy?.default_branch && h[r.default_branch] ? r.default_branch : null) || bs.find(function (x) { return h[x]; }) || Object.keys(h)[0]; return b ? { branch: b, commit: h[b] } : null; },
    versions: function (r) { return (r.versions || []).filter(Repo.indexed).sort(function (a, b) { return Date.parse(b.committer_time) - Date.parse(a.committer_time); }); },
    servable: function (r) { var vs = Repo.versions(r), h = Repo.head(r); return (h && vs.find(function (v) { return v.commit === h.commit; })) || vs[0] || null; },
    jobs: function (r) { return (r.jobs || []).slice().sort(function (a, b) { return Date.parse(b.created_at) - Date.parse(a.created_at); }); },
    recoveredAt: function (r, j) {
      if (!j || j.state !== 'failed') return null;
      var group = j.group || '', end = Date.parse(j.updated_at);
      if (!Number.isFinite(end)) return null;
      if (j.kind === 'sync') {
        var c = (r.sync_checks || {})[group];
        if (c && !c.error && Date.parse(c.checked_at) > end) return c.checked_at;
      }
      var later = (r.jobs || []).filter(function (v) {
        return v.state === 'succeeded' && (v.group || '') === group && v.kind === j.kind &&
          (j.kind === 'sync' || (j.commit && v.commit === j.commit)) && Date.parse(v.updated_at) > end;
      }).sort(function (a,b) { return Date.parse(b.updated_at)-Date.parse(a.updated_at); });
      return later.length ? later[0].updated_at : null;
    },
    currentJobs: function (r) {
      return Repo.jobs(r).filter(function (j) { return !Repo.recoveredAt(r, j); });
    },
    latestCheck: function (r) {
      return Object.entries(r.sync_checks || {}).map(function (entry) { return Object.assign({group:entry[0]},entry[1]); })
        .filter(function (c) { return validTime(c.checked_at); })
        .sort(function (a,b) { return Date.parse(b.checked_at)-Date.parse(a.checked_at); })[0];
    },
    state: function (r) {
      if (r.deleted) return 'deleted';
      if (!r.enabled) return 'disabled';
      if (r.storage_missing) return 'missing';
      var jobs = Repo.currentJobs(r), last = jobs[0], h = Repo.head(r);
      var running = jobs.some(function (j) { return j.state === 'queued' || j.state === 'running'; });
      var published = (r.published_index_count || 0) > 0 || Repo.versions(r).length > 0;
      if (published) return last && last.state === 'failed' && !(h && Repo.versions(r).some(function (v) { return v.commit === h.commit; })) ? 'stale' : 'ready';
      if (running) return 'preparing';
      return last && last.state === 'failed' ? 'failed' : 'pending';
    },
  };
  // HH:MM today, otherwise MM-DD HH:MM.
  function clock(x) {
    if (!validTime(x)) return '-';
    var d = new Date(x), now = new Date(), p = function (n) { return String(n).padStart(2, '0'); };
    var hm = p(d.getHours()) + ':' + p(d.getMinutes());
    return d.toDateString() === now.toDateString() ? hm : (d.getFullYear() === now.getFullYear() ? '' : d.getFullYear() + '-') + p(d.getMonth() + 1) + '-' + p(d.getDate()) + ' ' + hm;
  }
  function status(state) {
    var m = STATUS[state] || ['text-muted', state, state];
    return '<span class="inline-flex items-center gap-1.5 whitespace-nowrap text-[13px]"><span class="dot ' + m[0] + '"></span>' + t(m[1], m[2]) + '</span>';
  }

  function short(s) { return s ? s.slice(0, 8) : '-'; }

  // number-roll: renders digits as sliding strips; call again with a new value to roll.
  function roll(el, text) {
    text = String(text);
    var prev = el.dataset.roll || '';
    var shape = function (s) { return s.replace(/\d/g, '0'); };
    if (shape(prev) !== shape(text) || !el.querySelector('.roll')) {
      el.innerHTML = '<span class="roll">' + text.split('').map(function (ch) {
        if (!/\d/.test(ch)) return '<span>' + esc(ch) + '</span>';
        return '<span class="roll-d"><span class="roll-v">0</span><span class="roll-s" aria-hidden="true" style="transform:translateY(0)"></span></span>';
      }).join('') + '</span>';
      el.getBoundingClientRect();
    }
    var strips = el.querySelectorAll('.roll-s'), values = el.querySelectorAll('.roll-v'), k = 0;
    text.split('').forEach(function (ch) { if (/\d/.test(ch)) { strips[k].style.transform = 'translateY(' + (-+ch) + 'em)'; values[k].textContent = ch; k++; } });
    el.dataset.roll = text;
  }

  function footer() {
    var host = document.getElementById('site-footer');
    if (!host) return;
    host.innerHTML = '<footer class="mt-24 border-t rule"><div class="mx-auto flex max-w-page flex-wrap items-center gap-x-6 gap-y-2 px-5 py-8 text-[13px] text-muted sm:px-8">' +
      '<span class="flex items-center gap-2 text-ink">' + MARK + 'Context Source Graph</span>' +
      '<a class="hover:text-ink" href="/sourcegraph/connect#mcp">MCP</a><a class="hover:text-ink" href="/sourcegraph/connect#bash">CLI</a><a class="hover:text-ink" href="/sourcegraph/connect#http">HTTP</a>' +
      '<span class="w-full sm:ml-auto sm:w-auto">Built by Context4AI</span>' +
      '</div></footer>';
  }

  window.App = { ready: API.ready, canManage: canManage, t: t, lang: lang, user: user, esc: esc, applyLang: applyLang, requireLogin: requireLogin, toast: toast, copy: copy, status: status, short: short, roll: roll, Repo: Repo, clock: clock, validTime: validTime, ICON: ICON, MARK: MARK, isDark: isDark };

  document.addEventListener('DOMContentLoaded', function () {
    renderHeader(); footer(); applyLang();
    API.ready.then(function () { renderHeader(); document.dispatchEvent(new Event('app:auth')); });
    document.addEventListener('app:auth', renderHeader);
    document.addEventListener('click', function (e) {
      document.querySelectorAll('details[open]').forEach(function (d) { if (!d.contains(e.target)) d.removeAttribute('open'); });
    });
  });
})();
