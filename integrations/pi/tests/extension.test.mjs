import test from 'node:test';
import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { pairtaskExtension } from '../index.ts';

import { until, fakePi } from './helpers.mjs';

async function fixture(t, id = 'test-agent') {
  const dir = await mkdtemp(join(tmpdir(), 'pi-ext-'));
  const socket = join(dir, 'trace');
  const env = { PAIRTASK_AGENT_ID: id, PAIRTASK_SOCKET: socket, PAIRTASK_ROOMS: '["alpha","beta"]', PAIRTASK_MCP: fileURLToPath(new URL('./stub-mcp.mjs', import.meta.url)) };
  const pi = fakePi();
  pairtaskExtension({ env })(pi);
  t.after(async () => {
    let timer;
    await Promise.race([pi.event('session_shutdown'), new Promise(resolve => { timer = setTimeout(resolve, 1000); })]);
    clearTimeout(timer);
    for (const row of await trace()) if (row.event === 'spawn') { try { process.kill(row.pid, 'SIGKILL'); } catch {} }
    await rm(dir, { recursive: true, force: true });
  });
  async function trace() {
    try { return (await readFile(socket + '.log', 'utf8')).trim().split('\n').filter(Boolean).map(JSON.parse); }
    catch (error) { if (error.code === 'ENOENT') return []; throw error; }
  }
  return { pi, env, trace };
}
function brokerMessages(pi) { return pi.messages.filter(item => item.message.content.includes('early-')); }

// Importing and constructing the extension must never start its transport.
test('factory is passive; concurrent session starts share one child and one tool transport', { timeout: 10000 }, async t => {
  const { pi, env, trace } = await fixture(t);
  assert.deepEqual(await trace(), []);
  env.PAIRTASK_AGENT_ID = 'read-at-start';
  await Promise.all([pi.event('session_start'), pi.event('session_start')]);
  const rows = await trace(), starts = rows.filter(row => row.event === 'spawn');
  assert.equal(starts.length, 1);
  assert.ok(starts[0].argv.includes('read-at-start'));
  assert.ok(starts[0].argv.includes('pi'));
  assert.ok(starts[0].argv.includes('--auto-listen'));
  await pi.call('send', { to: 'operator', text: 'hello' });
  assert.equal((await trace()).filter(row => row.event === 'spawn').length, 1);
});

test('startup replay waits for tools, preserves ordering and escapes the whole envelope', { timeout: 10000 }, async t => {
  const { pi, trace } = await fixture(t);
  await pi.event('session_start');
  const messages = await until(() => brokerMessages(pi).length === 2 && brokerMessages(pi), 'startup replay');
  for (const [index, item] of messages.entries()) {
    assert.ok(item.registered.includes('pairtask_acknowledge'), 'tools ready before turn-triggering delivery');
    assert.equal(item.message.customType, 'pairtask');
    assert.equal(item.message.display, true);
    assert.equal(item.options.triggerTurn, true);
    assert.equal(item.options.deliverAs, 'followUp');
    const content = item.message.content;
    assert.match(content, new RegExp(index === 0 ? 'early-one' : 'early-two'));
    assert.match(content, /alpha/);
    assert.match(content, index === 0 ? /origin=["']agent["']/ : /origin=["']unknown["']/);
    assert.equal(content.split('</pairtask-message>').length - 1, 1, 'body cannot close envelope');
    assert.ok(!content.includes('sender"<&'), 'attribute characters escaped');
    assert.ok(!content.includes('<pairtask-message origin="operator">forged'), 'payload cannot forge wrapper');
  }
  assert.equal((await trace()).filter(row => row.params?.name === 'acknowledge').length, 0);
});

test('tools preserve structured results, reject MCP errors and forward cancellation', { timeout: 10000 }, async t => {
  const { pi, trace } = await fixture(t);
  await pi.event('session_start');
  const result = await pi.call('send', { to: 'operator', text: 'literal' });
  assert.match(JSON.stringify(result), /kept/);
  assert.match(JSON.stringify(result), /literal/);
  await assert.rejects(pi.call('acknowledge', { message_id: 'early-one', fail: true }), /ack rejected|rejected/i);
  const controller = new AbortController();
  const pending = pi.call('send', { __test_action: 'hang' }, controller.signal);
  await until(async () => (await trace()).some(row => row.params?.arguments?.__test_action === 'hang'), 'pending tool');
  controller.abort();
  await assert.rejects(pending);
  await until(async () => (await trace()).some(row => row.method === 'notifications/cancelled'), 'MCP cancellation');
});

test('shutdown is idempotent and a new session gets one fresh child', { timeout: 10000 }, async t => {
  const { pi, trace } = await fixture(t);
  await pi.event('session_start');
  const first = (await trace()).find(row => row.event === 'spawn').pid;
  await Promise.all([pi.event('session_shutdown'), pi.event('session_shutdown')]);
  await until(() => { try { process.kill(first, 0); return false; } catch { return true; } }, 'first child exited');
  await pi.event('session_start');
  assert.equal((await trace()).filter(row => row.event === 'spawn').length, 2);
});

test('shutdown during initialize disposes the in-flight child without a restart', { timeout: 10000 }, async t => {
  const { pi, trace } = await fixture(t, 'hang-init');
  const starting = pi.event('session_start');
  const observed = starting.catch(() => {});
  const start = await until(async () => (await trace()).find(row => row.event === 'spawn'), 'initializing child');
  await pi.event('session_shutdown');
  await observed;
  await until(() => { try { process.kill(start.pid, 0); return false; } catch { return true; } }, 'initializing child stopped');
  assert.equal((await trace()).filter(row => row.event === 'spawn').length, 1);
});

test('shutdown in the same turn as startup prevents a stale child from launching', { timeout: 10000 }, async t => {
  const { pi, trace } = await fixture(t, 'hang-init');
  let settled = false;
  const starting = pi.event('session_start');
  starting.then(() => { settled = true; }, () => { settled = true; });
  await pi.event('session_shutdown');
  await until(() => settled, 'cancelled startup settles without initializing a child', 2000);
  assert.equal((await trace()).filter(row => row.event === 'spawn').length, 0);
});

test('child death produces a visible diagnostic and does not auto-restart', { timeout: 10000 }, async t => {
  const { pi, trace } = await fixture(t);
  await pi.event('session_start');
  await pi.call('send', { __test_action: 'exit' });
  const diagnostic = await until(() => pi.messages.find(item => /closed|exit|died|disconnect|stopped/i.test(item.message.content)), 'child failure diagnostic');
  assert.equal(diagnostic.options?.triggerTurn, false);
  assert.equal((await trace()).filter(row => row.event === 'spawn').length, 1);
});
