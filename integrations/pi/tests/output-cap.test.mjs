import test from 'node:test';
import assert from 'node:assert/strict';
import { pairtaskExtension } from '../index.ts';
import { fakePi } from './helpers.mjs';

function fixture(value) {
  const pi = fakePi();
  const env = value === undefined ? {} : { PAIRTASK_PI_MAX_TOKENS: value };
  pairtaskExtension({ env })(pi);
  return {
    pi,
    async transform(payload) {
      const handler = pi.handlers.get('before_provider_request');
      if (!handler) return payload;
      const result = await handler({ type: 'before_provider_request', payload }, { hasUI: false });
      return result === undefined ? payload : result;
    },
  };
}

test('output cap clamps the reported OpenRouter request without changing prompt or model', async () => {
  const payload = Object.freeze({ model: 'moonshotai/kimi-k3.1-pro', max_tokens: 838460,
    messages: Object.freeze([{ role: 'user', content: 'synthetic request' }]), stream: true });
  const { transform } = fixture('32768');
  const result = await transform(payload);
  assert.deepEqual(result, { ...payload, max_tokens: 32768 });
  assert.equal(payload.max_tokens, 838460);
  assert.equal(result.messages, payload.messages);
});

for (const field of ['max_tokens', 'max_completion_tokens', 'max_output_tokens']) {
  test(`output cap covers ${field} and preserves a smaller configured output limit`, async () => {
    const { transform } = fixture('32768');
    assert.equal((await transform({ [field]: 943718 }))[field], 32768);
    for (const value of [1024, 32768]) assert.equal((await transform({ [field]: value }))[field], value);
  });
}

test('output cap supports Google generationConfig without mutating shared configuration', async () => {
  const generationConfig = Object.freeze({ maxOutputTokens: 943718, temperature: 0.2,
    thinkingConfig: Object.freeze({ includeThoughts: true }) });
  const payload = Object.freeze({ generationConfig, contents: [] });
  const result = await fixture('32768').transform(payload);
  assert.deepEqual(result, { ...payload, generationConfig: { ...generationConfig, maxOutputTokens: 32768 } });
  assert.equal(generationConfig.maxOutputTokens, 943718);
  assert.equal((await fixture('32768').transform({ generationConfig: { maxOutputTokens: 1024 } })).generationConfig.maxOutputTokens, 1024);
});

test('output cap is strict even with a large explicit thinking budget', async () => {
  const thinking = Object.freeze({ type: 'enabled', budget_tokens: 200000 });
  const input = Object.freeze({ max_tokens: 943718, thinking });
  assert.deepEqual(await fixture('32768').transform(input), {
    max_tokens: 32768, thinking: { type: 'enabled', budget_tokens: 31744 },
  });
  assert.equal(thinking.budget_tokens, 200000);
  const small = { max_tokens: 8192, thinking: { type: 'enabled', budget_tokens: 2048 } };
  assert.deepEqual(await fixture('32768').transform(small), small);
  assert.deepEqual(await fixture('2048').transform(input), {
    max_tokens: 2048, thinking: { type: 'enabled', budget_tokens: 1024 },
  });
});

test('output cap handles the pinned Google SDK config shape and its thinking budget', async () => {
  const thinkingConfig = Object.freeze({ includeThoughts: true, thinkingBudget: 60000 });
  const config = Object.freeze({ maxOutputTokens: 943718, thinkingConfig, temperature: 0.2 });
  const payload = Object.freeze({ model: 'google-fixture', contents: [], config });
  assert.deepEqual(await fixture('32768').transform(payload), {
    ...payload, config: { ...config, maxOutputTokens: 32768,
      thinkingConfig: { ...thinkingConfig, thinkingBudget: 31744 } },
  });
  assert.equal(config.maxOutputTokens, 943718);
  assert.equal(thinkingConfig.thinkingBudget, 60000);
  for (const budget of [-1, 0, 1024]) {
    const input = { config: { maxOutputTokens: 4096, thinkingConfig: { thinkingBudget: budget } } };
    assert.deepEqual(await fixture('32768').transform(input), input);
  }
});

test('output cap respects explicit higher ceilings and the zero escape', async () => {
  const payload = { max_tokens: 943718, thinking: { type: 'enabled', budget_tokens: 200000 } };
  assert.equal((await fixture('65536').transform(payload)).max_tokens, 65536);
  assert.deepEqual(await fixture('0').transform(payload), payload);
  assert.deepEqual(await fixture(undefined).transform(payload), payload);
});

test('output cap does not invent fields or change unsupported payloads', async () => {
  const { transform } = fixture('32768');
  for (const payload of [null, undefined, false, 3, 'wire data', [], { messages: [] },
    { generationConfig: null }, { generationConfig: { temperature: 1 } },
    { max_tokens: '943718' }, { unknown: { max_tokens: 943718 } }]) {
    assert.deepEqual(await transform(payload), payload);
  }
});

test('invalid output cap environment cannot silently restore the huge request', async () => {
  for (const value of ['', 'invalid', '-1', '2047', '32768oops', '32768.5', '9007199254740992']) {
    const { transform, pi } = fixture(value);
    await pi.event('session_start');
    assert.equal((await transform({ max_tokens: 943718 })).max_tokens, 32768, value);
    assert.match(JSON.stringify(pi.messages), /32768|32,768/, `warning explains fallback for ${value}`);
  }
});
