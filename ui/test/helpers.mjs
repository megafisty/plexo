// Shared builders for the headless store tests. They construct the real Store
// and View shapes (not fixtures) so the tests exercise the same objects the
// components read.
import { createStore, createView } from "../app/store/state.js";
import { ensureWindow } from "../app/store/window.js";

export const NOW = "2024-01-01T00:00:00.000Z";
export const NOW_MS = Date.parse(NOW);

export const conv = (kind, id) => ({ kind, id });

export const entry = (o = {}) => ({
	id: "e1",
	session: "Vix",
	conv: conv("dm", "Kira"),
	convSeq: 1,
	kind: "dm",
	speaker: "Vix",
	html: ": hi",
	createdAtMs: NOW_MS,
	receivedAtMs: NOW_MS,
	...o,
});

export const messageEvent = (session, c, e, o = {}) => ({
	session,
	kind: "message",
	payload: { conv: c, entry: e, self: false, ...o },
});

export const batch = (events) => ({ t: "batch", d: { events } });

/** collect returns a dispatch that records commands and returns stable cids. */
export function collect() {
	const calls = [];
	let n = 0;
	return {
		calls,
		dispatch(cmd) {
			const cid = `u-${++n}`;
			calls.push({ cid, cmd });
			return cid;
		},
	};
}

/** live returns a store and view with one live session, browser unfocused. */
export function live() {
	const store = createStore();
	const view = createView();
	view.focused = false;
	store.sessions["Vix"] = {
		character: "Vix",
		state: "live",
		self: { character: "Vix", online: true },
		adCount: 0,
		conversations: [],
	};
	return { store, view };
}

/** pendingSend installs an optimistic row and its cid mapping, mirroring
 * sendDraft, and returns the window. */
export function pendingSend(store, session, key, c, cid, entryId) {
	const win = ensureWindow(store, session, key);
	win.items.push({
		id: entryId,
		convSeq: Number.MAX_SAFE_INTEGER,
		kind: c.kind === "dm" ? "dm" : "msg",
		speaker: session,
		html: ": hi",
		time: Date.now(),
		self: true,
		send: "pending",
	});
	win.rev++;
	store.pending[cid] = { session, conv: c, entryId };
	return win;
}