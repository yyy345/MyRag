// Dedicated synthetic database, queues and SDK MockTransport. No dotenv/provider calls.
const { spawn } = require('node:child_process');
const { join } = require('node:path');
const { mkdirSync, existsSync } = require('node:fs');
const { randomUUID } = require('node:crypto');
const { createInterface } = require('node:readline');
const { PrismaClient } = require('@prisma/client');
const amqp = require('amqplib');
const root = join(__dirname, '..');
const frontendIndex = process.argv.indexOf('--frontend');
const frontend = frontendIndex < 0 ? 'react' : process.argv[frontendIndex + 1];
if (!['react', 'vue'].includes(frontend)) throw new Error('Choose --frontend react or vue');
const browserArguments = process.argv.slice(2).filter((_, index) => frontendIndex < 0 || (index + 2 !== frontendIndex && index + 2 !== frontendIndex + 1));
const endpoint = 'postgresql://postgres:migration-test-only@127.0.0.1:55432/';
const name = 'migration_c26_e2e_' + randomUUID().replaceAll('-', '');
const database = endpoint + name;
const broker = 'amqp://guest:guest@127.0.0.1:55673/';
const admin = new PrismaClient({ datasources: { db: { url: endpoint + 'postgres' } } });
const children = [];
let fixture;
const directory = join(root, '.cache/go-e2e');
mkdirSync(directory, { recursive: true });
const env = { ...process.env, DATABASE_URL: database, REDIS_URL: 'redis://127.0.0.1:56380/0', RABBITMQ_URL: broker,
  JWT_SECRET: 'go-local-migration-test-secret-at-least-32-characters', AI_SERVICE_TOKEN: 'parse-local-test-abcdefghijklmnopqrstuvwxyz0123456789',
  GO_HTTP_ADDR: '127.0.0.1:3180', AI_PROVIDER_ENABLED: 'false', AUTO_EXTRACT_ENABLED: 'false', MIGRATION_EXTERNAL_AI: '0' };
for (const key of Object.keys(env)) if (/^(OSS_|BAILIAN_|DEEPSEEK_)/.test(key)) delete env[key];
for (const [key,path] of [['GOMODCACHE','go-mod-validation'],['GOCACHE','go-build-validation']]) {
  if (existsSync(join(root,'.cache',path))) env[key]=join(root,'.cache',path);
}
const go = process.env.GO_BINARY || (process.platform === 'win32' && existsSync('D:/Go/bin/go.exe') ? 'D:/Go/bin/go.exe' : 'go');
const python = process.env.PYTHON_BINARY || join(root,'ai-service/.venv',process.platform==='win32'?'Scripts/python.exe':'bin/python');
function start(file,args,cwd,stdio='inherit') {
  const child=spawn(file,args,{cwd,env,stdio,windowsHide:true});children.push(child);return child;
}
function completion(child) { return new Promise((resolve,reject)=>{child.once('error',reject);child.once('exit',(code)=>code===0?resolve():reject(new Error('Local subprocess failed with exit code '+code)));}); }
async function waitLive(url) {
  for(let n=0;n<100;n++){try{const res=await fetch(url,{signal:AbortSignal.timeout(500)});if(res.ok)return;}catch{}await new Promise(r=>setTimeout(r,100));}
  throw new Error('Local fixture did not become live');
}
async function main() {
  await admin.$executeRawUnsafe('CREATE DATABASE "'+name+'"');
  try {
    for(const command of ['migrate','api','outbox','results']) {
      await completion(start(go,['build','-o',join(directory,command+(process.platform==='win32'?'.exe':'')),'./cmd/'+command],join(root,'backend')));
    }
    await completion(start(join(directory,'migrate'+(process.platform==='win32'?'.exe':'')),[],join(root,'backend')));
    const helper=start(python,['-u','tests/serve_index_fixtures.py',database],join(root,'ai-service'),['ignore','pipe','inherit']);
    const lines=createInterface({input:helper.stdout});
    fixture=await Promise.race([new Promise((resolve,reject)=>{lines.once('line',line=>{try{resolve(JSON.parse(line));}catch{reject(new Error('Invalid fixture metadata'));}});helper.once('exit',()=>reject(new Error('Python fixture exited')));}),new Promise((_,reject)=>{const timer=setTimeout(()=>reject(new Error('Fixture startup timed out')),15000);timer.unref();})]);
    lines.close();await waitLive(fixture.url+'/health/live');
    Object.assign(env,{AI_BASE_URL:fixture.url,INDEX_TASK_QUEUE:fixture.task_queue,INDEX_RESULT_QUEUE:fixture.result_queue,MIGRATION_TEST_DATABASE_URL:database,MIGRATION_BACKEND:'go'});
    const services={python:helper.pid};
    for(const command of ['api','outbox','results']) services[command]=start(join(directory,command+(process.platform==='win32'?'.exe':'')),[],join(root,'backend')).pid;
    await waitLive('http://127.0.0.1:3180/health/live');
    if(process.argv.includes('--benchmark')) {
      services.express=start(process.execPath,['--import','tsx',join(root,'scripts/legacy-test-server.cjs')],join(root,'web'),'ignore').pid;
      await waitLive('http://127.0.0.1:3100/api/auth/captcha');
      await require('./measure-migration.cjs')(database,root,services);
    } else {
      await completion(start(process.execPath,[join(root,'node_modules/@playwright/test/cli.js'),'test','--config','playwright.go.config.ts',...browserArguments],join(root,frontend === 'react' ? 'client' : 'web')));
    }
  } finally {
    for(const child of children.reverse()){if(child.exitCode===null){const done=new Promise(r=>child.once('exit',r));child.kill();await Promise.race([done,new Promise(r=>setTimeout(r,2000))]);}}
    if(fixture){const connection=await amqp.connect(broker);try{const channel=await connection.createChannel();for(const q of [fixture.task_queue,fixture.task_queue+'.retry',fixture.task_queue+'.dead',fixture.result_queue,fixture.result_queue+'.dead']) await channel.deleteQueue(q);}finally{await connection.close();}}
    await admin.$executeRawUnsafe('DROP DATABASE IF EXISTS "'+name+'" WITH (FORCE)');
  }
}
main().catch(err=>{console.error(err.message);process.exitCode=1;}).finally(()=>admin.$disconnect());
