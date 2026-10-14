// Only generated, local bytes; this script never loads .env or any model adapter.
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const { resolve } = require('node:path');
const iconv = require('iconv-lite');
const { parseDocumentBuffer } = require('../server/src/ingestion/parser.ts');

const samples = [
  ['中文.TXT', Buffer.from('中文🙂\u0000\r\n next\t')],
  ['中文.md', iconv.encode('# 标题\n\n正文', 'gbk')],
  ['bom.txt', Buffer.from('\ufeffhello')],
  ['empty.txt', Buffer.from(' \u0000\n\t')],
  ['blank.md', Buffer.from('\ufeff \u3000')],
  ['unsupported.exe', Buffer.from('fixture')],
];
const python = resolve(__dirname, '../ai-service/.venv', process.platform === 'win32' ? 'Scripts/python.exe' : 'bin/python');
const program = `import sys,json,base64
from app.ingestion.parser import parse_document
results=[]
for sample in json.load(sys.stdin):
    try: results.append({"ok":True,"text":parse_document(base64.b64decode(sample[1]),sample[0])})
    except ValueError: results.append({"ok":False})
print(json.dumps(results,ensure_ascii=True))`;
(async () => {
  const result = spawnSync(python, ['-c', program], { cwd: resolve(__dirname, '../ai-service'), windowsHide: true, encoding: 'utf8', input: JSON.stringify(samples.map(([name, bytes]) => [name, bytes.toString('base64')])) });
  if (result.status !== 0) throw new Error(result.stderr || 'Python parser failed to start');
  const actual = JSON.parse(result.stdout);
  for (const [index, [name, bytes]] of samples.entries()) {
    let expected;
    try { expected = { ok: true, text: await parseDocumentBuffer(bytes, name) }; }
    catch { expected = { ok: false }; }
    assert.deepEqual(actual[index], expected, `parser mismatch: ${name}`);
  }
  console.log(`Text parser parity: ${samples.length} local samples passed`);
})().catch(error => { console.error(error.message); process.exitCode = 1; });
