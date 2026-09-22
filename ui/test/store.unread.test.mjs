import { test } from "bun:test";
import assert from "node:assert/strict";

import { anyElevated, convSeverity, sessionSeverity } from "../src/store/unread.js";
import { conv, live } from "./helpers.mjs";

/** addConv installs one conversation into a session's map. */
function addConv(store, session, key, extra = {}) {
	const c = extra.conv ?? conv(key.split(":")[0], key.split(":")[1]);
	(store.conversations[session] ??= {})[key] = {
		key,
		conv: c,
		session,
		unread: false,
		highlight: false,
		lastActivity: 0,
		typing: {},
		materialized: false,
		...extra,
	};
}

test("convSeverity ranks a DM unread and a channel highlight as elevated", () => {
	assert.equal(
		convSeverity({ conv: conv("dm", "x"), unread: true, highlight: false }),
		"elevated",
	);
	assert.equal(
		convSeverity({ conv: conv("channel", "x"), unread: true, highlight: true }),
		"elevated",
	);
	assert.equal(
		convSeverity({ conv: conv("official", "x"), unread: true, highlight: false }),
		"unread",
	);
	assert.equal(
		convSeverity({ conv: conv("dm", "x"), unread: false, highlight: false }),
		"none",
	);
});

test("sessionSeverity takes the highest severity across a session's conversations", () => {
	const { store } = live();
	addConv(store, "Vix", "channel:rpg", { unread: true });

	assert.equal(sessionSeverity(store, "Vix"), "unread", "plain channel unread");

	addConv(store, "Vix", "dm:Kira", { conv: conv("dm", "Kira"), unread: true });
	assert.equal(sessionSeverity(store, "Vix"), "elevated", "an unread DM outranks it");
});

test("sessionSeverity is none for an unknown or empty session", () => {
	const { store } = live();
	assert.equal(sessionSeverity(store, "Vix"), "none");
	assert.equal(sessionSeverity(store, "Nobody"), "none");
});

test("a highlight in another session is visible without the active session", () => {
	const { store, view } = live();
	addConv(store, "Vix", "channel:lobby", { unread: true });
	addConv(store, "Socks", "channel:rpg", { highlight: true });

	assert.equal(sessionSeverity(store, "Vix"), "unread");
	assert.equal(sessionSeverity(store, "Socks"), "elevated");
	// The badge is independent of which session/conversation is active: it is
	// what makes a background session's elevated traffic visible at all.
	assert.ok(view.activeSession === null);
	assert.equal(anyElevated(store), true);
});

test("anyElevated ignores plain channel unread but a highlight flips it", () => {
	const { store } = live();
	addConv(store, "Vix", "channel:lobby", { unread: true });
	assert.equal(anyElevated(store), false);

	addConv(store, "Vix", "channel:rpg", { highlight: true });
	assert.equal(anyElevated(store), true);
});
