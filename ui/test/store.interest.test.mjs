import { test } from "node:test";
import assert from "node:assert/strict";

import { activateConv } from "../app/store/commands.js";
import { ensureActiveInterest, resubscribeActive } from "../app/store/interest.js";
import { OPS } from "../app/transport/protocol.js";
import { collect, conv, live } from "./helpers.mjs";

const frontpage = (extra = {}) => ({
	key: "official:Frontpage",
	conv: conv("official", "Frontpage"),
	session: "Vix",
	unread: false,
	highlight: false,
	lastActivity: 0,
	typing: {},
	materialized: false,
	...extra,
});

test("resubscribeActive re-asserts full interest for each active non-warp conversation", () => {
	const { store, view } = live();
	store.sessions.Socks = {};
	store.sessions.Ghost = {};
	view.activeConv = {
		Vix: "official:Frontpage",
		Socks: "warp:e1", // read-only pane: HTTP-seeded, no interest
		// Ghost has no active conversation
	};

	const { calls, dispatch } = collect();
	resubscribeActive(store, view, dispatch);

	assert.equal(calls.length, 1, "only the real active conversation is resubscribed");
	assert.equal(calls[0].cmd.op, OPS.setInterest);
	assert.equal(calls[0].cmd.session, "Vix");
	assert.deepEqual(calls[0].cmd.conv, { kind: "official", id: "Frontpage" });
	assert.equal(calls[0].cmd.level, "full");
});

test("resubscribeActive re-asserts even a conversation already marked asked", () => {
	const { store, view } = live();
	view.activeConv = { Vix: "official:Frontpage" };
	store.conversations.Vix = { "official:Frontpage": frontpage({ interestAsked: true }) };

	const { calls, dispatch } = collect();
	resubscribeActive(store, view, dispatch);

	assert.equal(calls.length, 1, "a new socket subscription must re-assert regardless");
});

test("ensureActiveInterest asks once until the window materializes", () => {
	const { store, view } = live();
	view.activeConv = { Vix: "official:Frontpage" };
	store.conversations.Vix = { "official:Frontpage": frontpage() };

	const { calls, dispatch } = collect();
	ensureActiveInterest(store, view, dispatch, "Vix");
	ensureActiveInterest(store, view, dispatch, "Vix");
	assert.equal(calls.length, 1, "a redraw does not re-dispatch while the window is in flight");
	assert.equal(calls[0].cmd.level, "full");
	assert.deepEqual(calls[0].cmd.conv, { kind: "official", id: "Frontpage" });

	store.entries.Vix = {
		"official:Frontpage": { items: [], hasOlder: false, hasNewer: false, rev: 0 },
	};
	ensureActiveInterest(store, view, dispatch, "Vix");
	assert.equal(calls.length, 1, "a materialized window is not re-asked");
});

test("activateConv asks once, so ensureActiveInterest does not double-ask", () => {
	const { store, view } = live();
	store.conversations.Vix = {
		"dm:Kira": {
			key: "dm:Kira",
			conv: conv("dm", "Kira"),
			session: "Vix",
			unread: false,
			highlight: false,
			lastActivity: 0,
			typing: {},
			materialized: false,
		},
	};
	const { calls, dispatch } = collect();

	activateConv(store, view, dispatch, "Vix", "dm:Kira");
	const afterActivate = calls.length;
	assert.ok(afterActivate >= 1, "activateConv requested interest");

	ensureActiveInterest(store, view, dispatch, "Vix");
	assert.equal(calls.length, afterActivate, "the render guard sees interest already asked");
});
test("resubscribeActive resumes a retained window with a delta cursor", () => {
	const { store, view } = live();
	view.activeConv = { Vix: "official:Frontpage" };
	store.conversations.Vix = { "official:Frontpage": frontpage() };
	store.entries.Vix = {
		"official:Frontpage": {
			items: [],
			oldestSeq: 5,
			newestSeq: 42,
			hasOlder: true,
			hasNewer: false,
			liveSeq: 42,
			rev: 0,
		},
	};

	const { calls, dispatch } = collect();
	resubscribeActive(store, view, dispatch);

	assert.equal(calls[0].cmd.since, 42, "the retained window's newest seq is the delta base");
});
