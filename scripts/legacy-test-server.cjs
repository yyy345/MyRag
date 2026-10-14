// Test configuration is explicit. Never load server/.env for agent tests.
const testDatabase = process.env.MIGRATION_TEST_DATABASE_URL || 'postgresql://postgres:migration-test-only@127.0.0.1:55432/migration_test';
const target = new URL(testDatabase);
if (target.hostname !== '127.0.0.1' || target.port !== '55432' || !/^\/migration_(?:test|c26_[a-z0-9_]+)$/.test(target.pathname)) throw new Error('Legacy tests require the isolated migration database');
Object.assign(process.env, {
  PORT: '3100',
  HOST: '127.0.0.1',
  DATABASE_URL: testDatabase,
  REDIS_URL: 'redis://127.0.0.1:56380/0',
  RABBITMQ_URL: 'amqp://guest:guest@127.0.0.1:55673',
  JWT_SECRET: 'local-migration-test-only',
});
// External integration is opt-in. Synthetic uploads belong to UUID test users
// from the isolated database. Integration tests clean up only their own objects.
if (process.env.MIGRATION_EXTERNAL_AI !== '1') {
  process.env.OSS_ACCESS_KEY_ID = 'migration-test-invalid-key';
  process.env.OSS_ACCESS_KEY_SECRET = 'migration-test-invalid-secret';
  process.env.OSS_BUCKET = 'migration-test-not-configured';
  process.env.BAILIAN_API_KEY = 'migration-test-invalid-key';
  // Never send fixture data to external providers in local-only mode.
  // Keep real Express/DB/Redis/RabbitMQ; only the external adapter boundary is replaced.
  const disabled = async () => { throw new Error('外部服务未启用（本地测试模式）'); };
  for (const [file, exports] of [
    ['../server/src/ai/bailian.ts', { getEmbedding: disabled, getEmbeddings: disabled, describeImage: disabled, chat: disabled, streamChat: async function* () { await disabled(); } }],
    ['../server/src/storage/oss.ts', { uploadImage: disabled, deleteImage: async () => {} }],
  ]) {
    const filename = require.resolve(file);
    require.cache[filename] = { id: filename, filename, loaded: true, exports };
  }
}
require('../server/src/index.ts');
