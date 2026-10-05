import { readdirSync, readFileSync } from 'node:fs';
import { join } from 'node:path';
import { execFileSync } from 'node:child_process';
function check(dir) {
  for (const e of readdirSync(dir,{withFileTypes:true})) {
    const path=join(dir,e.name);
    if(e.isDirectory())check(path);
    else if(/\.(?:js|mjs|cjs)$/.test(path))execFileSync(process.execPath,['--check',path],{stdio:'inherit'});
    else if(path.endsWith('.html')&&/window\.DEMO|cdn\.tailwindcss\.com/.test(readFileSync(path,'utf8')))throw new Error('Demo runtime in '+path);
  }
}
check('web');
console.log('Website scripts and production-only dependencies checked.');
