// Headless tests for the shared conversation-kind predicates and the
// active-conversation lookup. Pure; no network or DOM.

import { test } from "bun:test";
import assert from "node:assert/strict";

import { activeConv, isChannelKind, isMemberConv } from "../src/lib/conversations.js";
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
