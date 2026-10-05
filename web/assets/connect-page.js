import {escapeHTML as esc, searchURL} from './search-model.mjs';
const A = window.App, API = window.API, t = A.t, $ = s => document.querySelector(s);
let syntax = 'find';
if (location.hash === '#syntax') setTimeout(() => document.getElementById('syntax').scrollIntoView(), 0);

// Each example is the search tool's arguments and the equivalent rg command, with fixed sample results.
const SYNTAX = [
  {id:'find', zh:'找代码', en:'Find code', ex:[
    [{pattern:'WithTimeout'},"rg 'WithTimeout'",'一个标识符，只匹配文件内容','One identifier; only file contents are matched',
      [["internal/service/service.go",[[19,"\tctx, cancel := context.[[WithTimeout]](ctx, s.limits.SearchTimeout)"]]],["internal/indexer/build.go",[[5,"\tctx, cancel := context.[[WithTimeout]](ctx, 10*time.Minute)"]]]]],
    [{pattern:'func (s *Service)',fixed_strings:true},"rg -F 'func (s *Service)'",'按原文找代码片段，括号、星号不用转义','Literal code; no need to escape parentheses or *',
      [["internal/service/service.go",[[10,"[[func (s *Service)]] Search(ctx context.Context, repo, revision, q string) (*SearchResponse, error) {"],[29,"[[func (s *Service)]] resolveServable(ctx context.Context, repo, revision string) (string, error) {"]]],["internal/service/management.go",[[4,"[[func (s *Service)]] Prepare(ctx context.Context, repo string, req PrepareRequest, key string) (*Job, error) {"],[22,"[[func (s *Service)]] Purge(ctx context.Context, repo string, ifMatch string) error {"]]]]],
    [{pattern:'defer cancel\\(\\)'},"rg 'defer cancel\\(\\)'",'正则里的括号要转义，空格就是空格','Escape parentheses in a regex; a space is just a space',
      [["internal/service/service.go",[[20,"\t[[defer cancel()]]"]]],["internal/indexer/build.go",[[6,"\t[[defer cancel()]]"]]]]]]},
  {id:'regex', zh:'正则与大小写', en:'Regex and case', ex:[
    [{pattern:'Prepare|Resolve'},"rg 'Prepare|Resolve'",'任一出现的行','Lines containing either one',
      [["internal/service/management.go",[[3,"// [[Prepare]] accepts a durable sync/index intent. It returns 202 semantics."],[4,"func (s *Service) [[Prepare]](ctx context.Context, repo string, req [[Prepare]]Request, key string) (*Job, error) {"]]],["internal/service/service.go",[[34,"\treturn rec.[[Resolve]](revision, time.Now())"]]]]],
    [{pattern:'prepare',ignore_case:true},"rg -i 'prepare'",'默认区分大小写；ignore_case 等同 -i','Case-sensitive by default; ignore_case is -i',
      [["internal/service/management.go",[[3,"// [[Prepare]] accepts a durable sync/index intent. It returns 202 semantics."],[4,"func (s *Service) [[Prepare]](ctx context.Context, repo string, req [[Prepare]]Request, key string) (*Job, error) {"]]]]],
    [{pattern:'^func \\(s \\*Service\\) P\\w+'},"rg '^func \\(s \\*Service\\) P\\w+'",'^ 和 $ 匹配行首行尾，不会跨行','^ and $ anchor lines; matches never span lines',
      [["internal/service/management.go",[[4,"[[func (s *Service) Prepare]](ctx context.Context, repo string, req PrepareRequest, key string) (*Job, error) {"],[22,"[[func (s *Service) Purge]](ctx context.Context, repo string, ifMatch string) error {"]]]]]]},
  {id:'narrow', zh:'缩小范围', en:'Narrow down', ex:[
    [{pattern:'errors\\.Is',glob:['*.go']},"rg -g '*.go' 'errors\\.Is'",'只搜 Go 文件','Go files only',
      [["internal/service/service.go",[[22,"\tif [[errors.Is]](err, context.DeadlineExceeded) {"]]]]],
    [{pattern:'return nil, err',fixed_strings:true,paths:['internal/service']},"rg -F 'return nil, err' internal/service",'只搜某个目录','One directory only',
      [["internal/service/management.go",[[6,"\t\t[[return nil, err]]"]]],["internal/service/service.go",[[13,"\t\t[[return nil, err]]"]]]]],
    [{pattern:'WithTimeout',glob:['!internal/indexer/**']},"rg -g '!internal/indexer/**' 'WithTimeout'",'排除某个目录','Exclude a directory',
      [["internal/service/service.go",[[19,"\tctx, cancel := context.[[WithTimeout]](ctx, s.limits.SearchTimeout)"]]]]]]}
];
const MORE = [
  ['read_many','同一版本批量读取多个文件行段','Read several file ranges at one commit'],
  ['list','浏览目录','Browse a directory'],
  ['diff','对比两个版本','Compare two versions'],
  ['resolve','多个查询前先固定同一个版本','Pin one version before several queries'],
  ['repositories','列出可以搜的仓库','List searchable repositories'],
  ['availability','查看哪些版本可以搜','See which versions are searchable'],
  ['prepare','准备还不能搜的历史版本','Prepare an older version for search']
];

function lineRow(n, html, width = 'w-9') {
  return '<div class="flex"><span class="' + width + ' shrink-0 select-none pr-3 text-right text-muted/60">' + n + '</span><span class="truncate whitespace-pre">' + html + '</span></div>';
}
// Fixed sample text marks matches with [[ ]].
const marked = text => esc(text).replace(/\[\[(.*?)\]\]/g, '<mark class="hit">$1</mark>');


// Tool walkthrough: a fixed illustration of search, then read at the returned commit. It does not call the API.
const FLOW_REPO = window.DEPLOYMENT.example_repo, FLOW_SHA = '97d60bd23c37', FLOW_FILE = 'internal/service/service.go';
const FLOW_HITS = [
  [FLOW_FILE, 19, 'ctx, cancel := context.WithTimeout(ctx, s.limits.SearchTimeout)'],
  ['internal/indexer/build.go', 5, 'ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)']
];
const FLOW_READ = [
  [17, '\t\treturn nil, ErrIndexNotReady'],
  [18, '\t}'],
  [19, '\tctx, cancel := context.WithTimeout(ctx, s.limits.SearchTimeout)'],
  [20, '\tdefer cancel()'],
  [21, '\tres, err := s.engine.Search(ctx, gen, q)']
];
const hit = text => marked(text.replaceAll('WithTimeout', '[[WithTimeout]]'));
const STEPS = [
  {tool:'search', zh:'搜索代码，返回命中的片段和所在提交', en:'Search code; returns matching snippets and the commit',
    call:'search(repo: "' + FLOW_REPO + '", pattern: "WithTimeout")',
    out:FLOW_HITS.map(h => '<div class="truncate"><span class="text-muted">' + esc(h[0]) + ':' + h[1] + '</span><span class="pl-3">' + hit(h[2]) + '</span></div>').join('') +
      '<div class="mt-1 text-muted">commit <span class="text-ink">' + FLOW_SHA + '</span>…</div>'},
  {tool:'read', zh:'用这个提交读取上下文', en:'Read the context at that commit',
    call:'read(repo: "' + FLOW_REPO + '", revision: "' + FLOW_SHA + '…", path: "' + FLOW_FILE + '", start_line: 17, end_line: 21)',
    out:FLOW_READ.map(l => lineRow(l[0], hit(l[1]))).join('')}
];
let flowRun = 0, flowVisible = false;
const reduced = window.matchMedia('(prefers-reduced-motion: reduce)').matches;
function wait(ms, id) { return new Promise(done => setTimeout(() => done(id === flowRun), ms)); }
function fmtCall(text) {
  const i = text.indexOf('(');
  return i < 0 ? '<span class="font-medium">' + esc(text) + '</span>' : '<span class="font-medium">' + esc(text.slice(0, i)) + '</span><span class="text-muted">' + esc(text.slice(i)) + '</span>';
}
function markStep(active, running) {
  $('#flow-steps').querySelectorAll('[data-step]').forEach((el, i) => {
    const on = i === active, done = i < active || (on && !running);
    el.className = 'flex w-full items-baseline gap-4 rounded-control px-3 py-2.5 text-left transition-colors ' + (on ? 'bg-ink/[0.04]' : 'hover:bg-ink/[0.03]');
    const state = el.querySelector('[data-state]');
    state.innerHTML = '<span class="dot' + (on && running ? ' dot-live' : '') + '"></span>';
    state.className = 'w-3 shrink-0 text-center ' + (on || done ? 'text-ink' : 'text-muted');
    el.querySelector('[data-text]').className = on || done ? 'text-ink' : 'text-muted';
  });
}
function stepBlock(i) {
  const el = document.createElement('div');
  el.className = i ? 'mt-5' : '';
  el.innerHTML = '<div class="flex gap-2"><span class="select-none text-muted">›</span><span data-call class="min-w-0 whitespace-pre-wrap break-words"></span></div><div data-out class="mt-1 pl-4"></div>';
  $('#flow-trace').appendChild(el);
  return el;
}
function showStep(i) {
  const el = stepBlock(i);
  el.querySelector('[data-call]').innerHTML = fmtCall(STEPS[i].call);
  el.querySelector('[data-out]').innerHTML = STEPS[i].out;
}
async function playFlow(from = 0) {
  const id = ++flowRun;
  $('#flow-trace').innerHTML = '';
  $('#flow-replay').classList.add('invisible');
  for (let k = 0; k < from; k++) showStep(k);
  if (reduced) {
    for (let k = from; k < STEPS.length; k++) showStep(k);
    markStep(STEPS.length - 1, false);
    return;
  }
  for (let i = from; i < STEPS.length; i++) {
    const s = STEPS[i], el = stepBlock(i), call = el.querySelector('[data-call]');
    markStep(i, true);
    for (let c = 1; c <= s.call.length; c += 2) {
      call.innerHTML = fmtCall(s.call.slice(0, c)) + '<span class="caret"></span>';
      if (!await wait(16, id)) return;
    }
    call.innerHTML = fmtCall(s.call);
    $('#flow-progress').classList.add('on');
    if (!await wait(600, id)) return;
    $('#flow-progress').classList.remove('on');
    el.querySelector('[data-out]').innerHTML = '<div class="fade-in">' + s.out + '</div>';
    markStep(i, false);
    if (!await wait(i === STEPS.length - 1 ? 0 : 1100, id)) return;
  }
  $('#flow-replay').classList.remove('invisible');
  if (await wait(6000, id) && flowVisible) playFlow();
}
function renderTools() {
  $('#flow-steps').innerHTML = STEPS.map((s, i) => '<li><button data-step="' + i + '"><span data-state></span><span class="w-14 shrink-0 font-mono text-[13.5px]">' + s.tool + '</span><span data-text>' + t(s.zh, s.en) + '</span></button></li>').join('');
  $('#flow-steps').querySelectorAll('[data-step]').forEach(b => b.onclick = () => playFlow(+b.dataset.step));
  markStep(-1, false);
  $('#flow-more').innerHTML = MORE.map(x => '<div class="flex items-baseline gap-4 py-1"><dt class="w-28 shrink-0 font-mono text-[13px]">' + x[0] + '</dt><dd class="text-muted">' + t(x[1], x[2]) + '</dd></div>').join('');
  $('#flow-replay').textContent = t('重播', 'Replay');
  if (flowVisible || reduced) playFlow();
}

// Syntax examples with fixed sample results. The home search box has no paths field; a directory becomes a glob.
const tryURL = a => searchURL(window.DEPLOYMENT.example_repo, '', {pattern: a.pattern, fixedStrings: a.fixed_strings, ignoreCase: a.ignore_case, glob: [...(a.glob || []), ...(a.paths || []).map(p => p + '/**')]});
function renderSyntax() {
  const g = SYNTAX.find(x => x.id === syntax);
  $('#syntax-tabs').innerHTML = SYNTAX.map(x => '<button role="tab" data-s="' + x.id + '" aria-selected="' + (x.id === syntax) + '" class="rounded-control px-2.5 py-1 text-[13px] ' + (x.id === syntax ? 'bg-ink/[0.06] text-ink' : 'text-muted hover:text-ink') + '">' + t(x.zh, x.en) + '</button>').join('');
  $('#syntax-tabs').querySelectorAll('[data-s]').forEach(b => b.onclick = () => { syntax = b.dataset.s; renderSyntax(); });
  $('#syntax-cases').innerHTML = g.ex.map(x => {
    const body = x[4].map(f => '<div class="mt-3 first:mt-0"><div class="text-muted">' + esc(f[0]) + '</div>' + f[1].map(l => lineRow(l[0], marked(l[1]), 'w-10')).join('') + '</div>').join('');
    return '<div class="fade-in"><div class="flex flex-wrap items-baseline gap-x-4 gap-y-1"><code class="max-w-full break-all rounded-[6px] bg-ink/[0.05] px-2 py-0.5 font-mono text-[13.5px]">' + esc(JSON.stringify(x[0])) + '</code><span class="text-[14px] text-muted">' + t(x[2], x[3]) + '</span>' +
      '<a href="' + esc(tryURL(x[0])) + '" class="ml-auto text-[13px] text-muted hover:text-ink">' + t('试一试', 'Try it') + '</a></div>' +
      '<p class="mt-1.5 font-mono text-[12.5px] text-muted">' + esc(x[1]) + '</p>' +
      '<div class="field mt-2.5 overflow-x-auto p-4 font-mono text-[12.5px] leading-[1.75]">' + body + '</div></div>';
  }).join('');
}

$('#flow-replay').onclick = () => playFlow();
document.addEventListener('langchange', () => { renderTools(); renderSyntax(); });

window.ClientPanel.mount($('#client-panel'), {hash: true});
renderTools(); renderSyntax();
new IntersectionObserver(es => {
  const v = es[0].isIntersecting;
  if (v && !flowVisible) { flowVisible = true; playFlow(); }
  else if (!v && flowVisible) { flowVisible = false; flowRun++; $('#flow-progress').classList.remove('on'); }
}, {threshold: 0.35}).observe(document.getElementById('tools'));

// Illustrative response toggle; no repository requests or settings are changed.
let pluginDemoMode = 'auto';
function renderPluginDemo() {
  const enabled = pluginDemoMode === 'auto';
  $('#plugin-demo-modes').querySelectorAll('[data-plugin-mode]').forEach(button => {
    const active = button.dataset.pluginMode === pluginDemoMode;
    button.setAttribute('aria-pressed', String(active));
    button.className = 'rounded-[6px] px-3 py-1.5 text-[12px] ' + (active ? 'bg-paper text-ink' : 'text-muted hover:text-ink');
  });
  $('#plugin-demo-label').textContent = enabled ? 'READ / READ_MANY + WASM PLUGIN' : 'READ / READ_MANY';
  $('#plugin-demo-mode').textContent = 'plugins=' + pluginDemoMode;
  $('#plugin-demo-extra').classList.toggle('hidden', !enabled);
  $('#plugin-demo-plain').classList.toggle('hidden', enabled);
  $('#plugin-demo-caption').textContent = enabled ? t('一次读取 · 原文 + 登记证据', 'One read · content + recorded evidence') : t('一次读取 · 仅原文', 'One read · original content');
}
$('#plugin-demo-modes').querySelectorAll('[data-plugin-mode]').forEach(button => {
  button.onclick = () => { pluginDemoMode = button.dataset.pluginMode; renderPluginDemo(); };
});
document.addEventListener('langchange', renderPluginDemo);
renderPluginDemo();
