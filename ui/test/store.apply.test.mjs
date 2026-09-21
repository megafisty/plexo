import { test } from "bun:test";
import assert from "node:assert/strict";

import { applyEnvelope, applySendResult } from "../src/store/apply.js";
import { unionFriends } from "../src/lib/friends.js";
import { applyPresence, closeCommand, closeModal, dialogOpen, openCommand, openModal, toggleModal, togglePopout } from "../src/store/state.js";
import { activateConv } from "../src/store/commands.js";
import { ensureWindow, insertLive } from "../src/store/window.js";
import { batch, collect, conv, entry, live, messageEvent, NOW, pendingSend } from "./helpers.mjs";

// --- optimistic send correlation (#4) ---

test("a successful ack keeps the cid mapping until the self echo retires the exact row", () => {
	const { store, view } = live();
	const c = conv("dm", "Kira");
	const win = pendingSend(store, "Vix", "dm:Kira", c, "u-1", "pending-u-1");

	applySendResult(store, "u-1", true);
	assert.equal(win.items[0].send, "sent");
	assert.ok(store.pending["u-1"], "a successful ack keeps the mapping for the echo");

	applyEnvelope(
		store,
		view,
		batch([
			messageEvent("Vix", c, entry({ id: "e1", convSeq: 42 }), {
				self: true,
				cid: "u-1",
			}),
		]),
	);

	assert.equal(store.pending["u-1"], undefined);
	assert.deepEqual(
		win.items.map((e) => e.id),
		["e1"],
		"the optimistic row is replaced by the canonical one",
	);
});

test("a failed send marks the row and clears the cid mapping", () => {
	const { store } = live();
	const c = conv("dm", "Kira");
	const win = pendingSend(store, "Vix", "dm:Kira", c, "u-1", "pending-u-1");

	applySendResult(store, "u-1", false, "too_long");
	assert.equal(win.items[0].send, "failed");
	assert.equal(win.items[0].error, "too_long");
	assert.equal(store.pending["u-1"], undefined, "no echo is coming");
});

test("a cid-less self message does not retire a pending send", () => {
	const { store, view } = live();
	const c = conv("dm", "Kira");
	const win = pendingSend(store, "Vix", "dm:Kira", c, "u-1", "pending-u-1");

	// Our character active on another connection: a self message with no cid.
	applyEnvelope(
		store,
		view,
		batch([
			messageEvent("Vix", c, entry({ id: "foreign-1", convSeq: 7 }), {
				self: true,
			}),
		]),
	);

	assert.ok(store.pending["u-1"], "the in-flight send is untouched");
	const ids = win.items.map((e) => e.id);
	assert.ok(ids.includes("pending-u-1"), "the optimistic row survives");
	assert.ok(ids.includes("foreign-1"), "the foreign self message is appended");
	assert.equal(ids.length, 2);
});

// --- modal shortcut gating (#5) ---

test("dialogOpen covers the warpmark modal", () => {
	const { view } = live();
	assert.equal(dialogOpen(view), false);
	openModal(view, {
		kind: "warpmark",
		session: "Vix",
		entryId: "e1",
		speaker: "Vix",
		existing: false,
		label: "",
	});
	assert.equal(dialogOpen(view), true);
});

// --- the shell's overlay slots ---

test("the modal slot holds one dialog and a popout is not modal", () => {
	const { view } = live();
	assert.equal(dialogOpen(view), false);

	// A dialog blind to its own kind (sidebar join/status, timeline warpmark) is
	// installed directly; a top-bar toggle replaces whatever is open.
	openModal(view, { kind: "join" });
	assert.equal(dialogOpen(view), true);
	toggleModal(view, "search");
	assert.equal(view.modal?.kind, "search", "opening a modal replaces the previous one");
	toggleModal(view, "search");
	assert.equal(view.modal, null, "toggling the open modal closes it");

	togglePopout(view, "friends");
	assert.equal(view.popout, "friends");
	assert.equal(dialogOpen(view), false, "a popout leaves keyboard navigation live");
	togglePopout(view, "warpmarks");
	assert.equal(view.popout, "warpmarks", "the popout slot holds one at a time");
	togglePopout(view, "warpmarks");
	assert.equal(view.popout, null);
});

// --- the command-palette slot layers over dialogs ---

test("a format palette mounts over an open dialog and keeps it", () => {
	const { view } = live();
	openModal(view, { kind: "status" });

	// A global palette is refused while a dialog owns the screen.
	openCommand(view, "main");
	assert.equal(view.palette, null, "a non-format palette cannot layer over a dialog");
	assert.equal(view.modal?.kind, "status", "the dialog is untouched");

	// A format palette (one carrying an apply closure) is the exception.
	const apply = () => {};
	openCommand(view, "format-marks", apply, "selected");
	assert.equal(view.palette?.command, "format-marks");
	assert.equal(view.palette?.onFormat, apply);
	assert.equal(view.palette?.selection, "selected");
	assert.equal(view.modal?.kind, "status", "the dialog stays mounted below");
	assert.equal(dialogOpen(view), true, "the palette captures global input");

	closeCommand(view);
	assert.equal(view.palette, null);
	assert.equal(view.modal?.kind, "status", "closing the palette leaves the dialog open");
});

test("a global palette opens when no dialog is present", () => {
	const { view } = live();
	openCommand(view, "main");
	assert.equal(view.palette?.command, "main");
	assert.equal(dialogOpen(view), true);

	// Opening a modal drops a palette the backdrop would hide, and closeModal
	// clears the pair so an apply closure cannot outlive its dialog.
	openModal(view, { kind: "join" });
	assert.equal(view.palette, null);
	openCommand(view, "format-marks", () => {});
	closeModal(view);
	assert.equal(view.modal, null);
	assert.equal(view.palette, null);
});

// --- session loss reconciles tabs (#7) ---

test("session_lost unbinds the tab and drops its active/pending conversation", () => {
	const { store, view } = live();
	view.tabs = [{ id: "tab-1", session: "Vix" }];
	view.activeTab = "tab-1";
	view.activeConv = { Vix: "official:Frontpage" };
	view.pendingConv = { Vix: "official:Join" };

	applyEnvelope(store, view, batch([
		{ session: "Vix", kind: "state", payload: { key: "session/Vix", removed: true } },
	]));

	assert.equal(store.sessions.Vix, undefined);
	assert.equal(view.tabs[0].session, null, "the tab falls back to the picker");
	assert.equal(view.activeConv.Vix, undefined);
	assert.equal(view.pendingConv.Vix, undefined);
});

// --- reference-equality guarantees the pure/memo checks rely on (#8) ---

test("applyPresence replaces the character record wholesale", () => {
	const { store } = live();
	store.characters.Kira = {
		name: "Kira",
		online: true,
		presenceKnown: true,
	};
	const before = store.characters.Kira;

	applyPresence(store, { character: "Kira", online: false }, true);

	assert.notEqual(store.characters.Kira, before, "a changed presence is a new record");
	assert.equal(store.characters.Kira.online, false);
});

test("applyConv replaces the members array", () => {
	const { store, view } = live();
	const c = conv("official", "Frontpage");
	const convEvent = (members) => ({
		session: "Vix",
		kind: "state",
		payload: { key: `conv/Vix/${c.kind}:${c.id}`, value: { members, ops: [] } },
	});

	applyEnvelope(store, view, batch([convEvent(["a"])]));
	const first = store.conversations.Vix["official:Frontpage"].members;

	applyEnvelope(store, view, batch([convEvent(["a", "b"])]));
	const second = store.conversations.Vix["official:Frontpage"].members;

	assert.notEqual(first, second, "a membership change is a new array for the memo key");
	assert.deepEqual(second, ["a", "b"]);
});

// --- identity skips (resync/repeat payloads) ---

test("an unchanged presence payload keeps the existing record", () => {
	const { store } = live();
	const payload = {
		character: "Kira",
		gender: "Female",
		status: "online",
		statusMsg: "",
		admin: false,
		online: true,
	};
	applyPresence(store, payload, true);
	const before = store.characters.Kira;

	applyPresence(store, { ...payload }, true);

	assert.equal(store.characters.Kira, before, "an identical resync does not replace the row");
});

test("an unchanged conv member set keeps the existing array", () => {
	const { store, view } = live();
	const c = conv("official", "Frontpage");
	const convEvent = (members) => ({
		kind: "state",
		payload: { key: `conv/Vix/${c.kind}:${c.id}`, value: { members, ops: [] } },
	});

	applyEnvelope(store, view, batch([convEvent(["a", "b"])]));
	const first = store.conversations.Vix["official:Frontpage"].members;

	applyEnvelope(store, view, batch([convEvent(["a", "b"])]));

	assert.equal(store.conversations.Vix["official:Frontpage"].members, first);
});

// --- room role (T2) ---

test("a conv state record applies the session's room role set-to", () => {
	const { store, view } = live();
	const c = conv("room", "ADH-abc");
	const convEvent = (role) => ({
		kind: "state",
		payload: { key: `conv/Vix/${c.kind}:${c.id}`, value: { ops: [], role } },
	});

	applyEnvelope(store, view, batch([convEvent("owner")]));
	assert.equal(store.conversations.Vix["room:ADH-abc"].role, "owner");

	applyEnvelope(store, view, batch([convEvent("none")]));
	assert.equal(store.conversations.Vix["room:ADH-abc"].role, "none");
});

test("a conv_view applies the session's room role", () => {
	const { store, view } = live();
	const c = conv("room", "ADH-abc");

	applyEnvelope(
		store,
		view,
		batch([
			{
				kind: "conv_view",
				payload: {
					session: "Vix",
					conv: c,
					window: [],
					cursor: { asOfSeq: 0, oldestSeq: 0, hasOlder: false },
					role: "mod",
				},
			},
		]),
	);

	assert.equal(store.conversations.Vix["room:ADH-abc"].role, "mod");
});

test("a full conv_view seeds the room op list", () => {
	const { store, view } = live();
	const c = conv("room", "ADH-abc");

	applyEnvelope(
		store,
		view,
		batch([
			{
				kind: "conv_view",
				payload: {
					session: "Vix",
					conv: c,
					members: [],
					ops: ["Kira", "Sam"],
					window: [],
					cursor: { asOfSeq: 0, oldestSeq: 0, hasOlder: false },
				},
			},
		]),
	);

	assert.deepEqual(store.conversations.Vix["room:ADH-abc"].ops, ["Kira", "Sam"]);
});

test("the snapshot seeds a conversation's room role", () => {
	const { store, view } = live();
	applyEnvelope(store, view, {
		t: "snapshot",
		d: {
			sessions: [
				{
					character: "Vix",
					state: "live",
					self: { character: "Vix", online: true, admin: false },
					adCount: 0,
					conversations: [
						{
							conv: conv("room", "ADH-abc"),
							kind: "room",
							title: "Test room",
							lastActivity: NOW,
							role: "owner",
						},
					],
				},
			],
		},
	});

	assert.equal(store.conversations.Vix["room:ADH-abc"].role, "owner");
});

// --- account sets carried once (T2) ---

test("the snapshot applies account-wide friends, bookmarks, and ignores", () => {
	const { store, view } = live();
	applyEnvelope(store, view, {
		t: "snapshot",
		d: {
			sessions: [
				{
					character: "Vix",
					state: "live",
					self: { character: "Vix", online: true, admin: false },
					adCount: 0,
					conversations: [],
				},
			],
			friends: [{ name: "Kira", online: true, admin: false }],
			bookmarks: [{ name: "Neko", online: true, admin: false }],
			ignores: ["Spammer"],
			catalog: { official: [], rooms: [] },
		},
	});
	assert.deepEqual(store.friends.map((f) => f.name), ["Kira"]);
	assert.deepEqual(store.bookmarks.map((f) => f.name), ["Neko"]);
	assert.deepEqual(
		unionFriends(store).map((f) => f.name),
		["Kira", "Neko"],
	);
	assert.deepEqual(store.ignores, ["Spammer"]);
});

test("a friends delta keeps a both-character in both lists and the union deduplicates", () => {
	const { store, view } = live();
	applyEnvelope(
		store,
		view,
		batch([
			{
				kind: "state",
				payload: {
					key: "account/friends",
					value: {
						friends: [{ name: "Both", online: true, admin: false }],
						bookmarks: [
							{ name: "Both", online: true, admin: false },
							{ name: "Carol", online: true, admin: false },
						],
					},
				},
			},
		]),
	);
	assert.deepEqual(store.friends.map((f) => f.name), ["Both"]);
	assert.deepEqual(store.bookmarks.map((f) => f.name), ["Both", "Carol"]);
	assert.deepEqual(
		unionFriends(store).map((f) => f.name),
		["Both", "Carol"],
	);
	assert.equal(
		unionFriends(store),
		unionFriends(store),
		"the union is memoized on the two list references",
	);
});

// --- delta re-entry (T3) ---

test("a delta conv_view merges into the retained window", () => {
	const { store, view } = live();
	const c = conv("official", "Frontpage");
	const key = "official:Frontpage";
	store.conversations.Vix = {};
	store.conversations.Vix[key] = {
		key,
		conv: c,
		session: "Vix",
		unread: false,
		highlight: false,
		lastActivity: 0,
		typing: {},
		materialized: true,
	};
	const win = ensureWindow(store, "Vix", key);
	insertLive(win, { id: "e1", convSeq: 1, kind: "msg", speaker: "X", html: "1", time: 1 }, true);
	insertLive(win, { id: "e2", convSeq: 2, kind: "msg", speaker: "X", html: "2", time: 2 }, true);
	win.hasOlder = true;

	applyEnvelope(
		store,
		view,
		batch([
			{
				kind: "conv_view",
				payload: {
					session: "Vix",
					conv: c,
					window: [
						{
							id: "e3",
							session: "Vix",
							conv: c,
							convSeq: 3,
							kind: "msg",
							speaker: "X",
							html: "3",
							createdAtMs: 3,
							receivedAtMs: 3,
						},
					],
					cursor: { asOfSeq: 3, oldestSeq: 0, hasOlder: false },
					delta: true,
				},
			},
		]),
	);

	const after = store.entries.Vix[key];
	assert.deepEqual(after.items.map((e) => e.id), ["e1", "e2", "e3"]);
	assert.equal(after.hasOlder, true, "a delta preserves retained older history");
	assert.equal(after.hasNewer, false);
	assert.equal(after.liveSeq, 3);
});

test("re-activating a conversation retains its window and asks for a delta", () => {
	const { store, view } = live();
	store.conversations.Vix = {};
	for (const c of [conv("official", "Frontpage"), conv("dm", "Kira")]) {
		const key = `${c.kind}:${c.id}`;
		store.conversations.Vix[key] = {
			key,
			conv: c,
			session: "Vix",
			unread: false,
			highlight: false,
			lastActivity: 0,
			typing: {},
			materialized: true,
		};
	}
	append(ensureWindow(store, "Vix", "official:Frontpage"), 10);
	append(ensureWindow(store, "Vix", "dm:Kira"), 5);

	const { calls, dispatch } = collect();
	activateConv(store, view, dispatch, "Vix", "official:Frontpage");
	activateConv(store, view, dispatch, "Vix", "dm:Kira");
	assert.ok(store.entries.Vix["official:Frontpage"], "the released window is retained");
	activateConv(store, view, dispatch, "Vix", "official:Frontpage");

	const last = calls[calls.length - 1].cmd;
	assert.equal(last.op, "set_interest");
	assert.equal(last.level, "full");
	assert.equal(last.since, 10, "re-entry resumes from the retained window's newest seq");
});

// --- sparse conversation description ---

/** convState is a set-to conversation state record under the given key. */
const convState = (session, key, value) => ({
	session,
	kind: "state",
	payload: { key, value },
});

test("a conversation description is cleared by an explicit empty value", () => {
	const { store, view } = live();
	const key = "conv/Vix/dm:Kira";

	applyEnvelope(
		store,
		view,
		batch([convState("Vix", key, { description: "<b>hi</b>", ops: [] })]),
	);
	assert.equal(store.conversations.Vix["dm:Kira"].description, "<b>hi</b>");

	applyEnvelope(
		store,
		view,
		batch([convState("Vix", key, { description: "", ops: [] })]),
	);
	assert.equal(
		store.conversations.Vix["dm:Kira"].description,
		"",
		"an empty description is a clear, not a keep",
	);
});

test("an omitted conversation description leaves the client's copy intact", () => {
	const { store, view } = live();
	const key = "conv/Vix/dm:Kira";

	applyEnvelope(
		store,
		view,
		batch([convState("Vix", key, { description: "<b>hi</b>", ops: [] })]),
	);
	// A later update with no description key is the sparse "unchanged" signal.
	applyEnvelope(
		store,
		view,
		batch([convState("Vix", key, { members: ["Vix"], ops: [] })]),
	);
	assert.equal(store.conversations.Vix["dm:Kira"].description, "<b>hi</b>");
});

/** append adds one real entry at convSeq seq. */
function append(win, seq) {
	insertLive(
		win,
		{ id: `e${seq}`, convSeq: seq, kind: "msg", speaker: "X", html: "x", time: seq },
		true,
	);
}