// Local frontend server, optionally proxying the existing service. Never embeds a service token.
import http from 'node:http';
import { readFile } from 'node:fs/promises';
import { extname, join } from 'node:path';
const port = Number(process.env.WEB_PORT || 5179);
const backend = process.env.SOURCEGRAPH_DEV_BACKEND;
const root = 'internal/webui/dist';
http.createServer(async (req, res) => {
  const u = new URL(req.url, 'http://localhost');
  if(['/sourcegraph/api-tokens','/sourcegraph/api-tokens.html'].includes(u.pathname)){res.writeHead(308,{Location:'/sourcegraph/user-settings'+u.search});return res.end();}
  const sub = u.pathname.replace(/^\/sourcegraph/, '');
  if (/^\/(api|v1|auth|mcp|access-policy|site-settings|healthz|readyz)(\/|$)/.test(sub)) {
    if (!backend) { res.writeHead(503, { 'Content-Type': 'application/json' }); return res.end(JSON.stringify({ Error: 'Set SOURCEGRAPH_DEV_BACKEND to a local service', Meta: { Code: 'BACKEND_NOT_CONFIGURED' } })); }
    const dest = new URL(req.url, backend);
    const proxy = http.request(dest, { method: req.method, headers: { ...req.headers } }, upstream => { res.writeHead(upstream.statusCode, upstream.headers); upstream.pipe(res); });
    proxy.on('error', () => { res.writeHead(502); res.end(); }); req.pipe(proxy); return;
  }
  if ((req.method === 'GET' || req.method === 'HEAD') && /^\/(index|code|repos|repo|connect|operations|query-errors|login|users|api-tokens|user-settings|settings)\.html$/.test(sub)) {
    const page = sub.slice(1, -5);
    res.writeHead(308, { Location: '/sourcegraph/' + (page === 'index' ? '' : page) + u.search });
    return res.end();
  }
  let file = u.pathname === '/' ? 'home.html' : sub === '' || sub === '/' ? 'index.html' : sub.slice(1);
  if (/^repos\/.+/.test(file)) file = 'repo.html';
  if (!extname(file)) file += '.html';
  if (file.includes('..') || file.includes('\\') || !/^(assets\/[^/]+|[a-z-]+\.html|plugins\/llms\.txt|plugins\.md)$/.test(file)) { res.writeHead(404); return res.end(); }
  try { const data = await readFile(join(root, file)); res.writeHead(200, { 'Content-Type': ({ '.html':'text/html', '.js':'text/javascript', '.mjs':'text/javascript', '.css':'text/css', '.txt':'text/plain; charset=utf-8', '.md':'text/plain; charset=utf-8' })[extname(file)] || 'application/octet-stream' }); res.end(data); }
  catch { res.writeHead(404); res.end('Not found. Run bun run build:web first.'); }
}).listen(port, '127.0.0.1', () => console.log('Website: http://127.0.0.1:' + port + '/ and /sourcegraph/'));
