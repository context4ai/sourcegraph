import { readdirSync, readFileSync, writeFileSync, mkdirSync, rmSync, copyFileSync } from 'node:fs';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
const source = 'web', target = 'internal/webui/dist';
mkdirSync(target, { recursive: true });
for (const name of readdirSync(target)) if (name !== 'README.txt') rmSync(join(target, name), { recursive: true, force: true });
function copy(dir, out) {
  mkdirSync(out, { recursive: true });
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    if (e.name === 'dist' || ['data.js', 'search.js', 'tw-config.js'].includes(e.name)) continue;
    const from = join(dir, e.name), to = join(out, e.name);
    if (e.isDirectory()) copy(from, to);
    else copyFileSync(from, to);
  }
}
copy(source, target);
// Publish the canonical plugin contract; do not maintain a second ABI document.
copyFileSync('docs/ref/repository-plugins.md', join(target, 'plugins.md'));
const deployment = JSON.parse(readFileSync('config/deployment.json', 'utf8'));
writeFileSync(join(target, 'assets/deployment.js'), 'window.DEPLOYMENT=' + JSON.stringify(deployment) + ';');
execFileSync(process.execPath, ['node_modules/tailwindcss/lib/cli.js', '-c', 'web/tailwind.config.cjs', '-i', 'web/tailwind.css', '-o', join(target, 'assets/styles.css'), '--minify'], { stdio: 'inherit' });
const stamp = createHash('sha256');
for (const name of readdirSync(join(target, 'assets')).sort()) stamp.update(readFileSync(join(target, 'assets', name)));
const version = stamp.digest('hex').slice(0, 12);
for (const name of readdirSync(target).filter(n => n.endsWith('.html'))) {
  const path = join(target, name);
  let html = readFileSync(path, 'utf8');
  html = html.replace(/<script src="https:\/\/cdn\.tailwindcss\.com"><\/script>\s*/g, '')
    .replace(/<script src="assets\/(?:tw-config|data|search)\.js[^\"]*"><\/script>\s*/g, '')
    .replace('<head>', '<head>\n  <base href="/sourcegraph/">\n  <script src="assets/deployment.js"></script>\n  <script src="assets/theme.js"></script>\n  <script src="assets/api.js"></script>\n  <link rel="stylesheet" href="assets/styles.css">')
    .replace(/((?:src|href)="assets\/[^"?]+)(?:\?[^\"]*)?"/g, '$1?v=' + version + '"');
  if (/window\.DEMO|window\.Search/.test(html)) throw new Error('Mock data dependency remains in ' + name);
  writeFileSync(path, html);
}
rmSync(join(target, 'tailwind.config.cjs'), { force: true });
rmSync(join(target, 'tailwind.css'), { force: true });
console.log('Built production website -> ' + target + ' (' + version + ')');
