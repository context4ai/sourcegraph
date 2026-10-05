// Connection panel shared by the home and connect pages: client tabs, the API Token toggle and the copyable snippet.
(function () {
  'use strict';
  var A = window.App, API = window.API, C = window.Clients, t = A.t;
  var TIP = ['匿名可以查询公开仓库，无需 Token；查询私有仓库或发起版本索引则需要 Token；Token 在登录后的用户设置中获取',
    'Public repositories can be queried anonymously without a token. Private repositories and version indexing need a token, available in User settings after signing in'];

  function mount(root, options) {
    options = options || {};
    var hash = !!options.hash, current = C.byId(hash ? location.hash.slice(1) : '').id;
    // The signed-in user's first usable token, held in memory only.
    var personal = null, personalFor = null;
    root.innerHTML =
      '<div class="flex flex-wrap items-center justify-between gap-x-6 gap-y-2">' +
        '<div class="flex flex-wrap gap-1" id="client-tabs" role="tablist"></div>' +
        '<div class="flex items-center gap-1.5 text-[13px] text-muted">' +
          '<label class="inline-flex cursor-pointer items-center gap-2 hover:text-ink"><input id="with-token" type="checkbox" class="h-3.5 w-3.5 accent-ink"><span>API Token</span></label>' +
          '<span class="group relative inline-flex">' +
            '<button type="button" id="token-help" aria-describedby="token-help-tip" class="grid h-4 w-4 place-items-center rounded-full border border-ink/25 text-[10px] leading-none hover:border-ink/50 hover:text-ink">?</button>' +
            '<span id="token-help-tip" role="tooltip" class="lift pointer-events-none absolute right-0 top-full z-30 mt-2 w-72 rounded-surface bg-card p-3 text-[12.5px] leading-relaxed text-ink opacity-0 transition-opacity group-hover:opacity-100 group-focus-within:opacity-100"></span>' +
          '</span>' +
        '</div>' +
      '</div>' +
      '<div class="field relative mt-2 p-5">' +
        '<pre class="overflow-x-auto pr-8 font-mono text-[12.5px] leading-[1.75]"><code id="snippet"></code></pre>' +
        '<button id="copy" class="absolute right-3 top-3 rounded-[6px] p-1.5 text-muted hover:bg-sand hover:text-ink"></button>' +
      '</div>' +
      '<p id="client-note" class="type-fig mt-2"></p>';
    var $ = function (s) { return root.querySelector(s); };

    function snippet(c) { return personal && C.withToken ? c.code.split('<service-token>').join(personal.key) : c.code; }
    function render() {
      $('#client-tabs').innerHTML = C.list.map(function (c) {
        return '<button role="tab" data-client="' + c.id + '" aria-selected="' + (c.id === current) + '" class="rounded-control px-2.5 py-1 text-[13px] ' + (c.id === current ? 'bg-ink/[0.06] text-ink' : 'text-muted hover:text-ink') + '">' + A.esc(c.label) + '</button>';
      }).join('');
      $('#client-tabs').querySelectorAll('[data-client]').forEach(function (b) {
        b.onclick = function () { current = b.dataset.client; if (hash) history.replaceState(null, '', '#' + current); render(); };
      });
      var c = C.byId(current), secured = C.withToken;
      $('#with-token').checked = secured;
      $('#token-help').ariaLabel = t('API Token 说明', 'About API Token');
      $('#token-help-tip').innerHTML = t(TIP[0], TIP[1]);
      $('#snippet').textContent = snippet(c);
      var tokenNote = !secured ? '' : personal ? t('已填入你的 Token「' + personal.name + '」', 'Filled in with your token “' + personal.name + '”') : t('把 <service-token> 换成你的 API Token', 'Replace <service-token> with your API Token');
      $('#client-note').textContent = [c.note ? t(c.note[0], c.note[1]) : '', tokenNote].filter(Boolean).join(' · ');
      $('#copy').ariaLabel = t('复制接入配置', 'Copy connection configuration');
    }
    async function loadPersonal() {
      var u = A.user(), owner = u ? u.id : null;
      if (owner === personalFor) return;
      personalFor = owner; personal = null;
      if (!owner) return render();
      try {
        var out = await API.request('/auth/api-keys?limit=100');
        if (personalFor !== owner) return;
        personal = (out.items || []).find(function (k) { return typeof k.key === 'string' && k.key.startsWith('sgk_') && !(k.expires_at && Date.parse(k.expires_at) <= Date.now()); }) || null;
      } catch (e) { personal = null; }
      render();
    }

    $('#with-token').onchange = function (e) { C.withToken = e.target.checked; render(); };
    $('#copy').innerHTML = A.ICON.copy;
    $('#copy').onclick = function () { A.copy(snippet(C.byId(current))); };
    if (hash) window.addEventListener('hashchange', function () { var id = location.hash.slice(1); if (C.list.some(function (c) { return c.id === id; })) { current = id; render(); } });
    document.addEventListener('langchange', render);
    document.addEventListener('app:auth', loadPersonal);
    window.addEventListener('pagehide', function () { personal = null; personalFor = null; });
    render();
    API.ready.then(function () { render(); loadPersonal(); });
    return {
      select: function (id) {
        if (!C.list.some(function (c) { return c.id === id; })) return;
        current = id; if (hash) history.replaceState(null, '', '#' + current); render();
      }
    };
  }

  window.ClientPanel = { mount: mount };
})();
