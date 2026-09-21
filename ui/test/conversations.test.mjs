// Headless tests for the shared conversation-kind predicates and the
// active-conversation lookup. Pure; no network or DOM.

import { test } from "bun:test";
import assert from "node:assert/strict";

import { activeConv, isChannelKind, isMemberConv, roomVisibility } from "../src/lib/conversations.js";
import { live } from "./helpers.mjs";

test("isChannelKind is true only for channels and rooms", () => {
	assert.equal(isChannelKind("official"), true);
	assert.equal(isChannelKind("room"), true);
	for (const kind of ["dm", "broadcast", "warp"]) {
		assert.equal(isChannelKind(kind), false);
	}
});

test("isMemberConv tolerates an absent conversation", () => {
	assert.equal(isMemberConv(undefined), false);
	assert.equal(isMemberConv({ conv: { kind: "dm" } }), false);
	assert.equal(isMemberConv({ conv: { kind: "room" } }), true);
});

test("activeConv resolves the active session's conversation", () => {
	const { store, view } = live();
	store.conversations["Vix"] = { "room:ADH-1": { key: "room:ADH-1" } };
	view.tabs = [{ id: "t1", session: "Vix" }];
	view.activeTab = "t1";
	view.activeConv["Vix"] = "room:ADH-1";
	assert.equal(activeConv(store, view)?.key, "room:ADH-1");
});

test("activeConv is undefined without an active session or selection", () => {
	const { store, view } = live();
	// No tab bound: activeSession is null.
	assert.equal(activeConv(store, view), undefined);
	view.tabs = [{ id: "t1", session: "Vix" }];
	view.activeTab = "t1";
	// Bound, but no conversation selected.
	assert.equal(activeConv(store, view), undefined);
});

test("roomVisibility reports official channels as public", () => {
	const { store } = live();
	assert.equal(
		roomVisibility(store, { kind: "official", id: "Frontpage" }),
		"public",
	);
	// The catalog need not have loaded: official channels are always public.
	assert.equal(store.channels.loaded, false);
});

test("roomVisibility is unknown until the open-room list loads", () => {
	const { store } = live();
	assert.equal(
		roomVisibility(store, { kind: "room", id: "ADH-1" }),
		"unknown",
	);
});

test("roomVisibility is public iff the room is in the loaded catalog", () => {
	const { store } = live();
	store.channels = {
		loaded: true,
		official: [],
		rooms: [{ name: "ADH-1", title: "Open", characters: 2 }],
	};
	assert.equal(
		roomVisibility(store, { kind: "room", id: "adh-1" }),
		"public",
		"catalog membership matches case-insensitively",
	);
	assert.equal(
		roomVisibility(store, { kind: "room", id: "ADH-2" }),
		"private",
	);
});

test("roomVisibility is unknown for non-room kinds", () => {
	const { store } = live();
	store.channels = { loaded: true, official: [], rooms: [] };
	assert.equal(roomVisibility(store, { kind: "dm", id: "Kira" }), "unknown");
});
