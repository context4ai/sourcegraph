// Same-origin API client. Credentials remain in HttpOnly cookies, never local storage.
(function () {
  'use strict';
  var session = null;
  var policy = { public_read: false, public_prepare: false, sso_enabled: false };
  var messages = {
    RECOVERY_EXHAUSTED: ["任务多次中断，自动恢复次数已用尽；可重新准备", "Repeated interruptions exhausted recovery; prepare again"],
    LEASE_EXPIRED: ["任务租约过期，原执行者未及时续租或保存完成状态", "Task lease expired before renewal or completion was saved"],
    PREPARATION_FAILED: ["准备失败，请重试；持续失败时联系维护者查看日志", "Preparation failed; retry or ask the maintainer to inspect logs"],
    PREPARATION_TIMEOUT: ["准备超时，可稍后重试", "Preparation timed out; retry later"],
    GIT_AUTH_FAILED: ["代码服务凭证认证失败，请更新令牌", "Code service authentication failed; update credentials"],
    GIT_ACCESS_DENIED: ["代码服务拒绝访问，请检查仓库权限", "Code service access denied; check repository permissions"],
    GIT_CREDENTIAL_UNAVAILABLE: ["没有可用的代码服务凭证，请检查令牌和有效期", "No usable code credentials; check tokens and expiry"],
    GIT_FETCH_FAILED: ["拉取代码失败，请检查网络、凭证和仓库权限", "Fetch failed; check network, credentials and repository access"],
    GIT_OPERATION_FAILED: ["Git 操作失败，请检查仓库访问及服务日志", "Git operation failed; check access and service logs"],
    GIT_OUTPUT_LIMIT: ["Git 输出超过处理上限，请缩小范围", "Git output exceeded the processing limit; narrow the scope"],
    PARTIAL_CLONE_UNSUPPORTED: ["远端不支持按需克隆，无法使用当前索引模式", "Remote does not support partial clone for this mode"],
    INSUFFICIENT_STORAGE: ["磁盘可用空间不足，请释放空间后重试", "Insufficient disk space; free space and retry"],
    CONTROL_STATE_UNAVAILABLE: ["任务状态存储暂时不可用，请稍后重试", "Task state storage is unavailable; retry later"],
    STALE_WORKER: ["任务执行权已过期或被接管", "Task ownership expired or was taken over"],
    POLICY_CHANGED: ["仓库配置已变更，请按新配置重新准备", "Repository policy changed; prepare with the new policy"],
    SHORT_SHA_UNSUPPORTED: ['请使用完整的 40 位 Commit SHA，或选择监听分支', 'Use a full 40-character commit SHA or a monitored branch'],
    BRANCH_NOT_OBSERVED: ['此监听分支尚未同步，请查看仓库准备进度', 'This monitored branch has not been synchronized'],
    BRANCH_NOT_MONITORED: ['此分支不在监听范围内，请选择已登记分支', 'This branch is not monitored; choose a registered branch'],
    INVALID_REVISION: ['版本格式无效，请使用完整 SHA 或监听分支', 'Invalid revision; use a full SHA or a monitored branch'],
    REVISION_NOT_ELIGIBLE: ["该提交不在当前保留范围内", "Commit is outside the retention policy"],
    REVISION_NOT_FOUND: ["未找到指定提交或分支", "Commit or branch was not found"],
    REPOSITORY_STORAGE_MISSING: ["代码存放位置已变化，需要重新同步", "Storage location changed; synchronize again"],
    JOB_HISTORY_CLEARED: ["任务记录已清除，新操作请使用新的幂等键", "Task history was cleared; use a new idempotency key"],
    CLEAR_JOBS_INCOMPLETE: ["部分记录可能已清除，请重试完成剩余清理", "Some records may have been cleared; retry to finish"],
    INDEX_BUILD_FAILED: ["索引构建失败，请重试或检查服务日志", "Index build failed; retry or inspect service logs"],

    AUTHENTICATION_REQUIRED: ['请先登录后重试', 'Sign in to continue'],
    ACCESS_DENIED: ['当前账号没有此操作的权限', 'Your account does not have permission'],
    SSO_DISABLED: ['本站尚未启用登录，请联系维护者配置 SSO', 'Sign-in is not enabled on this service'],
    CSRF_REQUIRED: ['会话需要刷新，请刷新页面后重试', 'Refresh the page before retrying'],
    REVISION_CONFLICT: ['配置已被更新，请刷新并重新预览', 'Configuration changed. Refresh and preview again'],
    DEFAULT_BRANCH_UNDETERMINED: ['无法确定主干，请选择分支或版本', 'Select an explicit branch or revision'],
    DEFAULT_BRANCH_NOT_OBSERVED: ['主干尚未同步，请查看仓库准备进度', 'The default branch has not been synchronized'],
    INDEX_NOT_READY: ['该提交的索引尚未就绪', 'The index for this commit is not ready'],
    REPOSITORY_DISABLED: ['仓库已停用或删除', 'Repository disabled or deleted'],
    RATE_LIMITED: ['请求较多，请稍后重试', 'Too many requests. Try again shortly']
  };
  function message(code, fallback) {
    var en = document.documentElement.lang === 'en';
    return messages[code] ? messages[code][en ? 1 : 0] : fallback || code;
  }
  async function request(path, options) {
    options = options || {};
    if (!path.startsWith('/') || path.startsWith('//')) throw new Error('Only same-origin API paths are allowed');
    var url = path.startsWith('/sourcegraph/') ? path : '/sourcegraph' + path;
    var headers = new Headers(options.headers || {});
    var method = options.method || 'GET';
    if (options.body !== undefined) headers.set('Content-Type', 'application/json');
    if (session && !['GET', 'HEAD'].includes(method.toUpperCase())) headers.set('X-CSRF-Token', session.csrf_token);
    var response = await fetch(url, { method: method, credentials: 'same-origin', cache: 'no-store', headers: headers,
      body: options.body === undefined ? undefined : JSON.stringify(options.body), signal: options.signal });
    var body;
    try { body = await response.json(); } catch (_) { throw Object.assign(new Error(message('INVALID_RESPONSE', 'Unexpected server response')), { status: response.status, code: 'INVALID_RESPONSE' }); }
    if (!response.ok) {
      var code = body.Meta && body.Meta.Code || body.error || 'HTTP_' + response.status;
      var error = new Error(message(code, body.Error || body.message || code));
      error.code = code; error.status = response.status; error.requestId = response.headers.get('X-Request-ID') || body.Meta && body.Meta.RequestID;
      error.detail = body.Error || ''; error.retryAfter = response.headers.get('Retry-After');
      throw error;
    }
    return body;
  }
  async function loadSession() {
    try { session = await request('/auth/me'); }
    catch (err) { session = null; if (![401, 404].includes(err.status)) API.authError = err; }
    API.session = session;
    document.dispatchEvent(new Event('app:auth'));
    return session;
  }
  async function catalog(signal) {
    var rows = [], after = '', seen = new Set();
    for (var page = 0; page < 100; page++) {
      var result = await request('/v1/repos' + (after ? '?after=' + encodeURIComponent(after) : ''), { signal: signal });
      if (!Array.isArray(result)) throw new Error('Invalid repository catalog');
      rows.push.apply(rows, result);
      if (result.length < 100) return rows;
      after = result[result.length - 1].repo_id;
      if (!after || seen.has(after)) throw new Error('Invalid repository cursor');
      seen.add(after);
    }
    throw new Error('Repository catalog exceeds the browser limit');
  }
  var API = window.API = { request: request, loadSession: loadSession, catalog: catalog, message: message, jobError: function(job) { var code=job.error||""; return message(code, document.documentElement.lang === "en" ? "Task did not complete; retry or inspect service logs" : "任务未完成，请重试或联系维护者查看日志") + (job.recovery_reason ? " · " + message(job.recovery_reason, job.recovery_reason) : "") + " · " + code; }, session: session, policy: policy,
    idempotency: function () { return crypto.randomUUID(); },
    repo: function (repo, signal) { return request('/v1/repo/availability?repo=' + encodeURIComponent(repo), { signal: signal }); } };
  API.ready = Promise.all([loadSession(), request('/access-policy').then(function (p) { API.policy = p; }).catch(function (e) { API.policyError = e; })]);
})();
