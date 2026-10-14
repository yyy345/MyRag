// Compare generated local CSV bytes; no .env or external AI/storage services.
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const { resolve } = require('node:path');
const { parseDocumentBuffer } = require('../server/src/ingestion/parser.ts');
const python = resolve(__dirname, '../ai-service/.venv', process.platform === 'win32' ? 'Scripts/python.exe' : 'bin/python');
const program = `import sys,json,base64
sys.path.insert(0,'tests')
from csv_fixtures import samples,FIXES
from app.ingestion.parser import parse_document,ParseError
results=[]
for name,data in samples().items():
    try: result={"ok":True,"text":parse_document(data,name)}
    except ParseError: result={"ok":False}
    results.append([name,base64.b64encode(data).decode(),result])
print(json.dumps({"samples":results,"fixes":FIXES},ensure_ascii=True))`;
(async () => {
  const generated = spawnSync(python, ['-c', program], { cwd: resolve(__dirname, '../ai-service'), encoding: 'utf8', windowsHide: true });
  if (generated.status !== 0) throw new Error(generated.stderr || 'CSV fixtures failed');
  const { samples, fixes } = JSON.parse(generated.stdout);
  let matched = 0, changed = 0;
  for (const [name, bytes, actual] of samples) {
    let legacy;
    try { legacy = { ok: true, text: await parseDocumentBuffer(Buffer.from(bytes, 'base64'), name) }; }
    catch { legacy = { ok: false }; }
    if (Object.hasOwn(fixes, name)) {
      const corrected = fixes[name] === null ? { ok: false } : { ok: true, text: fixes[name] };
      assert.deepEqual(actual, corrected, `Corrected CSV behavior regressed: ${name}`);
      assert.notDeepEqual(actual, legacy, `Legacy CSV behavior changed; review documented difference: ${name}`);
      changed++;
    } else {
      assert.deepEqual(actual, legacy, `CSV mismatch: ${name}`);
      matched++;
    }
  }
  console.log(`CSV comparison: ${matched} matching outcomes; ${changed} documented fixes (quotes, multiline, Markdown escaping, extra columns, invalid quotes)`);
})().catch(error => { console.error(error); process.exitCode = 1; });
