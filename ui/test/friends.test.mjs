import { test } from "bun:test";
import assert from "node:assert/strict";

import { isBookmarked } from "../src/lib/friends.js";
import { setBookmark } from "../src/store/commands.js";
import { createStore } from "../src/store/state.js";

test("isBookmarked matches case-insensitively against the bookmark projection", () => {
	const store = createStore();
	store.bookmarks = [{ name: "Carol", online: true }];
	assert.equal(isBookmarked(store, "Carol"), true);
	assert.equal(isBookmarked(store, "carol"), true);
	assert.equal(isBookmarked(store, "Kira"), false);
});

test("setBookmark emits the account op with add and remove", () => {
	const calls = [];
	const dispatch = (cmd) => {
		calls.push(cmd);
		return "u-1";
	};
	setBookmark(dispatch, "Carol", true);
	setBookmark(dispatch, "Carol", false);
	assert.deepEqual(calls, [
		{ op: "set_bookmark", character: "Carol", action: "add" },
		{ op: "set_bookmark", character: "Carol", action: "remove" },
	]);
});