// Join pairtask from the Pi coding agent (#155).
//
// At session start the extension runs one pairtask-mcp bridge for this agent:
// one broker session that serves the pairtask tools and pushes peer and
// operator messages. Pushed messages reach the model as custom messages in a
// <pairtask-message> envelope that keeps the broker-set origin; delivery is
// never an acknowledgment, only the pairtask_acknowledge tool is. Nothing is
// started when the extension loads.
//
// Environment (set by `pairtask start pi` or `pairtask join`):
//   PAIRTASK_AGENT_ID  the logical agent ID (without it the extension stays idle)
//   PAIRTASK_SOCKET    the broker socket
//   PAIRTASK_ROOMS     rooms to join, as a JSON array
//   PAIRTASK_MCP       the pairtask-mcp binary

import { McpClient, StdioTransport } from "@earendil-works/pi-mcp";

// The part of Pi's ExtensionAPI this extension uses.
export interface PiApi {
	on(event: string, handler: (...args: unknown[]) => unknown): void;
	registerTool(tool: PiTool): void;
	sendMessage(message: PiMessage, options?: { triggerTurn?: boolean; deliverAs?: "steer" | "followUp" | "nextTurn" }): void;
}

export interface PiTool {
	name: string;
	label: string;
	description: string;
	parameters: unknown;
	promptGuidelines?: string[];
	annotations?: unknown;
	execute(toolCallId: string, params: Record<string, unknown>, signal?: AbortSignal): Promise<PiToolResult>;
}

export interface PiToolResult {
	content: unknown[];
	details?: unknown;
	structuredContent?: unknown;
}

export interface PiMessage {
	customType: string;
	content: string;
	display: boolean;
	details?: unknown;
}

export interface ExtensionOptions {
	env?: Record<string, string | undefined>;
}

const pushMethod = "notifications/claude/channel";
const listenerWaitMs = 3000;

// pairtaskExtension builds the extension; the default export reads
// process.env. Tests inject their own environment.
export function pairtaskExtension(options: ExtensionOptions = {}): (pi: PiApi) => void {
	return (pi: PiApi) => {
		const ext = new Extension(pi, options.env ?? process.env);
		pi.on("session_start", () => ext.start());
		pi.on("session_shutdown", () => ext.stop());
	};
}

export default pairtaskExtension();

interface Config {
	agent: string;
	socket: string;
	rooms: string[];
	mcp: string;
}

// Bridge is one running pairtask-mcp and its MCP client.
interface Bridge {
	generation: number;
	client: McpClient;
	ready: boolean;
	queued: unknown[];
}

class Extension {
	private pi: PiApi;
	private env: Record<string, string | undefined>;
	private generation = 0;
	private bridge: Bridge | undefined;
	private registered = new Set<string>();
	private starting: Promise<void> | undefined;

	constructor(pi: PiApi, env: Record<string, string | undefined>) {
		this.pi = pi;
		this.env = env;
	}

	// start runs one bridge for this session. Starts that overlap share one
	// start (single flight); a later start replaces the running bridge, and a
	// stop makes an in-flight start stale, so its bridge is closed.
	start(): Promise<void> {
		this.starting ??= this.startBridge().finally(() => {
			this.starting = undefined;
		});
		return this.starting;
	}

	private async startBridge(): Promise<void> {
		const generation = ++this.generation;
		await this.closeBridge();
		if (generation !== this.generation) return; // stopped while the old bridge closed
		const config = readConfig(this.env);
		if (typeof config === "string") {
			if (config !== "") this.status(config);
			return;
		}
		const bridge = this.openBridge(config, generation);
		this.bridge = bridge;
		try {
			await bridge.client.connect(transportFor(config));
			if (this.stale(bridge)) return void (await bridge.client.close());
			await this.registerTools(bridge.client);
			if (this.stale(bridge)) return void (await bridge.client.close());
			await this.checkListener(bridge);
			if (this.stale(bridge)) return void (await bridge.client.close());
		} catch (error) {
			if (this.stale(bridge)) return;
			this.bridge = undefined;
			await bridge.client.close().catch(() => {});
			this.status(`pairtask bridge failed to start: ${errorText(error)}`);
			return;
		}
		this.flush(bridge);
	}

	async stop(): Promise<void> {
		this.generation++;
		await this.closeBridge();
	}

	private stale(bridge: Bridge): boolean {
		return bridge.generation !== this.generation;
	}

	// openBridge wires the client before it connects: the bridge may push a
	// replayed message right after initialization.
	private openBridge(config: Config, generation: number): Bridge {
		const client = new McpClient({ name: "pairtask-pi", version: "0.1.0" });
		const bridge: Bridge = { generation, client, ready: false, queued: [] };
		client.onNotification(pushMethod, (params) => this.push(bridge, params));
		client.onError((error) => {
			if (!this.stale(bridge)) this.status(`pairtask bridge error: ${errorText(error)}`);
		});
		client.onClose(() => {
			if (this.stale(bridge) || this.bridge !== bridge) return;
			this.bridge = undefined;
			this.status(`pairtask bridge for ${config.agent} stopped; restart pi to reconnect. Unacknowledged messages replay then.`);
		});
		return bridge;
	}

	private async closeBridge(): Promise<void> {
		const bridge = this.bridge;
		this.bridge = undefined;
		if (bridge) await bridge.client.close().catch(() => {});
	}

	// registerTools exposes each bridge tool once per Pi runtime as
	// pairtask_<name>; calls go to whichever bridge is current.
	private async registerTools(client: McpClient): Promise<void> {
		const tools = await client.listTools();
		const guidelines = client.instructions ? [client.instructions] : undefined;
		for (const tool of tools) {
			const name = `pairtask_${tool.name}`;
			if (this.registered.has(name)) continue;
			this.registered.add(name);
			this.pi.registerTool({
				name,
				label: `pairtask ${tool.name}`,
				description: tool.description ?? `pairtask ${tool.name}`,
				parameters: tool.inputSchema ?? { type: "object", properties: {} },
				annotations: tool.annotations,
				promptGuidelines: name === "pairtask_send" ? guidelines : undefined,
				execute: (_id, params, signal) => this.call(tool.name, params, signal),
			});
		}
	}

	private async call(name: string, params: Record<string, unknown>, signal?: AbortSignal): Promise<PiToolResult> {
		const bridge = this.bridge;
		if (!bridge?.ready) throw new Error("the pairtask bridge is not connected");
		const result = await bridge.client.callTool(name, params, { signal });
		if (result.isError) throw new Error(contentText(result.content) || `pairtask ${name} failed`);
		return { content: result.content, details: result.structuredContent, structuredContent: result.structuredContent };
	}

	// checkListener waits briefly for the bridge's automatic listener and
	// reports, rather than hides, one that never started (a duplicate ID).
	private async checkListener(bridge: Bridge): Promise<void> {
		const deadline = Date.now() + listenerWaitMs;
		for (;;) {
			const state = await listenerState(bridge.client);
			if (state.state.startsWith("listening")) return;
			if (state.error || Date.now() >= deadline) {
				this.status(`pairtask is not receiving messages: ${state.error || state.state}`);
				return;
			}
			await new Promise((resolve) => setTimeout(resolve, 100));
		}
	}

	// push delivers a pushed message, or queues it until the tools are ready.
	private push(bridge: Bridge, params: unknown): void {
		if (this.stale(bridge)) return;
		if (!bridge.ready) {
			bridge.queued.push(params);
			return;
		}
		this.deliver(params);
	}

	private flush(bridge: Bridge): void {
		bridge.ready = true;
		const queued = bridge.queued.splice(0);
		for (const params of queued) this.deliver(params);
	}

	private deliver(params: unknown): void {
		const message = pushedMessage(params);
		this.pi.sendMessage(
			{ customType: "pairtask", content: envelope(message), display: true, details: message },
			{ triggerTurn: true, deliverAs: "followUp" },
		);
	}

	private status(text: string): void {
		this.pi.sendMessage({ customType: "pairtask-status", content: text, display: true }, { triggerTurn: false });
	}
}

// readConfig is the bridge configuration, "" when the extension should stay
// idle (no agent ID), or an error to report.
function readConfig(env: Record<string, string | undefined>): Config | string {
	const agent = env.PAIRTASK_AGENT_ID ?? "";
	if (agent === "") return "";
	let rooms: unknown = [];
	try {
		rooms = JSON.parse(env.PAIRTASK_ROOMS || "[]");
	} catch {
		return "PAIRTASK_ROOMS must be a JSON array of room names";
	}
	if (!Array.isArray(rooms) || !rooms.every((r) => typeof r === "string")) {
		return "PAIRTASK_ROOMS must be a JSON array of room names";
	}
	return {
		agent,
		socket: env.PAIRTASK_SOCKET || "/tmp/collab-ai.sock",
		rooms,
		mcp: env.PAIRTASK_MCP || "/usr/local/lib/pairtask/pairtask-mcp",
	};
}

function transportFor(config: Config): StdioTransport {
	const args = ["--agent-id", config.agent, "--socket", config.socket, "--harness", "pi", "--claude-channel", "--auto-listen"];
	for (const room of config.rooms) args.push("--room", room);
	return new StdioTransport({ command: config.mcp, args, inheritEnv: true, stderr: "inherit" });
}

async function listenerState(client: McpClient): Promise<{ state: string; error: string }> {
	const result = await client.callTool("listener_status", {});
	const status = (result.structuredContent ?? {}) as { state?: string; error?: string };
	return { state: status.state ?? "unknown", error: status.error ?? "" };
}

// PushedMessage is what the envelope shows: only broker-set fields.
export interface PushedMessage {
	type: string;
	origin: string;
	from: string;
	to: string;
	room: string;
	messageId: string;
	inReplyTo: string;
	ackRequested: boolean;
	text: string;
}

// pushedMessage reads the broker frame the bridge serialized into the
// notification's content. A missing origin stays unknown; it is never
// inferred from the sender.
export function pushedMessage(params: unknown): PushedMessage {
	const content = (params as { content?: unknown })?.content;
	let frame: Record<string, unknown> = {};
	try {
		frame = typeof content === "string" ? JSON.parse(content) : {};
	} catch {
		frame = {};
	}
	const payload = (frame.payload ?? {}) as { text?: unknown };
	return {
		type: str(frame.type) || "msg",
		origin: str(frame.origin) || "unknown",
		from: str(frame.from),
		to: str(frame.to),
		room: str(frame.room),
		messageId: str(frame.message_id),
		inReplyTo: str(frame.in_reply_to),
		ackRequested: frame.ack_requested === true,
		text: typeof payload.text === "string" ? payload.text : typeof content === "string" ? content : "",
	};
}

// envelope frames a pushed message so the model sees who sent it and with
// which origin. Every value is escaped, so no text can close the envelope.
export function envelope(m: PushedMessage): string {
	const attrs: Array<[string, string]> = [
		["type", m.type],
		["origin", m.origin],
		["from", m.from],
		["to", m.to],
		["room", m.room],
		["message_id", m.messageId],
		["in_reply_to", m.inReplyTo],
		["ack_requested", String(m.ackRequested)],
	];
	const head = attrs
		.filter(([, v]) => v !== "")
		.map(([k, v]) => `${k}="${escape(v)}"`)
		.join(" ");
	return `<pairtask-message ${head}>\n${escape(m.text)}\n</pairtask-message>`;
}

export function escape(text: string): string {
	return text.replace(/&/g, "&amp;").replace(/</g, "&lt;").replace(/>/g, "&gt;").replace(/"/g, "&quot;");
}

function str(v: unknown): string {
	return typeof v === "string" ? v : "";
}

function contentText(content: unknown[]): string {
	return content
		.map((c) => (c as { type?: string; text?: string }).text ?? "")
		.filter((t) => t !== "")
		.join("\n");
}

function errorText(error: unknown): string {
	return error instanceof Error ? error.message : String(error);
}
