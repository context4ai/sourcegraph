import {spawn} from 'node:child_process';
import {resolve} from 'node:path';
const child=spawn(resolve('output/bin/repo-service'),{stdio:'inherit',env:{...process.env,SOURCEGRAPH_DATA_ROOT:resolve('.tmp/data'),SOURCEGRAPH_ORIGIN:process.env.SOURCEGRAPH_ORIGIN||'http://localhost:8080'}});
for(const signal of ['SIGINT','SIGTERM'])process.on(signal,()=>child.kill(signal));
child.on('exit',code=>process.exit(code??1));
