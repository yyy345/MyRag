// Evaluate only the legacy pure RRF block. Do not import its provider/DB modules.
const assert = require('node:assert/strict');
const { readFileSync } = require('node:fs');
const { resolve } = require('node:path');
const { spawnSync } = require('node:child_process');
const ts = require('typescript');
const source = readFileSync(resolve(__dirname, '../server/src/rag/retriever.ts'), 'utf8');
const start = source.indexOf('  const scoreMap =');
const end = source.indexOf('  // 4. 命中计数', start);
assert(start >= 0 && end > start, 'Legacy RRF source changed; review baseline extraction');
const pure = ts.transpileModule(`function legacy(vectorResults,bm25Results,rrfK=60,finalTopK=5) { ${source.slice(start,end)} return results; }`, { compilerOptions: { target: ts.ScriptTarget.ES2022 } }).outputText;
const legacy = new Function(`${pure}; return legacy;`)();
const item = id => ({ chunk_id: id, entry_id: `entry-${id}`, entry_title: `title-${id}`, content: `text-${id}`, score: 0.5 });
const cases = [[[], []], [[item('a')], []], [[], [item('b')]], [[item('a')], [item('b')]], [[item('a'), item('b'), item('c')], [item('b'), item('d')]], [Array.from({length: 20}, (_,i) => item(String(i))), [item('19'), item('17')]]];
const python = resolve(__dirname, '../ai-service/.venv', process.platform === 'win32' ? 'Scripts/python.exe' : 'bin/python');
const result = spawnSync(python, ['-c', 'import sys,json; from app.rag.retriever import reciprocal_rank_fusion; print(json.dumps([reciprocal_rank_fusion(*case) for case in json.load(sys.stdin)]))'], { cwd: resolve(__dirname, '../ai-service'), input: JSON.stringify(cases), encoding: 'utf8', windowsHide: true });
if (result.status !== 0) throw new Error(result.stderr);
const actual = JSON.parse(result.stdout);
for (let i = 0; i < cases.length; i++) {
  const convert = row => ({id: row.chunk_id, entry_id: row.entry_id, entry_title: row.entry_title, content: row.content, similarity: row.score, rank: row.score});
  const expected = legacy(cases[i][0].map(convert), cases[i][1].map(convert)).map(row => ({chunk_id: row.chunkId, entry_id: row.entryId, entry_title: row.entryTitle, content: row.content, score: row.score}));
  assert.deepEqual(actual[i], expected, `RRF mismatch ${i}`);
}
console.log(`RRF comparison: ${cases.length} matching cases`);
