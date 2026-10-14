const { spawnSync } = require('node:child_process');
const path = require('node:path');
const cwd = path.join(__dirname, '..');
for (const [command, args, env] of [
  ['docker', ['compose', '-f', 'compose.migration-test.yml', 'up', '-d', '--wait'], process.env],
  [process.execPath, ['node_modules/prisma/build/index.js', 'migrate', 'deploy', '--schema', 'server/prisma/schema.prisma'], { ...process.env, DATABASE_URL: 'postgresql://postgres:migration-test-only@127.0.0.1:55432/migration_test' }],
]) {
  const result = spawnSync(command, args, { cwd, env, stdio: 'inherit', windowsHide: true });
  if (result.error) { console.error(result.error.message); process.exit(1); }
  if (result.status !== 0) process.exit(result.status || 1);
}
