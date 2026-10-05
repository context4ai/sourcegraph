(function (root) {
  'use strict';
  const DEFAULT_PATH_GROUP = 'deffault';
  const active = j => j && (j.state === 'queued' || j.state === 'running');
  const orderedJobs = r => (r.jobs || []).slice().sort((a, b) => Date.parse(b.created_at) - Date.parse(a.created_at));
  const validTime = x => x && !String(x).startsWith('0001-') && Number.isFinite(Date.parse(x));
  function time(x) { return validTime(x) ? new Date(x).toLocaleString(root.App && root.App.lang() === 'en' ? 'en' : 'zh-CN') : '—'; }
  function bytes(n) { if (!Number.isFinite(n) || n < 0) return '—'; const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']; let i = 0; while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; } return n.toLocaleString(undefined, {maximumFractionDigits: 1}) + ' ' + units[i]; }
  function repositoryName(value) {
    const name=value.trim(),parts=name.split('/');
    if (name.length>512 || parts.length<2 || parts.some(p=>!/^[A-Za-z0-9_][A-Za-z0-9_.-]*$/.test(p)||p.endsWith('.git'))) throw new Error('名称需为 namespace/repository，不带 .git / Use namespace/repository without .git');
    return name;
  }
  function splitBranches(value) { return value.split(/[,\r\n]/).map(x => x.trim()).filter(Boolean); }
  function branches(value, defaultBranch = false) {
    const bs = Array.isArray(value) ? value.slice() : splitBranches(value);
    if (bs.length + Number(defaultBranch) < 1 || bs.length + Number(defaultBranch) > 8 || new Set(bs).size !== bs.length) throw new Error('1–8 个唯一分支 / 1–8 unique branches');
    if (bs.some(b => b.length>1024 || b==='HEAD' || !/^[A-Za-z0-9_][A-Za-z0-9_./-]*$/.test(b) || b.startsWith('refs/') || b === 'snapshot' || /[\s~^:?*\[\\\x00-\x1f\x7f]/.test(b) || b.includes('..') || b.includes('@{') || b.startsWith('-') || b.startsWith('/') || b.endsWith('/') || b.endsWith('.') || b.split('/').some(s => !s || s.startsWith('.') || s.endsWith('.lock')))) throw new Error('分支名称无效 / Invalid branch name');
    return bs;
  }
  function policy(input, kind, n, defaultBranch = false) {
    const bs = branches(input, defaultBranch), value = Number(n), max = kind === 'days' ? 365 : 4096;
    if (!['days', 'count'].includes(kind) || !Number.isInteger(value) || value < 1 || value > max) throw new Error('保留范围无效 / Invalid retention: 1–' + max);
    return {branches: bs, ...(defaultBranch ? {default_branch:true} : {}), days: kind === 'days' ? value : 0, count: kind === 'count' ? value : 0};
  }
  const SYNC_INTERVAL_MINUTES = Object.freeze([1,3,5,10,30,60,180,360,720,1440]);
  const DEFAULT_SYNC_INTERVAL_MINUTES = 5;
  function syncInterval(value) {
    const minutes = Number(value);
    if (!['number','string'].includes(typeof value) || !SYNC_INTERVAL_MINUTES.includes(minutes)) throw new Error('同步间隔无效 / Invalid sync interval');
    return minutes;
  }
  function syncIntervalLabel(minutes) {
    const value = minutes < 60 ? minutes : minutes / 60;
    return root.App.t(value + (minutes < 60 ? ' 分钟' : ' 小时'), value + (minutes < 60 ? (value === 1 ? ' minute' : ' minutes') : (value === 1 ? ' hour' : ' hours')));
  }
  function syncIntervalOptions(selected) {
    return SYNC_INTERVAL_MINUTES.map(minutes => '<option value="' + minutes + '"' + (minutes === Number(selected) ? ' selected' : '') + '>' + syncIntervalLabel(minutes) + '</option>').join('');
  }
  function directoryPath(value) {
    const path = typeof value === 'string' ? value.trim().replace(/\/$/, '') : '';
    if (!path || path.length > 1024 || path.startsWith('/') || /^[A-Za-z]:/.test(path) || /[\\*?\[\]\x00-\x1f\x7f]/.test(path) || path.split('/').some(x => !x || x === '.' || x === '..' || x.toLowerCase() === '.git')) throw new Error('请填写安全的仓库相对目录 / Use a safe repository-relative directory');
    return path;
  }
  function pathGroups(value) {
    if (!Array.isArray(value) || !value.length || value.length > 16) throw new Error('需要 1–16 个分组 / Use 1–16 path groups');
    const names = new Set(), all = [];
    const groups = value.map(group => {
      const name = typeof group.name === 'string' ? group.name.trim() : '';
      if (!/^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/.test(name) || names.has(name)) throw new Error('分组名称需唯一，只支持字母、数字、下划线和短横线 / Use unique alphanumeric group names');
      names.add(name);
      if (!Array.isArray(group.paths) || !group.paths.length) throw new Error('每个分组至少填写一个目录 / Each group needs a directory');
      const paths = group.paths.map(directoryPath);
      for (const path of paths) {
        if (all.some(x => x.path === path || (x.name !== name && (x.path.startsWith(path + '/') || path.startsWith(x.path + '/'))))) throw new Error('目录不能重复，不同分组不能包含父子目录 / Duplicate or overlapping paths across groups');
        all.push({name,path});
      }
      return {name,paths};
    });
    if (all.length > 64) throw new Error('最多 64 个监听目录 / At most 64 watched directories');
    return groups;
  }
  function groupPathRows(rows) {
    const groups = new Map();
    for (const row of rows) {
      const name = row.group.trim() || DEFAULT_PATH_GROUP;
      if (!groups.has(name)) groups.set(name, []);
      groups.get(name).push(row.path);
    }
    return pathGroups([...groups].map(([name, paths]) => ({name, paths})));
  }
  function indexModeLabel(mode) { return root.App.t(mode === 'monorepo' ? 'Monorepo 按需' : '全量索引', mode === 'monorepo' ? 'Monorepo on demand' : 'Full repository'); }
  function indexTiming(record) {
    const seconds = record && record.last_index_duration_seconds;
    if (!Number.isFinite(seconds) || seconds <= 0) return '';
    const value = seconds.toLocaleString(undefined, {maximumFractionDigits: 2});
    return root.App.t('上次索引耗时 ' + value + ' 秒，仅供参考', 'Previous indexing took ' + value + ' s, for reference only');
  }
  function jobReceipts(result) {
    const jobs = Array.isArray(result?.jobs) ? result.jobs : [result];
    if (!jobs.length || jobs.some(j => !j || typeof j.job_id !== 'string' || !j.job_id.trim() || !['queued','running','succeeded','failed','cancelled','canceled'].includes(j.state))) throw new Error('没有拿到有效的任务回执，请重试 / No valid job receipt; retry');
    return jobs;
  }
  function state(r) {
    if (r.deleted) return 'deleted'; if (!r.enabled) return 'disabled';
    if(r.storage_missing)return 'missing';
    if ((r.jobs || []).some(active)) return 'preparing';
    const last = root.App.Repo.currentJobs(r)[0]; if (last && last.state === 'failed') return 'failed';
    if ((r.published_index_count || 0) > 0 || (r.versions || []).some(v => typeof v.indexed === 'boolean' ? v.indexed : v.shards > 0)) return 'published';
    return 'unprepared';
  }
  function status(code) {
    const A = root.App, labels = {missing:['本地材料缺失，需重新同步','Local data missing; sync required'],deleted:['已标记删除','Marked for deletion'],disabled:['已停用','Disabled'],preparing:['准备中','Preparing'],failed:['失败','Failed'],published:['索引已发布','Index published'],unprepared:['尚未发布索引','No published index'],queued:['排队中','Queued'],running:['运行中','Running'],succeeded:['已完成','Succeeded'],cancelled:['已取消','Cancelled'],canceled:['已取消','Cancelled']};
    const label = labels[code] || [code,code], color = ['running','queued','preparing'].includes(code) ? 'bg-live dot-live' : code === 'failed' ? 'bg-danger' : 'bg-ink/50';
    return '<span class="inline-flex items-center gap-1.5 text-[13px]"><span class="dot ' + color + '"></span>' + A.esc(A.t(...label)) + '</span>';
  }
  function error(e) { return [e.code, e.message, e.requestId && 'request: ' + e.requestId].filter(Boolean).join(' · '); }
  function manage() {
    if (!root.App.user()) { root.App.requireLogin(location.pathname + location.search + location.hash); return false; }
    if (!root.App.canManage()) { root.App.toast(root.App.t('当前登录身份没有仓库管理权限', 'Your account does not have repository management permission'), 'bad'); return false; }
    return true;
  }
  // Retry a result-unknown request with the same key; successful explicit actions get a new one.
  function operationKeys() {
    const keys = new Map();
    return { get: body => { const k = JSON.stringify(body); if (!keys.has(k)) keys.set(k, root.API.idempotency()); return keys.get(k); }, done: body => keys.delete(JSON.stringify(body)) };
  }
  function aggregate(data, minutes) {
    const end = Math.floor(Date.parse(data.sampled_at) / 60000) * 60;
    const start = end - (minutes - 1) * 60;
    const buckets = (data.buckets || []).filter(b => b.minute_unix >= start && b.minute_unix <= end).sort((a,b) => a.minute_unix - b.minute_unix);
    let requests=0, errors=0, total=0, max=0;
    const bounds=data.latency_bounds_ms || [], histogram=Array(bounds.length+1).fill(0);
    const series = buckets.map(b => { let req=0, err=0, ms=0, peak=0; const ops={}; Object.entries(b.by_operation || {}).forEach(([op,m]) => { ops[op]=(ops[op]||0)+(m.requests||0); if (Array.isArray(m.latency_buckets) && m.latency_buckets.length === histogram.length) m.latency_buckets.forEach((n,i)=>{histogram[i]+=n;}); req += m.requests || 0; err += m.errors || 0; ms += m.total_ms || 0; peak = Math.max(peak,m.max_ms || 0); }); total+=ms; max=Math.max(max,peak); requests+=req; errors+=err; return {minute:b.minute_unix,requests:req,errors:err,average:req?ms/req:null,max:req?peak:null,ops}; });
    let p95=null, p95Overflow=false;
    const sampled=histogram.reduce((a,b)=>a+b,0);
    if (requests && bounds.length && sampled===requests) {
      let count=0;for(let i=0;i<histogram.length;i++){count+=histogram[i];if(count>=Math.ceil(sampled*.95)){p95=i<bounds.length?bounds[i]:bounds[bounds.length-1];p95Overflow=i>=bounds.length;break;}}
    }
    return {requests,errors,p95,p95Overflow,qps:requests/(minutes*60),average:requests ? total/requests : null,max:requests ? max:null,rate:requests ? errors/requests*100:null,series,start,end};
  }
  // No requests while hidden; one request at a time; back off on failures; stop after a bounded visit.
  function poll(load, interval=15000, max=80) {
    let timer, running=false, count=0, failures=0, stopped=false;
    async function refresh() {
      if (stopped || running || document.hidden || count >= max) return;
      clearTimeout(timer); running=true; count++;
      try { await load(); failures=0; } catch (_) { failures++; }
      finally { running=false; if (!stopped && !document.hidden && count < max) timer=setTimeout(refresh, Math.min(interval*Math.pow(2,failures),120000)); }
    }
    document.addEventListener('visibilitychange', () => { clearTimeout(timer); if (!document.hidden) refresh(); });
    root.addEventListener('pagehide',()=>{stopped=true;clearTimeout(timer);});
    return {refresh, restart:()=>{count=0;stopped=false;refresh();}};
  }
  root.Management = {active,orderedJobs,time,validTime,bytes,repositoryName,splitBranches,branches,policy,SYNC_INTERVAL_MINUTES,DEFAULT_SYNC_INTERVAL_MINUTES,DEFAULT_PATH_GROUP,syncInterval,syncIntervalLabel,syncIntervalOptions,directoryPath,pathGroups,groupPathRows,indexModeLabel,indexTiming,jobReceipts,state,status,error,manage,operationKeys,aggregate,poll};
  if (typeof module !== 'undefined') module.exports = root.Management;
})(typeof window === 'undefined' ? globalThis : window);
