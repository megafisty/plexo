import { request } from "../render.js";
import { applyEnvelope, applySendResult } from "../store/apply.js";
import type { Store } from "../store/state.js";
import type { View } from "../store/state.js";
import { type Command, type CommandInput, isEnvelope, nextCID, type Result } from "./protocol.js";
// The browser WebSocket transport. It owns the connection, decodes envelopes
// into the store, and reconnects with backoff. It is the only module that
// talks to the core.

/** CommandResult reports the core's synchronous ack/err for one command. */
export interface CommandResult {
	accepted: boolean;
	errorCode?: string;
	errorMsg?: string;
}

export interface Transport {
	/** send queues a command and returns its cid for result correlation. */
	send(cmd: CommandInput): string;
	close(): void;
}

/** ConnectOpts configures the transport callbacks. */
export interface ConnectOpts {
	/** onResult fires once per command with the matching ack/err. */
	onResult?: (cid: string, result: CommandResult) => void;
	/** onHello fires once per connection when the core's hello arrives, carrying
	 * whether this socket resumed an existing durable subscription. Interest
	 * survives a resume, so the caller re-asserts it only when resumed is false
	 * (a fresh subscription after a reload or an expired grace window). */
	onHello?: (resumed: boolean) => void;
	/** onClose fires after any disconnect, before a reconnect is scheduled. The
	 * caller uses it to fail in-flight command promises rather than let them
	 * time out against a socket that is gone. */
	onClose?: () => void;
}

const MAX_BACKOFF_MS = 15000;

/** newSubscriptionID returns a fresh identity for one Transport. The core keys
 * the durable subscription on it, so a reconnect resumes interest while a page
 * reload starts a new subscription. */
function newSubscriptionID(): string {
	return `sub-${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 10)}`;
}

/** connect opens the core socket and keeps it open. */
export function connect(
	store: Store,
	view: View,
	opts: ConnectOpts = {},
): Transport {
	const { onResult, onHello, onClose } = opts;
	const url = new URL("/ws", window.location.href);
	url.protocol = url.protocol === "https:" ? "wss:" : "ws:";

	const subscriptionID = newSubscriptionID();

	let ws: WebSocket | null = null;
	let stopped = false;
	let attempt = 0;

	const open = (): void => {
		const socket = new WebSocket(url);
		ws = socket;
		socket.onopen = () => {
			attempt = 0;
			// Attach this socket to its durable server-side subscription before any
			// command, so a reconnect keeps the interest this Transport asserted.
			socket.send(
				JSON.stringify({ t: "subscribe", d: { id: subscriptionID } }),
			);
			store.core.connection = "open";
			request();
		};
		socket.onmessage = (ev: MessageEvent<string>) => {
			let decoded: unknown;
			try {
				decoded = JSON.parse(ev.data);
			} catch {
				return;
			}
			if (!isEnvelope(decoded)) {
				return;
			}
			if (decoded.t === "ack" || decoded.t === "err") {
				handleResult(decoded);
				return;
			}
			if (decoded.t === "hello") {
				const h = decoded.d as { resumed?: boolean } | undefined;
				onHello?.(h?.resumed === true);
				request();
				return;
			}
			applyEnvelope(store, view, decoded);
			request();
		};
		socket.onclose = () => {
			store.core.connection = "closed";
			request();
			onClose?.();
			if (!stopped) {
				scheduleReconnect();
			}
		};
		socket.onerror = () => {
			// onclose always follows; nothing to do here.
		};
	};

	const handleResult = (env: { t: string; cid?: string; d?: unknown }): void => {
		if (env.cid === undefined) {
			return;
		}
		let result: CommandResult;
		if (env.t === "ack") {
			const r = env.d as Result | undefined;
			result = {
				accepted: r?.accepted ?? true,
				errorCode: r?.errorCode,
				errorMsg: r?.errorMsg,
			};
		} else {
			const r = env.d as { code?: string; msg?: string } | undefined;
			result = { accepted: false, errorCode: r?.code, errorMsg: r?.msg };
		}
		if (result.accepted) {
			applySendResult(store, env.cid, true);
		} else {
			applySendResult(store, env.cid, false, result.errorMsg);
		}
		onResult?.(env.cid, result);
		request();
	};

	const scheduleReconnect = (): void => {
		const delay = Math.min(1000 * 2 ** attempt, MAX_BACKOFF_MS);
		attempt += 1;
		window.setTimeout(() => {
			if (!stopped) {
				open();
			}
		}, delay);
	};

	const send = (cmd: CommandInput): string => {
		const cid = nextCID();
		if (ws === null || ws.readyState !== WebSocket.OPEN) {
			return cid;
		}
		const full: Command = { cid, ...cmd };
		ws.send(JSON.stringify({ t: "cmd", cid: full.cid, d: full }));
		return cid;
	};

	open();

	return {
		send,
		close: () => {
			stopped = true;
			ws?.close();
		},
	};
}