package sso

import "net/http"

// A small same-origin session page for integration checks, not the product UI.
func (h *Handler) page(w http.ResponseWriter, r *http.Request) {
	nonce := random()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; connect-src 'self'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	_, _ = w.Write([]byte(`<!doctype html><html lang="zh"><meta charset="utf-8"><title>Sourcegraph 登录状态</title><h1>Sourcegraph 登录状态</h1><p id="status">正在读取会话…</p><pre id="user"></pre><a href="/sourcegraph/auth/login">使用 SSO 登录</a> <button id="logout" disabled>退出登录</button><script nonce="` + nonce + `">
let csrf='';const status=document.getElementById('status'),user=document.getElementById('user'),button=document.getElementById('logout');
async function refresh(){try{const r=await fetch('/sourcegraph/auth/me',{cache:'no-store'});if(r.status===401){status.textContent='未登录';user.textContent='';button.disabled=true;return}if(!r.ok)throw Error();const d=await r.json();csrf=d.csrf_token;status.textContent='已登录';user.textContent=JSON.stringify({user:d.user,permissions:d.permissions,expires_at:d.expires_at},null,2);button.disabled=false}catch{status.textContent='会话服务暂不可用'}}
button.onclick=async()=>{button.disabled=true;try{const r=await fetch('/sourcegraph/auth/logout',{method:'POST',headers:{'X-CSRF-Token':csrf}});if(!r.ok)throw Error();csrf='';await refresh()}catch{status.textContent='退出失败，请重试';button.disabled=false}};refresh();
</script></html>`))
}
