import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, mkdirSync, copyFileSync, writeFileSync, readFileSync, rmSync, chmodSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const source = fileURLToPath(new URL('../', import.meta.url));
const oldImage = `sha256:${'a'.repeat(64)}`;
const newImage = `sha256:${'b'.repeat(64)}`;

// Docker is intentionally replaced here. These tests check orchestration and data
// preservation, while CI separately builds and boots the actual production image.
const dockerStub = `#!/usr/bin/env bash
set -eu
printf '%s | %s\n' "\${GARDEN_IMAGE:-}" "$*" >> "$FAKE_LOG"
if [[ "$1" == image ]]; then echo "$FAKE_NEW"; exit 0; fi
if [[ "$1" == run ]]; then echo 1; exit 0; fi
if [[ "$1" == cp ]]; then touch "\${@: -1}"; exit 0; fi
if [[ "$1" == exec || "$1" == pull || "$1" == rm ]]; then exit 0; fi
if [[ "$*" == *'db-version'* ]]; then echo "\${FAKE_SCHEMA:-1}"; exit 0; fi
if [[ "$*" == *'up -d'* && "\${GARDEN_IMAGE:-}" == "$FAKE_NEW" && "\${FAKE_FAIL_NEW:-0}" == 1 ]]; then exit 1; fi
if [[ "$*" == *'ps -q'* ]]; then echo garden-container; fi
`;

function fixture(t, options = {}) {
  const root = mkdtempSync(join(tmpdir(), 'little-garden-deploy-'));
  t.after(() => rmSync(root, { recursive: true, force: true }));
  for (const dir of ['scripts', 'deploy', '.local/deploy', 'fake-bin']) mkdirSync(join(root, dir), { recursive: true });
  for (const name of ['deploy.sh', 'backup.sh']) { copyFileSync(join(source, 'scripts', name), join(root, 'scripts', name)); chmodSync(join(root, 'scripts', name), 0o755); }
  writeFileSync(join(root, 'deploy/.env'), 'PUBLIC_ORIGIN=https://garden.example\n');
  writeFileSync(join(root, 'deploy/compose.yaml'), 'services: {}\n');
  writeFileSync(join(root, '.local/deploy/current'), `${oldImage}\n`);
  writeFileSync(join(root, 'fake-bin/docker'), dockerStub, { mode: 0o755 });
  const log = join(root, 'docker.log');
  const env = { ...process.env, PATH: `${join(root, 'fake-bin')}:${process.env.PATH}`, FAKE_LOG: log, FAKE_NEW: newImage, ...options };
  const run = (...args) => spawnSync('bash', [join(root, 'scripts/deploy.sh'), ...args], { env, encoding: 'utf8' });
  return { root, run, log: () => readFileSync(log, 'utf8'), current: () => readFileSync(join(root, '.local/deploy/current'), 'utf8').trim() };
}

test('successful update backs up before switching and records immutable image IDs', t => {
  const f = fixture(t); const result = f.run('v0.1.0'); assert.equal(result.status, 0, result.stderr);
  assert.equal(f.current(), newImage);
  assert.equal(readFileSync(join(f.root, '.local/deploy/previous'), 'utf8').trim(), oldImage);
  assert.ok(f.log().indexOf('garden backup') < f.log().indexOf('up -d'));
});

test('failed update rolls back only the compatible image without restoring a database', t => {
  const f = fixture(t, { FAKE_FAIL_NEW: '1' }); const result = f.run('v0.1.0'); assert.notEqual(result.status, 0);
  assert.equal(f.current(), oldImage);
  assert.ok(f.log().split('\n').some(line => line.startsWith(oldImage) && line.includes('up -d')));
  assert.ok(!f.log().includes('down -v'));
  assert.ok(!f.log().split('\n').some(line => line.includes('cp ') && line.includes(':/data/')));
});

test('schema mismatch blocks automatic rollback and preserves the deployment record', t => {
  const f = fixture(t, { FAKE_FAIL_NEW: '1', FAKE_SCHEMA: '2' }); const result = f.run('v0.1.0'); assert.notEqual(result.status, 0);
  assert.equal(f.current(), oldImage);
  assert.ok(!f.log().split('\n').some(line => line.startsWith(oldImage) && line.includes('up -d')));
  assert.match(result.stderr, /schema-incompatible/);
});

test('offline deployment does not pull an image', t => {
  const f = fixture(t); const result = f.run('--local', 'v0.1.0'); assert.equal(result.status, 0, result.stderr);
  assert.ok(!f.log().includes(' | pull '));
});

test('an arbitrary image/tag string is rejected before any pull', t => {
  const f = fixture(t); const result = f.run('latest; echo unsafe'); assert.notEqual(result.status, 0);
  assert.ok(!f.log().includes(' | pull '));
});
