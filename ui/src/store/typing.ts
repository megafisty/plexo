// Outbound typing signals for private messages.
//
// F-Chat's TPN is private-message-only, so only DM composers emit one. Sends
// are edge-triggered, mirroring Horizon: a burst collapses to a single
// "typing", a "paused" once the keyboard has been idle for PAUSE_MS, and a
// "clear" when the box is emptied. There is no periodic re-send — receiving
// clients do not age the indicator out, so keep-alives would only be churn.
// Sending the message emits nothing (the PRI itself ends typing).
import type { Dispatch } from "../context.js";
import { OPS, type ConvRef } from "../transport/protocol.js";

/** PAUSE_MS is the idle slack before "paused": a keypress keeps the character
 * "typing" for this long. Matches Horizon's 5 s. */
const PAUSE_MS = 5000;

type Status = "typing" | "paused" | "clear";

/** TypingTarget is the active DM composer we are signalling for. */
export interface TypingTarget {
	session: string;
	/** key is the conversation key, e.g. "dm:Kira". */
	key: string;
	conv: ConvRef;
	dispatch: Dispatch;
}

interface State {
	status: Status; // last status sent; "clear" at rest
	timer?: number;
}

const states = new Map<string, State>();

function keyOf(t: TypingTarget): string {
	return t.session + "\x00" + t.key;
}

function send(t: TypingTarget, st: State, status: Status): void {
	st.status = status;
	t.dispatch({
		op: OPS.sendTyping,
		session: t.session,
		conv: t.conv,
		status,
	});
}

function armPause(t: TypingTarget, st: State): void {
	if (st.timer !== undefined) {
		window.clearTimeout(st.timer);
	}
	st.timer = window.setTimeout(() => {
		st.timer = undefined;
		if (st.status === "typing") {
			send(t, st, "paused");
		}
	}, PAUSE_MS);
}

function clearTimer(st: State): void {
	if (st.timer !== undefined) {
		window.clearTimeout(st.timer);
		st.timer = undefined;
	}
}

function toClear(t: TypingTarget, st: State): void {
	clearTimer(st);
	if (st.status !== "clear") {
		send(t, st, "clear");
	}
}

/** noteInput records a keystroke (or other text edit) in the active DM. */
export function noteInput(t: TypingTarget, text: string): void {
	if (t.conv.kind !== "dm") {
		return;
	}
	const id = keyOf(t);
	let st = states.get(id);
	if (st === undefined) {
		st = { status: "clear" };
		states.set(id, st);
	}
	if (text.trim() === "") {
		toClear(t, st);
		return;
	}
	if (st.status !== "typing") {
		send(t, st, "typing");
	}
	armPause(t, st);
}

/** noteSent retires the signal after the message is sent. It does not emit a
 * "clear": the protocol assumes a delivered PRI ends typing, and the peer
 * clears on the message itself. */
export function noteSent(t: TypingTarget): void {
	if (t.conv.kind !== "dm") {
		return;
	}
	const id = keyOf(t);
	const st = states.get(id);
	if (st === undefined) {
		return;
	}
	clearTimer(st);
	states.delete(id);
}

/** forget drops all state for a session, cancelling its timers. Wired to logout
 * so a pending timer cannot signal a session that no longer exists. */
export function forget(session: string): void {
	for (const [id, st] of states) {
		if (id.startsWith(session + "\x00")) {
			clearTimer(st);
			states.delete(id);
		}
	}
}