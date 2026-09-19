import type { Dispatch } from "../context.js";
import { OPS, parseConvKey } from "../transport/protocol.js";
import type { Store } from "./state.js";
import type { View } from "./state.js";
import { INVITES_KEY } from "./state.js";
// Re-asserting live-delivery interest on (re)connect, and making sure a
// snapshot-selected conversation ever gets full interest.
//
// Interest lives on the server-side broker subscription, keyed by the
// Transport's subscribe id. It survives a socket blip (the core holds it for a
// grace window) but not a page reload or a grace expiry, which start a fresh
// subscription at the default (`summary`). Re-asserting `full` on every open is
// therefore idempotent on a resume and necessary on a fresh one; without it the
// active conversation silently stops receiving messages, typing, and member
// presence until the user switches panes.

/** resubscribeActive dispatches `set_interest: full` for every session's active
 * conversation. It is idempotent and a no-op before the first snapshot (no
 * conversation is selected yet), so it is safe to call on every socket open. A
 * warp pane is HTTP-seeded and never dispatches interest, so it is skipped. */
export function resubscribeActive(
	store: Store,
	view: View,
	dispatch: Dispatch,
): void {
	for (const session of Object.keys(store.sessions)) {
		const key = view.activeConv[session];
		if (key === undefined) {
			continue;
		}
		// The invites pane is client-only; it never dispatches interest.
		if (key === INVITES_KEY) {
			continue;
		}
		const conv = parseConvKey(key);
		if (conv.kind === "warp") {
			continue;
		}
		// A retained window (grace-expiry resume) asks for a delta; a fresh page
		// load has no window and gets the full materialization.
		const win = store.entries[session]?.[key];
		const since = win?.newestSeq ?? win?.liveSeq;
		dispatch({
			op: OPS.setInterest,
			session,
			conv,
			level: "full",
			...(since !== undefined ? { since } : {}),
		});
		const record = store.conversations[session]?.[key];
		if (record !== undefined) {
			record.interestAsked = true;
		}
	}
}

/** ensureActiveInterest requests full interest for a session's active
 * conversation when it has no window yet. A conversation selected by the
 * snapshot's `pickActive` never went through `activateConv`, so the session
 * view calls this during render. The conversation's `interestAsked` flag makes
 * it a no-op after the first call, so a redraw-heavy pane does not flood the
 * core with duplicate commands while the conv_view is in flight. A warp pane
 * is HTTP-seeded and never materialized, so it is skipped. */
export function ensureActiveInterest(
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
): void {
	const key = view.activeConv[session];
	if (key === undefined) {
		return;
	}
	// The invites pane is client-only; it never materializes from the core.
	if (key === INVITES_KEY) {
		return;
	}
	const conv = parseConvKey(key);
	if (conv.kind === "warp") {
		return;
	}
	const record = store.conversations[session]?.[key];
	if (
		record === undefined ||
		record.interestAsked === true ||
		store.entries[session]?.[key] !== undefined
	) {
		return;
	}
	record.interestAsked = true;
	dispatch({ op: OPS.setInterest, session, conv, level: "full" });
}