// Generated local fixtures only; never loads .env, models, OSS or OCR.
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const { resolve } = require('node:path');
const { parseDocumentBuffer } = require('../server/src/ingestion/parser.ts');
const python = resolve(__dirname, '../ai-service/.venv', process.platform === 'win32' ? 'Scripts/python.exe' : 'bin/python');
const program = `import sys,json,base64
sys.path.insert(0,'tests')
from pdf_fixtures import samples
from app.ingestion.parser import parse_document,ParseError
results=[]
for name,data in samples().items():
    try: result={"ok":True,"text":parse_document(data,name)}
    except ParseError: result={"ok":False}
    results.append([name,base64.b64encode(data).decode(),result])
print(json.dumps(results,ensure_ascii=True))`;
(async () => {
  const generated = spawnSync(python, ['-c', program], { cwd: resolve(__dirname, '../ai-service'), encoding: 'utf8', windowsHide: true });
  if (generated.status !== 0) throw new Error(generated.stderr || 'PDF fixtures failed');
  const samples = JSON.parse(generated.stdout);
  for (const [name, bytes, actual] of samples) {
    let expected;
    try { expected = { ok: true, text: await parseDocumentBuffer(Buffer.from(bytes, 'base64'), name) }; }
    catch { expected = { ok: false }; }
    if (name === 'chinese.PDF') {
      // The pinned legacy pdf.js lacks the CMap used by this CID-font fixture.
      // Track this as an explicit improvement, never as an identical result.
      assert.deepEqual(expected, { ok: false }, 'Legacy CID-font baseline changed; review comparison');
      assert.deepEqual(actual, { ok: true, text: '\n\n知识库解析测试\n第一行中文 English 123\n\n第二页内容\n结束' });
      continue;
    }
    assert.deepEqual(actual, expected, `PDF parser mismatch: ${name}`);
  }
  console.log(`PDF comparison: ${samples.length - 1} matching text/error outcomes; 1 documented Chinese CID-font improvement`);
})().catch(error => { console.error(error); process.exitCode = 1; });
