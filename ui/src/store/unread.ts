import type { Conversation, Store } from "./state.js";
import type { View } from "./state.js";
// Client-owned unread state. The core tracks no read state: unread is derived
// here purely from focus (active session + active conversation + a focused
// browser tab), and each conversation carries a single boolean. Highlight, by
// contrast, is a real core signal for a channel message that matched a
// configured string.

/** Severity ranks a conversation's pending signals. A direct-message unread or
 * a channel highlight is elevated above a plain channel unread; plain channel
 * unread is deliberately not promoted to the global marker. */
export type Severity = "none" | "unread" | "elevated";

/** isConvFocused is true only when the conversation is the active one in the
 * active session AND the browser tab itself has focus. Reading requires both:
 * a blurred tab has not actually been seen. */
export function isConvFocused(view: View, session: string, key: string): boolean {
	return (
		view.focused &&
		view.activeSession === session &&
		view.activeConv[session] === key
	);
}

/** convSeverity derives the display severity of one conversation. */
export function convSeverity(conv: Conversation): Severity {
	if (conv.highlight || (conv.conv.kind === "dm" && conv.unread)) {
		return "elevated";
	}
	return conv.unread ? "unread" : "none";
}

/** sessionSeverity derives the display severity of one session: the highest
 * severity across its conversations. It is what the session tab shows, since a
 * background session's summary events still set unread/highlight even without
 * full interest. An unknown or empty session is "none". */
export function sessionSeverity(store: Store, session: string): Severity {
	const per = store.conversations[session];
	if (per === undefined) {
		return "none";
	}
	let worst: Severity = "none";
	for (const conv of Object.values(per)) {
		const severity = convSeverity(conv);
		if (severity === "elevated") {
			return "elevated";
		}
		if (severity === "unread") {
			worst = "unread";
		}
	}
	return worst;
}

/** anyElevated reports whether any conversation needs the prominent global
 * marker (the document-title bubble): an unread DM or a highlight anywhere. */
export function anyElevated(store: Store): boolean {
	for (const session of Object.keys(store.conversations)) {
		if (sessionSeverity(store, session) === "elevated") {
			return true;
		}
	}
	return false;
}
