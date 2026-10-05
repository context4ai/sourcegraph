// Pure contract adapters shared by the production search and reader pages.
export const FULL_SHA = /^[0-9a-f]{40}$/;
export function escapeHTML(value) {
  return String(value ?? '').replace(/[&<>"']/g, c => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
}
export function validRepo(value) {
  return typeof value === 'string' && value.length > 0 && value.length <= 1024 && !/[\\\s\x00-\x1f]/.test(value) && !value.endsWith('.git') && value.split('/').length >= 2 && value.split('/').every(p => p && p !== '.' && p !== '..');
}
export function validPath(value, allowEmpty = true) {
  return typeof value === 'string' && ((allowEmpty && value === '') || (!/[\\\x00-\x1f]/.test(value) && !value.startsWith('/') && value.split('/').every(p => p && p !== '.' && p !== '..')));
}
export function parseLocation(search, hash = '') {
  const p = new URLSearchParams(search);
  for (const key of ['repo', 'commit', 'revision', 'pattern', 'fixed_strings', 'ignore_case', 'path', 'branch']) if (p.getAll(key).length > 1) throw new Error('Duplicate URL parameter: ' + key);
  if (p.has('commit') && p.has('revision')) throw new Error('Choose commit or revision, not both.');
  const repo = p.get('repo') || '', commit = p.get('commit') || '', revision = commit || p.get('revision') || '', path = p.get('path') || '';
  if (repo && !validRepo(repo)) throw new Error('Invalid repository.');
  if (commit && !FULL_SHA.test(commit)) throw new Error('A fixed commit must be a full 40-character lowercase SHA.');
  if (revision && (revision.length > 1024 || /[\x00-\x20\\]/.test(revision))) throw new Error('Invalid revision.');
  if (!validPath(path)) throw new Error('Invalid repository path.');
  return { repo, revision, path, search: {pattern: p.get('pattern') || '', fixedStrings: p.get('fixed_strings') === '1', ignoreCase: p.get('ignore_case') === '1', glob: p.getAll('glob').filter(Boolean)}, branch: p.get('branch') || '', range: parseRange(hash) };
}
export function parseRange(hash) {
  if (!hash) return null;
  const m = /^(?:#L|L)?([1-9]\d*)(?:-L?([1-9]\d*))?$/.exec(hash);
  if (!m) throw new Error('Use a positive line number or range, for example L20-L40.');
  const a = Number(m[1]), b = Number(m[2] || m[1]);
  if (!Number.isSafeInteger(a) || !Number.isSafeInteger(b) || a > b) throw new Error('Invalid line range.');
  return [a, b];
}
export function lineFragment(range) {
  return range ? '#L' + range[0] + (range[1] === range[0] ? '' : '-L' + range[1]) : '';
}
export function codeURL(repo, commit, path = '', range = null, branch = '') {
  const p = new URLSearchParams({repo, path});
  if (FULL_SHA.test(commit)) p.set('commit', commit); else if (commit) p.set('revision', commit);
  if (branch && !FULL_SHA.test(branch)) p.set('branch', branch);
  return '/sourcegraph/code?' + p + lineFragment(range);
}
// URL parameters use the API field names, so a link reads like the search request.
export function searchURL(repo, revision, search = {}, branch = '') {
  const p = new URLSearchParams({repo, pattern: search.pattern || ''});
  if (search.fixedStrings) p.set('fixed_strings', '1');
  if (search.ignoreCase) p.set('ignore_case', '1');
  for (const g of search.glob || []) p.append('glob', g);
  if (FULL_SHA.test(revision)) p.set('commit', revision); else if (revision) p.set('revision', revision);
  if (branch && !FULL_SHA.test(branch)) p.set('branch', branch);
  return '/sourcegraph/?' + p;
}
// The request body for /api/search; empty options are omitted.
export function searchBody(search, revision) {
  return {Pattern: search.pattern, ...(search.fixedStrings ? {FixedStrings: true} : {}), ...(search.ignoreCase ? {IgnoreCase: true} : {}), ...(search.glob?.length ? {Glob: search.glob} : {}), ...(revision ? {Revision: revision} : {})};
}
// Comma-separated globs as in VS Code's include/exclude fields; commas inside {a,b} belong to the glob.
export function splitGlobs(value) {
  const out=[];let depth=0,bracket=false,escape=false,current='';
  for(const c of value){
    if(escape){current+=c;escape=false;continue;}
    if(c==='\\'){current+=c;escape=true;continue;}
    if(c==='[')bracket=true;else if(c===']')bracket=false;
    if(!bracket){if(c==='{')depth++;else if(c==='}'&&depth)depth--;}
    if(c===','&&!depth&&!bracket){out.push(current);current='';}else current+=c;
  }
  out.push(current);return out.map(g=>g.trim()).filter(Boolean);
}
export function formatGlobs(globs) {
  return globs.map(g=>{
    let depth=0,bracket=false,escape=false,out='';
    for(const c of g){
      if(escape){out+=c;escape=false;continue;}
      if(c==='\\'){out+=c;escape=true;continue;}
      if(c==='[')bracket=true;else if(c===']')bracket=false;
      if(!bracket){if(c==='{')depth++;else if(c==='}'&&depth)depth--;}
      out+=(c===','&&!depth&&!bracket?'\\':'')+c;
    }
    return out;
  }).join(', ');
}
// Glob for exactly one repository file: anchored to the root, metacharacters escaped.
export function fileGlob(path) { return '/' + path.replace(/[\\*?[\]{}!]/g, '\\$&'); }
export function sourceURL(repo, commit, path, range) {
  return ((globalThis.DEPLOYMENT?.code_host||'https://example.org').replace(/\/$/,'')+'/') + repo.split('/').map(encodeURIComponent).join('/') + '/blob/' + encodeURIComponent(commit) + '/' + path.split('/').map(encodeURIComponent).join('/') + lineFragment(range);
}
export function decodeBytes(value) {
  if (typeof value !== 'string') throw new Error('Invalid encoded search line.');
  const raw = atob(value);
  return Uint8Array.from(raw, c => c.charCodeAt(0));
}
export function highlightBytes(encoded, fragments = []) {
  const bytes = decodeBytes(encoded), decoder = new TextDecoder('utf-8', {fatal: true});
  const ranges = fragments.map(f => [f.LineOffset, f.LineOffset + f.MatchLength]).filter(([a,b]) => Number.isInteger(a) && Number.isInteger(b) && a >= 0 && b > a && b <= bytes.length).sort((a,b) => a[0]-b[0]);
  let at = 0, out = '';
  for (let i = 0; i < ranges.length; i++) {
    let [a,b] = ranges[i];
    while (i + 1 < ranges.length && ranges[i + 1][0] <= b) b = Math.max(b, ranges[++i][1]);
    if (a < at) continue;
    out += escapeHTML(decoder.decode(bytes.slice(at,a))) + '<mark class="hit">' + escapeHTML(decoder.decode(bytes.slice(a,b))) + '</mark>';
    at = b;
  }
  return out + escapeHTML(decoder.decode(bytes.slice(at)));
}
export function normalizeSearch(data, repo, requestedRevision) {
  const meta = data?.Meta, result = data?.Result;
  if (!meta || !FULL_SHA.test(meta.Commit || '') || meta.Repository !== repo || !result || (result.Files != null && !Array.isArray(result.Files))) throw new Error('Search response has an invalid repository or commit identity.');
  if (FULL_SHA.test(requestedRevision || '') && requestedRevision !== meta.Commit) throw new Error('Search returned a different commit.');
  const files = (result.Files || []).map(file => {
    if (file.Repository !== repo || file.Version !== meta.Commit || !validPath(file.FileName, false)) throw new Error('A result does not match the requested snapshot.');
    const matches = Array.isArray(file.LineMatches) ? file.LineMatches : [];
    return { path: file.FileName, language: file.Language || '', pathMatches: matches.filter(l => l.FileName), lines: matches.filter(l => !l.FileName).sort((a,b) => a.LineNumber-b.LineNumber), count: matches.reduce((n,l) => n + (l.LineFragments?.length || 0), 0) };
  });
  return {meta, result, files, engineMS: Number.isFinite(result.Duration) ? result.Duration / 1e6 : null, displayedMatches: files.reduce((n,f) => n + f.count, 0)};
}
export function parseRead(data, repo, commit, path) {
  if (!data || data.Commit !== commit || data.Path !== path || typeof data.Content !== 'string') throw new Error('Read response does not match the requested snapshot.');
  const start = data.ReturnedStartLine, end = data.ReturnedEndLine;
  if (start == null && end == null && data.Content === '') return [];
  if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end < start) throw new Error('Invalid returned line range.');
  const raw = data.Content.match(/[^\n]*\n|[^\n]+$/g) || [];
  if (raw.length !== end-start+1) throw new Error('Returned text and line range disagree.');
  return raw.map((text,i) => ({number:start+i, raw:text, text:text.replace(/\r?\n$/, '')}));
}
export function selectedText(lines, range) {
  const selected = range ? lines.filter(l => l.number >= range[0] && l.number <= range[1]) : lines;
  if (!selected.length || (range && (selected[0].number !== range[0] || selected.at(-1).number !== range[1])) || selected.some((l,i) => i > 0 && l.number !== selected[i-1].number+1)) throw new Error('Read the complete selected range before copying.');
  return selected.map(l => l.raw).join('');
}
export function readWindow(range) {
  const start = Math.max(1, (range?.[0] || 1) - 10);
  return [start, Math.min(start + 199, Math.max(start + 99, range?.[1] || 100))];
}

// A monorepo may have a searchable group before all groups share an indexed SHA.
// Keep the logical repository selectable so users can narrow the directory scope.
export function selectableRepository(repo) {
  return !!repo.enabled && !repo.deleted && (repo.published_index_count !== 0 || (repo.index_mode === 'monorepo' && Array.isArray(repo.path_groups) && repo.path_groups.length > 0));
}

// Catalog observed_at is the last repository synchronization time.
export function defaultRepository(catalog, previous) {
 const available=catalog.filter(r=>r.enabled&&!r.deleted&&r.published_index_count!==0);
 if(available.some(r=>r.name===previous))return previous;
 const timestamp=r=>{const n=Date.parse(r.observed_at||'');return Number.isFinite(n)?n:0;};
 return [...available].sort((a,b)=>timestamp(b)-timestamp(a)||a.name.localeCompare(b.name))[0]?.name||'';
}

// Preserve the precedence of existing rules when editing the two UI fields.
export function reconcileGlobs(previous, includes, excludes) {
  previous=previous.map(g=>splitGlobs(formatGlobs([g]))[0]);
  const desired=[...includes,...excludes.map(g=>g.startsWith('!')?g:'!'+g)];
  const remaining=desired.slice(), kept=[];
  for(const g of previous){const i=remaining.indexOf(g);if(i>=0){kept.push(g);remaining.splice(i,1);}}
  return [...kept,...remaining];
}
