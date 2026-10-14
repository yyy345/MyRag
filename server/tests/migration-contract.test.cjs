const assert = require('node:assert/strict');
const { test } = require('node:test');
const fs = require('node:fs');
const path = require('node:path');
const express = require('express');
const jwt = require('jsonwebtoken');
const contract = require('../../contracts/legacy-api.json');
// Use a test-only key before importing the actual middleware. No .env is loaded from server cwd.
process.env.JWT_SECRET = 'migration-contract-test-key';
const { authMiddleware } = require('../src/middleware/auth.middleware.ts');
const { errorHandler } = require('../src/middleware/error.middleware.ts');

test('baseline covers every mounted Express route, including multiline declarations', () => {
  for (const module of Object.keys(contract.mounts)) {
    const source = fs.readFileSync(path.join(__dirname, `../src/routes/${module}.routes.ts`), 'utf8');
    const actual = [...source.matchAll(/router\.(get|post|put|patch|delete)\(\s*['"]([^'"]+)['"]/g)]
      .map(([, method, route]) => `${method.toUpperCase()} ${route}`).sort();
    const expected = contract.routes.filter(r => r.module === module).map(r => `${r.method} ${r.path}`).sort();
    assert.deepEqual(actual, expected, `route drift: ${module}`);
  }
});

test('actual JWT middleware over HTTP preserves successful identity and error envelopes', async t => {
  const app = express();
  app.get('/identity', authMiddleware, (req, res) => res.json({ code: 0, data: req.user, message: 'ok' }));
  app.use(errorHandler);
  const server = await new Promise(resolve => { const s = app.listen(0, '127.0.0.1', () => resolve(s)); });
  t.after(() => new Promise(resolve => { server.close(resolve); server.closeAllConnections(); }));
  const url = `http://127.0.0.1:${server.address().port}/identity`;
  for (const [sample, token] of [[contract.samples.unauthorized, null], [contract.samples.invalidToken, 'invalid'],
    [contract.samples.invalidToken, jwt.sign({ id: 'test', username: 'test' }, process.env.JWT_SECRET, { expiresIn: -1 })]]) {
    const response = await fetch(url, { headers: token ? { Authorization: `Bearer ${token}` } : {} });
    assert.equal(response.status, sample.status);
    assert.deepEqual(await response.json(), sample.body);
  }
  const identity = { id: '11111111-1111-4111-8111-111111111111', username: 'contract-test' };
  const token = jwt.sign(identity, process.env.JWT_SECRET, { expiresIn: '7d' });
  const response = await fetch(url, { headers: { Authorization: `Bearer ${token}` } });
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { code: 0, data: identity, message: 'ok' });
});
