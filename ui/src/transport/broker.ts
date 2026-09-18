import type { CommandInput } from "./protocol.js";
import type { CommandResult } from "./ws.js";
// The request/response side of the core socket. Fire-and-forget commands go
// straight through Dispatch; the handful of commands whose result the UI waits
// on (join, login, credentials) go through a broker so no promise can outlive
// its ack.

/** DEFAULT_TIMEOUT_MS bounds an unacknowledged command. Without it a lost ack
 * would leave the caller's promise -- and the dialog awaiting it -- pending
 * forever. */
const DEFAULT_TIMEOUT_MS = 10000;

export interface CommandBroker {
	/** ask sends a command and resolves with its ack, a synthetic failure when
	 * the socket is unavailable, or a timeout. It never leaves a waiter behind. */
	ask(cmd: CommandInput, timeoutMs?: number): Promise<CommandResult>;
	/** resolve feeds a transport ack/err to its waiter, if any. */
	resolve(cid: string, result: CommandResult): void;
	/** abort fails every pending waiter (socket closed, signed out). */
	abort(reason: string): void;
}

const failure = (errorMsg: string): CommandResult => ({
	accepted: false,
	errorMsg,
});

/** createBroker correlates commands with their acks. It owns the only map of
 * pending command promises: every entry is removed by its ack, its timeout, or
 * an abort, so a reconnect or a lost ack cannot leak one. */
export function createBroker(
	send: (cmd: CommandInput) => string,
): CommandBroker {
	const waiters = new Map<string, (r: CommandResult) => void>();

	return {
		ask(cmd, timeoutMs = DEFAULT_TIMEOUT_MS) {
			const cid = send(cmd);
			if (cid === "") {
				return Promise.resolve(failure("Not connected to the core."));
			}
			return new Promise<CommandResult>((resolve) => {
				const timer = window.setTimeout(() => {
					if (waiters.delete(cid)) {
						resolve(failure("The core did not respond."));
					}
				}, timeoutMs);
				waiters.set(cid, (result) => {
					window.clearTimeout(timer);
					resolve(result);
				});
			});
		},
		resolve(cid, result) {
			const waiter = waiters.get(cid);
			if (waiter === undefined) {
				return;
			}
			waiters.delete(cid);
			waiter(result);
		},
		abort(reason) {
			const pending = [...waiters.values()];
			waiters.clear();
			for (const waiter of pending) {
				waiter(failure(reason));
			}
		},
	};
}