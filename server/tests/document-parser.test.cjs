const assert = require('node:assert/strict');
const { test } = require('node:test');

// Reproduce PDF parser output containing NULs without a user document.
let pdfText = '';
const pdfPath = require.resolve('pdf-parse');
require.cache[pdfPath] = {
  id: pdfPath, filename: pdfPath, loaded: true,
  exports: async () => ({ text: pdfText, numpages: 1 }),
};
const { parseDocumentBuffer } = require('../src/ingestion/parser.ts');

test('PDF text removes NULs while preserving Chinese and whitespace', async () => {
  pdfText = '中文\u0000正文\n第二行\t内容\u0000';
  assert.equal(await parseDocumentBuffer(Buffer.alloc(0), 'test.pdf'), '中文正文\n第二行\t内容');
});

test('PDF containing only NULs and whitespace fails before persistence', async () => {
  pdfText = '\u0000 \n\u0000';
  await assert.rejects(parseDocumentBuffer(Buffer.alloc(0), 'test.pdf'), /无有效文本/);
});

test('plain text uses the same cleanup and preserves ordinary text', async () => {
  assert.equal(await parseDocumentBuffer(Buffer.from('hello\u0000 world\n'), 'test.txt'), 'hello world\n');
  assert.equal(await parseDocumentBuffer(Buffer.from('# 标题\n正文'), 'test.md'), '# 标题\n正文');
});
