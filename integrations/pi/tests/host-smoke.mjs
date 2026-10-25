// Mandatory image proof, invoked explicitly after installing the Pi bundle:
// node --test integrations/pi/tests/host-smoke.mjs
// Overrides allow the same proof against locally built binaries and npm Pi.
import test from 'node:test';
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { createServer } from 'node:http';
import { createConnection } from 'node:net';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { until } from './helpers.mjs';

function deferred() {
  let resolve;
  const promise = new Promise(done => { resolve = done; });
  return { promise, resolve };
}

function wire(socketPath, message) {
  return new Promise((resolve, reject) => {
    const socket = createConnection(socketPath);
    let buffer = '';
    const timer = setTimeout(() => finish(new Error(`socket timeout: ${message.type}`)), 3000);
    function finish(error, answer) {
      clearTimeout(timer);
      socket.destroy();
      if (error) reject(error); else resolve(answer);
    }
    socket.on('error', error => finish(error));
    socket.on('connect', () => socket.write(JSON.stringify(message) + '\n'));
    socket.on('data', bytes => {
      buffer += bytes;
      if (!buffer.includes('\n')) return;
      try {
        const answer = JSON.parse(buffer.slice(0, buffer.indexOf('\n')));
        if (answer.type === 'error') throw new Error(`${answer.code}: ${answer.detail}`);
        finish(null, answer);
      } catch (error) { finish(error); }
    });
  });
}

function completion(response, calls = []) {
  response.writeHead(200, { 'content-type': 'text/event-stream', 'cache-control': 'no-cache' });
  const chunk = (delta, finish_reason = null) => response.write('data: ' + JSON.stringify({
    id: 'fake-completion', object: 'chat.completion.chunk', created: 1, model: 'fixture',
    choices: [{ index: 0, delta, finish_reason }],
  }) + '\n\n');
  chunk({ role: 'assistant' });
  if (calls.length) {
    chunk({ tool_calls: calls.map((call, index) => ({ index, id: `call_${index}`, type: 'function', function: call })) });
    chunk({}, 'tool_calls');
  } else {
    chunk({ content: 'fixture complete' });
    chunk({}, 'stop');
  }
  response.end('data: [DONE]\n\n');
}

function responseTools(id) {
  return [
    { name: 'pairtask_acknowledge', arguments: JSON.stringify({ message_id: id }) },
    { name: 'pairtask_send', arguments: JSON.stringify({ to: 'operator', message_id: `reply-${id}`, in_reply_to: id, text: `host reply ${id}` }) },
  ];
}

async function stop(child) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return;
  const exited = once(child, 'exit');
  const kill = setTimeout(() => child.kill('SIGKILL'), 2000);
  child.stdin?.end();
  if (!child.stdin) child.kill('SIGTERM');
  await exited;
  clearTimeout(kill);
}

test('installed Pi wakes on idle push and queues busy push, with real tools and explicit acknowledgments', { timeout: 90000 }, async t => {
  const dir = await mkdtemp(join(tmpdir(), 'pi-host-'));
  const agentDir = join(dir, 'agent'), work = join(dir, 'work');
  await mkdir(agentDir); await mkdir(work);
  const socket = join(dir, 'broker.sock'), operator = join(dir, 'operator.sock');
  const idleGate = deferred(), busyGate = deferred();
  const requests = [], events = [], failures = [];
  let broker, pi;
  let stderr = '';
  const server = createServer(async (request, response) => {
    try {
      assert.equal(request.url, '/v1/chat/completions');
      let body = '';
      for await (const bytes of request) body += bytes;
      const input = JSON.parse(body);
      requests.push(input);
      const last = input.messages.at(-1);
      if (last.role === 'tool') return completion(response);
      const content = typeof last.content === 'string' ? last.content : last.content.map(block => block.text || '').join('\n');
      const id = content.match(/message_id="(host-idle|host-busy)"/)?.[1];
      if (!id && content.includes('hold busy request')) {
        await busyGate.promise;
        return completion(response);
      }
      assert.ok(id, `unexpected fake-provider prompt: ${content.slice(0, 300)}`);
      assert.match(content, /origin="operator"/);
      assert.match(content, /room="alpha"/);
      for (const name of ['pairtask_acknowledge', 'pairtask_send']) {
        assert.ok(input.tools.some(tool => tool.function.name === name), `Pi exposes ${name}`);
      }
      if (id === 'host-idle') await idleGate.promise;
      completion(response, responseTools(id));
    } catch (error) {
      failures.push(error);
      response.writeHead(500);
      response.end(String(error));
    }
  });
  t.after(async () => {
    idleGate.resolve(); busyGate.resolve();
    await stop(pi); await stop(broker);
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
    await rm(dir, { recursive: true, force: true });
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  await writeFile(join(agentDir, 'models.json'), JSON.stringify({ providers: {
    'pairtask-fixture': { baseUrl: `http://127.0.0.1:${server.address().port}/v1`, api: 'openai-completions', apiKey: 'local-fixture-only',
      models: [{ id: 'fixture', name: 'Local fixture', reasoning: false, input: ['text'], contextWindow: 32768, maxTokens: 1024,
        cost: { input: 0, output: 0, cacheRead: 0, cacheWrite: 0 } }] },
  } }));
  await writeFile(join(agentDir, 'settings.json'), JSON.stringify({ compaction: { enabled: false }, retry: { enabled: false } }));
  const brokerPath = process.env.PAIRTASK_TEST_BROKER || '/usr/local/lib/pairtask/pairtask-broker';
  const mcpPath = process.env.PAIRTASK_TEST_MCP || '/usr/local/lib/pairtask/pairtask-mcp';
  const piPath = process.env.PAIRTASK_TEST_PI || '/usr/local/bin/pi';
  const extensionPath = process.env.PAIRTASK_TEST_EXTENSION || '/usr/local/lib/pairtask/pi-extension/index.ts';
  const systemEnv = { PATH: process.env.PATH, HOME: process.env.HOME, TMPDIR: dir };
  broker = spawn(brokerPath, [], { env: { ...systemEnv, PAIRTASK_SOCKET_PATH: socket,
    PAIRTASK_DB_PATH: join(dir, 'broker.db'), PAIRTASK_OPERATOR_SOCKET_PATH: operator,
    PAIRTASK_OPERATOR_UID: String(process.getuid()) }, stdio: ['ignore', 'pipe', 'pipe'] });
  broker.on('error', error => failures.push(error));
  broker.stdout.resume(); broker.stderr.on('data', bytes => { stderr = (stderr + bytes).slice(-20000); });
  await until(async () => {
    assert.equal(failures.length, 0, String(failures[0]));
    try { return (await wire(socket, { type: 'status' })).status.reachable; } catch { return false; }
  }, 'isolated broker starts');
  pi = spawn(piPath, ['--mode', 'rpc', '--no-session', '--offline', '--no-extensions', '--no-skills',
    '--no-context-files', '--no-mcp', '--no-builtin-tools', '-e', extensionPath,
    '--provider', 'pairtask-fixture', '--model', 'fixture', '--thinking', 'off'],
  { cwd: work, env: { ...systemEnv, PI_CODING_AGENT_DIR: agentDir, PI_OFFLINE: '1',
    PAIRTASK_AGENT_ID: 'pi-host', PAIRTASK_SOCKET: socket, PAIRTASK_ROOMS: '["alpha"]', PAIRTASK_MCP: mcpPath },
    stdio: ['pipe', 'pipe', 'pipe'] });
  pi.on('error', error => failures.push(error));
  pi.stderr.on('data', bytes => { stderr = (stderr + bytes).slice(-20000); });
  let buffer = '';
  pi.stdout.on('data', bytes => {
    buffer += bytes;
    while (buffer.includes('\n')) {
      const index = buffer.indexOf('\n'), line = buffer.slice(0, index);
      buffer = buffer.slice(index + 1);
      try { events.push(JSON.parse(line)); } catch (error) { failures.push(error); }
    }
  });
  const command = value => pi.stdin.write(JSON.stringify(value) + '\n');
  const healthy = () => {
    assert.equal(failures.length, 0, `${failures[0]}\n${stderr}`);
    assert.equal(pi.exitCode, null, `Pi exited: ${stderr}`);
  };
  command({ type: 'get_state', id: 'ready' });
  await until(() => { healthy(); return events.find(e => e.id === 'ready' && e.success); }, 'Pi RPC ready', 20000);
  const send = async id => {
    const answer = await wire(operator, { type: 'operator_send', message_id: id, to: '*', room: 'alpha', payload: { text: id } });
    assert.equal(answer.stage, 'accepted');
  };
  const stage = async id => (await wire(operator, { type: 'operator_message_stages', message_id: id })).stages.recipients.find(r => r.agent_id === 'pi-host')?.stage;
  const replied = async id => {
    healthy();
    const message = (await wire(operator, { type: 'operator_receive' })).page.messages.find(m => m.message_id === `reply-${id}`);
    if (!message) return false;
    assert.equal(message.origin, 'agent'); assert.equal(message.from, 'pi-host');
    assert.equal(message.in_reply_to, id);
    return (await stage(id)) === 'agent_acknowledged';
  };

  await send('host-idle');
  await until(() => { healthy(); return requests.length === 1; }, 'idle push triggers provider request', 15000);
  assert.equal(await stage('host-idle'), 'adapter_received', 'push did not auto-ack before a model tool call');
  idleGate.resolve();
  await until(() => replied('host-idle'), 'real Pi executes ack and reply tools', 15000);
  await until(() => events.some(e => e.type === 'agent_settled'), 'idle push turn settles', 15000);

  const beforeBusy = requests.length;
  command({ type: 'prompt', id: 'busy', message: 'hold busy request' });
  await until(() => { healthy(); return requests.length === beforeBusy + 1; }, 'busy provider request', 15000);
  await send('host-busy');
  await until(async () => (await stage('host-busy')) === 'adapter_received', 'busy push delivered');
  assert.equal(requests.length, beforeBusy + 1, 'busy push waits for the running turn');
  busyGate.resolve();
  await until(() => replied('host-busy'), 'queued follow-up executes ack and reply tools', 15000);
  healthy();
});
