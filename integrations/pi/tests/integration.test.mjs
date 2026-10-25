import test from 'node:test';
import assert from 'node:assert/strict';
import { execFile, spawn } from 'node:child_process';
import { once } from 'node:events';
import { access, mkdtemp, rm } from 'node:fs/promises';
import { createConnection } from 'node:net';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { pairtaskExtension } from '../index.ts';
import { fakePi, until } from './helpers.mjs';

const run = promisify(execFile);
const repository = fileURLToPath(new URL('../../../', import.meta.url));

// These sockets belong only to the test's temporary broker. No live operator
// session, model provider, credential, or installed pairtask process is used.
function request(socketPath, frame) {
  return new Promise((resolve, reject) => {
    const socket = createConnection(socketPath);
    let text = '';
    const timer = setTimeout(() => finish(new Error(`timed out: ${frame.type}`)), 3000);
    function finish(error, value) {
      clearTimeout(timer);
      socket.destroy();
      if (error) reject(error); else resolve(value);
    }
    socket.on('error', error => finish(error));
    socket.on('connect', () => socket.write(JSON.stringify(frame) + '\n'));
    socket.on('data', chunk => {
      text += chunk;
      if (!text.includes('\n')) return;
      try {
        const answer = JSON.parse(text.slice(0, text.indexOf('\n')));
        if (answer.type === 'error') throw new Error(`${answer.code}: ${answer.detail}`);
        finish(null, answer);
      } catch (error) { finish(error); }
    });
    socket.on('end', () => { if (!text.includes('\n')) finish(new Error(`closed before ${frame.type} response`)); });
  });
}

function pushed(pi, id) {
  return pi.messages.filter(item => item.message.customType === 'pairtask' &&
    item.message.content.includes(`message_id="${id}"`));
}

function structured(result) {
  assert.ok(result.structuredContent, 'MCP structured result survives the adapter');
  return result.structuredContent;
}

test('real broker: operator provenance, explicit ack, claims, reply, replay and duplicate ownership', { timeout: 120000 }, async t => {
  const dir = await mkdtemp(join(tmpdir(), 'pi-e2e-'));
  const socket = join(dir, 'agent.sock'), operator = join(dir, 'operator.sock');
  const mcpPath = join(dir, 'pairtask-mcp'), brokerPath = join(dir, 'pairtask-broker');
  const instances = [];
  let broker;
  let logs = '';
  t.after(async () => {
    await Promise.all(instances.map(pi => pi.event('session_shutdown')));
    if (broker && broker.exitCode === null && broker.signalCode === null) {
      const exited = once(broker, 'exit');
      const kill = setTimeout(() => broker.kill('SIGKILL'), 2000);
      broker.kill('SIGTERM');
      await exited;
      clearTimeout(kill);
    }
    await rm(dir, { recursive: true, force: true });
  });
  // Go is already installed by CI. Build the source under review, not a binary
  // from PATH, so the integration cannot silently exercise another revision.
  await run('go', ['build', '-o', brokerPath, './cmd/broker'], { cwd: repository, timeout: 60000 });
  await run('go', ['build', '-o', mcpPath, './cmd/mcp'], { cwd: repository, timeout: 60000 });
  broker = spawn(brokerPath, [], { env: { ...process.env,
    PAIRTASK_SOCKET_PATH: socket, PAIRTASK_DB_PATH: join(dir, 'broker.db'),
    PAIRTASK_OPERATOR_SOCKET_PATH: operator, PAIRTASK_OPERATOR_UID: String(process.getuid()),
  }, stdio: ['ignore', 'pipe', 'pipe'] });
  for (const stream of [broker.stdout, broker.stderr]) stream.on('data', chunk => { logs = (logs + chunk).slice(-10000); });
  await until(async () => {
    assert.equal(broker.exitCode, null, `broker exited: ${logs}`);
    try { await access(socket); await access(operator); return true; } catch { return false; }
  }, 'test broker sockets');
  const env = { PAIRTASK_AGENT_ID: 'pi-integration', PAIRTASK_SOCKET: socket,
    PAIRTASK_MCP: mcpPath, PAIRTASK_ROOMS: '["alpha"]' };
  function instance() {
    const pi = fakePi();
    pairtaskExtension({ env })(pi);
    instances.push(pi);
    return pi;
  }
  const op = frame => request(operator, frame);
  const sessions = async () => (await request(socket, { type: 'status' })).status.sessions.filter(s => s.state === 'transport_connected');
  const stage = async id => (await op({ type: 'operator_message_stages', message_id: id })).stages.recipients.find(s => s.agent_id === env.PAIRTASK_AGENT_ID);
  const send = async id => {
    const result = await op({ type: 'operator_send', message_id: id, to: '*', room: 'alpha', payload: { text: `test ${id}` } });
    assert.equal(result.stage, 'accepted');
  };
  const pi = instance();
  await pi.event('session_start');
  for (const name of ['send', 'acknowledge', 'claim', 'read_claims', 'status', 'read_status', 'join_room', 'leave_room', 'list_rooms']) {
    assert.ok(pi.tools.has(`pairtask_${name}`), `real tool ${name} registered`);
  }
  assert.ok(pi.tools.get('pairtask_send').promptGuidelines?.length, 'host instructions reach Pi');
  const listener = structured(await pi.call('listener_status'));
  assert.match(listener.state, /^listening/);
  const originalSession = listener.session_id;
  assert.equal((await sessions()).length, 1, 'tools and push share one broker owner');
  const declaration = structured(await pi.call('status', { state: 'active', task: 'integration' }));
  assert.equal(declaration.agent_status.session_id, originalSession);
  assert.equal(declaration.agent_status.agent_id, env.PAIRTASK_AGENT_ID);

  await send('operator-parent');
  const delivered = await until(() => pushed(pi, 'operator-parent')[0], 'operator push');
  assert.match(delivered.message.content, /origin="operator"/);
  assert.match(delivered.message.content, /from="operator"/);
  assert.match(delivered.message.content, /room="alpha"/);
  assert.match(delivered.message.content, /ack_requested="true"/);
  assert.deepEqual(delivered.options, { triggerTurn: true, deliverAs: 'followUp' });
  assert.ok(delivered.registered.includes('pairtask_acknowledge'));
  await until(async () => (await stage('operator-parent')).stage === 'adapter_received', 'receipt without implicit acknowledgment');
  assert.equal((await stage('operator-parent')).session_id, originalSession);

  const claim = { operator_message_id: 'operator-parent', task: 'reply' };
  const claimed = structured(await pi.call('claim', claim));
  assert.ok(claimed.claims.some(c => c.task === 'reply' && c.agent_id === env.PAIRTASK_AGENT_ID && c.state === 'active'));
  await pi.call('send', { to: 'operator', text: 'model-free reply', message_id: 'pi-reply', in_reply_to: 'operator-parent' });
  const reply = await until(async () => (await op({ type: 'operator_receive' })).page.messages.find(m => m.message_id === 'pi-reply'), 'operator receives reply');
  assert.equal(reply.origin, 'agent');
  assert.equal(reply.from, env.PAIRTASK_AGENT_ID);
  assert.equal(reply.session_id, originalSession);
  assert.equal(reply.in_reply_to, 'operator-parent');
  assert.equal(reply.payload.text, 'model-free reply');
  const done = structured(await pi.call('finish_claim', claim));
  assert.ok(done.claims.some(c => c.task === 'reply' && c.state === 'done'));
  await pi.call('acknowledge', { message_id: 'operator-parent' });
  await until(async () => (await stage('operator-parent')).stage === 'agent_acknowledged', 'explicit broker acknowledgment');

  const duplicate = instance();
  await duplicate.event('session_start');
  const diagnostic = duplicate.messages.find(item => item.message.customType === 'pairtask-status' && /duplicate|not receiving|failed/i.test(item.message.content));
  assert.ok(diagnostic, 'duplicate owner is visible even if MCP initialization succeeds');
  assert.equal(diagnostic.options.triggerTurn, false);
  assert.equal((await sessions()).length, 1, 'duplicate bridge cannot register another owner');
  assert.equal(structured(await pi.call('listener_status')).session_id, originalSession);
  await duplicate.event('session_shutdown');

  await send('pending-replay');
  await until(() => pushed(pi, 'pending-replay').length === 1, 'original owner still receives');
  await until(async () => (await stage('pending-replay')).stage === 'adapter_received', 'pending receipt');
  await pi.event('session_shutdown');
  await until(async () => (await sessions()).length === 0, 'broker releases stopped owner');
  const resumed = instance();
  await resumed.event('session_start');
  await until(() => pushed(resumed, 'pending-replay').length === 1, 'durable replay through new MCP child');
  const resumedSession = structured(await resumed.call('listener_status')).session_id;
  assert.notEqual(resumedSession, originalSession);
  await until(async () => (await stage('pending-replay')).session_id === resumedSession, 'replay ownership moves');
  assert.equal(pushed(resumed, 'operator-parent').length, 0, 'acknowledged parent does not replay before later pending message');
  assert.equal((await stage('operator-parent')).stage, 'agent_acknowledged');
  await resumed.call('acknowledge', { message_id: 'pending-replay' });
  await until(async () => (await stage('pending-replay')).stage === 'agent_acknowledged', 'replayed message explicitly acknowledged');
});
