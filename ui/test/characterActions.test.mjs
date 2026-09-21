// Headless tests for the shared per-character action list. They exercise the
// visibility gating (Open DM, bookmark direction, moderator actions) and that
// the leaf action reads the character from the forwarded previous-context item.
// No DOM or network is involved.

import { test } from "bun:test";
import assert from "node:assert/strict";

import {
	CharacterActionsList,
	characterActionPrevious,
} from "../src/components/commands/characterActions.js";
import { live } from "./helpers.mjs";

/** ids lists the row ids a list materializes. */
const ids = (rows) => rows.map((r) => r.id);

/** call materializes CharacterActionsList for one target and context. */
function call(store, currentConv, target) {
	return CharacterActionsList.list(
		{
			store,
			view: null,
			dispatch: () => {},
			actions: {},
			session: "Vix",
			currentConv,
		},
		characterActionPrevious(target),
	);
}

/** room is a live channel conversation the moderation model can act in. */
const room = {
	conv: { kind: "room", id: "room1" },
	session: "Vix",
	members: ["Vix", "Kira"],
	ops: ["Vix"],
	role: "mod",
};

test("a DM target omits Open DM but offers Open Profile and Bookmark", () => {
	const { store } = live();
	const rows = call(
		store,
		{ conv: { kind: "dm", id: "Kira" }, session: "Vix" },
		{ name: "Kira", source: "dm" },
	);
	assert.deepEqual(ids(rows), ["open-profile", "bookmark"]);
});

test("a target outside that DM still offers Open DM", () => {
	const { store } = live();
	const rows = call(store, undefined, { name: "Kira", source: "seen" });
	assert.deepEqual(ids(rows), ["open-dm", "open-profile", "bookmark"]);
});

test("a bookmarked target offers Unbookmark instead of Bookmark", () => {
	const { store } = live();
	store.bookmarks = [{ name: "Kira" }];
	const rows = call(store, undefined, { name: "Kira", source: "seen" });
	assert.deepEqual(ids(rows), ["open-dm", "open-profile", "unbookmark"]);
});

test("the friends list only unbookmarks a bookmark-only contact", () => {
	const { store } = live();
	store.friends = [{ name: "Friend" }];
	store.bookmarks = [{ name: "Friend" }, { name: "OnlyBookmark" }];
	assert.deepEqual(
		ids(call(store, undefined, { name: "OnlyBookmark", source: "friends" })),
		["open-dm", "open-profile", "unbookmark"],
	);
	// A friend who is also bookmarked gets no bookmark affordance here.
	assert.deepEqual(
		ids(call(store, undefined, { name: "Friend", source: "friends" })),
		["open-dm", "open-profile"],
	);
});

test("moderator actions appear only from a channel roster", () => {
	const { store } = live();
	assert.ok(
		!ids(call(store, room, { name: "Kira", source: "seen" })).includes(
			"moderator-actions",
		),
	);
	assert.ok(
		ids(call(store, room, { name: "Kira", source: "roster" })).includes(
			"moderator-actions",
		),
	);
});

test("a leaf action reads the character from the previous item", () => {
	const { store } = live();
	const calls = [];
	const context = {
		store,
		view: null,
		dispatch: (cmd) => calls.push(cmd),
		actions: {},
		session: "Vix",
		currentConv: undefined,
	};
	const previous = characterActionPrevious({ name: "Kira", source: "seen" });
	const row = CharacterActionsList.list(context, previous).find(
		(r) => r.id === "bookmark",
	);
	CharacterActionsList.onSelect(row, context, previous);
	assert.deepEqual(calls, [
		{ op: "set_bookmark", character: "Kira", action: "add" },
	]);
});
