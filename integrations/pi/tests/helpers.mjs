import assert from 'node:assert/strict';
export async function until(predicate, label, timeout = 4000) {
  const end = Date.now() + timeout;
  while (Date.now() < end) {
    const value = await predicate();
    if (value) return value;
    await new Promise(resolve => setTimeout(resolve, 10));
  }
  assert.fail(`timed out: ${label}`);
}
export function fakePi() {
  const handlers = new Map(), tools = new Map(), messages = [];
  return {
    handlers, tools, messages,
    on(name, handler) { handlers.set(name, handler); },
    registerTool(tool) { tools.set(tool.name, tool); },
    sendMessage(message, options) { messages.push({ message, options, registered: [...tools.keys()] }); },
    async event(name) { return handlers.get(name)?.({}, { isIdle: () => false, hasUI: false }); },
    async call(name, args = {}, signal) {
      const tool = tools.get(`pairtask_${name}`);
      assert.ok(tool, `registered tool ${name}`);
      return tool.execute('test-call', args, signal);
    },
  };
}
