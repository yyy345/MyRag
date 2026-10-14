// Fixed text only. Never loads configuration, documents or provider credentials.
const assert = require('node:assert/strict');
const { spawnSync } = require('node:child_process');
const { resolve } = require('node:path');
const { chunkText } = require('../server/src/rag/chunker.ts');
const python = resolve(__dirname, '../ai-service/.venv', process.platform === 'win32' ? 'Scripts/python.exe' : 'bin/python');
const program = `import sys,json
sys.path.insert(0,'tests')
from chunk_fixtures import samples
from app.rag.chunker import chunk_text
print(json.dumps([[name,text,chunk_text(text)] for name,text in samples().items()],ensure_ascii=True))`;
(async () => {
  const generated = spawnSync(python, ['-c', program], { cwd: resolve(__dirname, '../ai-service'), encoding: 'utf8', windowsHide: true });
  if (generated.status !== 0) throw new Error(generated.stderr);
  let matched = 0, fixed = 0;
  for (const [name, text, actual] of JSON.parse(generated.stdout)) {
    const legacy = await chunkText(text);
    if (name === 'emoji_surrogate_boundary') {
      assert.notDeepEqual(actual, legacy, 'Review legacy surrogate splitting if behavior changes');
      assert(legacy.some(({content}) => /[\ud800-\udbff]$|^[\udc00-\udfff]/u.test(content)), 'Legacy no longer splits emoji');
      for (const {content} of actual) assert(content.isWellFormed(), 'Python damaged emoji');
      fixed++;
    } else {
      assert.deepEqual(actual, legacy, `Chunk mismatch: ${name}`);
      matched++;
    }
  }
  console.log(`Chunk comparison: ${matched} identical; ${fixed} documented surrogate fix`);
})().catch(error => { console.error(error); process.exitCode = 1; });
