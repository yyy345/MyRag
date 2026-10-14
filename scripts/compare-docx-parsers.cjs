// Generated local DOCX fixtures only. Never reads .env or calls model adapters.
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const { resolve } = require('node:path');
const { parseDocumentBuffer } = require('../server/src/ingestion/parser.ts');
const python = resolve(__dirname, '../ai-service/.venv', process.platform === 'win32' ? 'Scripts/python.exe' : 'bin/python');
const program = `import sys,json,base64
sys.path.insert(0,'tests')
from docx_fixtures import samples
from app.ingestion.parser import parse_document,ParseError
results=[]
for name,data in samples().items():
    try: result={"ok":True,"text":parse_document(data,name)}
    except ParseError: result={"ok":False}
    results.append([name,base64.b64encode(data).decode(),result])
print(json.dumps(results,ensure_ascii=True))`;
(async () => {
  const generated = spawnSync(python, ['-c', program], { cwd: resolve(__dirname, '../ai-service'), encoding: 'utf8', windowsHide: true });
  if (generated.status !== 0) throw new Error(generated.stderr || 'DOCX fixtures failed');
  const samples = JSON.parse(generated.stdout);
  for (const [name, bytes, actual] of samples) {
    let expected;
    try { expected = { ok: true, text: await parseDocumentBuffer(Buffer.from(bytes, 'base64'), name) }; }
    catch { expected = { ok: false }; }
    assert.deepEqual(actual, expected, `DOCX parser mismatch: ${name}`);
  }
  console.log(`DOCX parser parity: ${samples.length} local samples passed (exact text/error outcome)`);
})().catch(error => { console.error(error); process.exitCode = 1; });
