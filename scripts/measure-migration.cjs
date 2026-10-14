const { PrismaClient } = require('@prisma/client');
const Redis = require('ioredis');
const jwt = require('jsonwebtoken');
const { randomUUID } = require('node:crypto');
const { performance } = require('node:perf_hooks');
const { writeFileSync } = require('node:fs');
const { join } = require('node:path');
const { execFile } = require('node:child_process');
const { promisify } = require('node:util');
const execute=promisify(execFile);

module.exports = async function measure(database, root,services={}) {
  const target = new URL(database);
  if (target.hostname !== '127.0.0.1' || target.port !== '55432' || !target.pathname.startsWith('/migration_c26_e2e_')) throw new Error('Performance fixtures require the dedicated test database');
  const db = new PrismaClient({ datasources: { db: { url: database } } });
  const redis = new Redis('redis://127.0.0.1:56380/0', { maxRetriesPerRequest: 1 });
  const username = 'benchmark_' + randomUUID().slice(0, 8);
  let user;
  try {
    user = await db.user.create({ data: { username, email: username + '@example.test', passwordHash: 'unused-local-fixture' } });
    const kb = await db.knowledgeBase.create({ data: { userId: user.id, name: 'performance fixture' } });
    await db.knowledgeEntry.createMany({ data: Array.from({ length: 100 }, (_, n) => ({ kbId: kb.id, title: 'synthetic-' + n, content: 'synthetic knowledge ' + n, processingStatus: 'COMPLETED' })) });
    const targets = [{ name: 'Go', url: 'http://127.0.0.1:3180', secret: 'go-local-migration-test-secret-at-least-32-characters' }, { name: 'Express', url: 'http://127.0.0.1:3100', secret: 'local-migration-test-only' }];
    const results = [];
    for (const target of targets) {
      const headers = { Authorization: 'Bearer ' + jwt.sign({ id: user.id, username }, target.secret, { expiresIn: '1h' }) };
      const path = target.url + '/api/knowledge-bases/' + kb.id + '/entries?current=1&size=10';
      const request = async () => { const start = performance.now(); const response = await fetch(path, { headers, signal: AbortSignal.timeout(5000) }); const value = await response.json(); if (!response.ok || value.code !== 0 || value.data.total !== 100) throw new Error('Benchmark contract failed'); return performance.now() - start; };
      for (let n = 0; n < 10; n++) await request();
      const samples = [], start = performance.now();
      await Promise.all(Array.from({ length: 8 }, async () => { for (let n = 0; n < 25; n++) samples.push(await request()); }));
      const elapsed = performance.now() - start;
      const cold = [];
      for (let n = 0; n < 30; n++) { for (const prefix of ['go:entry:', 'entry:']) { const keys = await redis.keys(prefix + kb.id + ':*'); if (keys.length) await redis.del(...keys); } cold.push(await request()); }
      const summary = values => { const sorted = [...values].sort((a, b) => a - b); return { p50_ms: +sorted[Math.ceil(sorted.length * .5) - 1].toFixed(2), p95_ms: +sorted[Math.ceil(sorted.length * .95) - 1].toFixed(2) }; };
      results.push({ service: target.name, hot: summary(samples), cold: summary(cold), hot_requests_per_second: +(200 * 1000 / elapsed).toFixed(1) });
    }
    const go = targets[0]; const headers = { Authorization: 'Bearer ' + jwt.sign({ id: user.id, username }, go.secret, { expiresIn: '1h' }) };
    const form = new FormData(); form.append('file', new Blob(['local indexing performance fixture']), 'timing.txt');
    let start = performance.now(); const uploaded = await (await fetch(go.url + '/api/knowledge-bases/' + kb.id + '/entries/upload', { method: 'POST', headers, body: form })).json();
    if (uploaded.code !== 0) throw new Error('Indexing benchmark upload failed');
    for (let n = 0; ; n++) { const entry = await db.knowledgeEntry.findUniqueOrThrow({ where: { id: uploaded.data.entry_id } }); if (entry.processingStatus === 'COMPLETED') break; if (entry.processingStatus === 'FAILED' || n > 300) throw new Error('Indexing benchmark did not complete'); await new Promise(r => setTimeout(r, 20)); }
    const indexing_ms = +(performance.now() - start).toFixed(2);
    const session = (await (await fetch(go.url + '/api/knowledge-bases/' + kb.id + '/chat/sessions', { method: 'POST', headers })).json()).data;
    start = performance.now(); const response = await fetch(go.url + '/api/knowledge-bases/' + kb.id + '/chat/sessions/' + session.id + '/messages', { method: 'POST', headers: { ...headers, 'Content-Type': 'application/json' }, body: JSON.stringify({ content: 'knowledge' }), signal: AbortSignal.timeout(10000) });
    const reader = response.body.getReader(), decoder = new TextDecoder(); let first = null, stream = '';
    while (true) { const { value, done } = await reader.read(); if (done) break; if (first === null) first = performance.now() - start; stream += decoder.decode(value, { stream: true }); }
    if (!stream.includes('completion_tokens') || !stream.includes('🙂回答')) throw new Error('SSE performance fixture failed');
    const stream_total_ms=+(performance.now()-start).toFixed(2);
    const rss_mib={};
    if(process.platform==='win32')for(const [name,pid] of Object.entries(services)){
      if(!Number.isInteger(pid)||pid<1)throw new Error('Invalid local service PID');
      // Windows venv's python.exe can be a launcher. Include all descendants.
      const code=`$rows=Get-CimInstance Win32_Process; $ids=New-Object 'System.Collections.Generic.List[int]'; $ids.Add(${pid}); for($n=0;$n -lt 25;$n++){ $children=@($rows | Where-Object { $ids.Contains([int]$_.ParentProcessId) -and !$ids.Contains([int]$_.ProcessId) }); if($children.Count -eq 0){break}; foreach($child in $children){$ids.Add([int]$child.ProcessId)} }; (Get-Process -Id $ids.ToArray() | Measure-Object -Property WorkingSet64 -Sum).Sum`;
      const {stdout}=await execute('powershell.exe',['-NoProfile','-NonInteractive','-Command',code],{windowsHide:true,timeout:10000});
      rss_mib[name]=+(Number(stdout.trim())/1024/1024).toFixed(2);
    }
    const report = { dataset_entries: 100, hot_requests: 200, cold_requests: 30, concurrency: 8, model: 'SDK MockTransport only', results,rss_mib, go_mock_pipeline: { indexing_ms, first_chunk_ms: +first.toFixed(2),stream_total_ms } };
    writeFileSync(join(root, '.cache/migration-benchmark.json'), JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify(report, null, 2));
  } finally { if (user) await db.user.deleteMany({ where: { id: user.id } }); await db.$disconnect(); redis.disconnect(); }
};
