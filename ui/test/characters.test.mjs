import { test } from "node:test";
import assert from "node:assert/strict";

import { pushRecentDm, RECENT_DM_CAP, seenOnlineNames } from "../app/lib/characters.js";
import { rosterRank, sortRosterNames } from "../app/lib/order.js";

const character = (name, online, extra = {}) => ({
	name,
	online,
	presenceKnown: true,
	admin: false,
	...extra,
});

test("seenOnlineNames keeps only online characters, alphabetical", () => {
	const characters = {
		Zed: character("Zed", true),
		amy: character("amy", true),
		Bob: character("Bob", false),
	};
	assert.deepEqual(seenOnlineNames(characters), ["amy", "Zed"]);
});

test("seenOnlineNames drops excluded names", () => {
	const characters = {
		Vix: character("Vix", true),
		Kira: character("Kira", true),
	};
	assert.deepEqual(seenOnlineNames(characters, new Set(["Vix"])), ["Kira"]);
});

test("seenOnlineNames on an empty registry is empty", () => {
	assert.deepEqual(seenOnlineNames({}), []);
});

test("pushRecentDm appends newest last, oldest first", () => {
	let list = [];
	list = pushRecentDm(list, "Kira");
	list = pushRecentDm(list, "Vix");
	assert.deepEqual(list, ["Kira", "Vix"]);
});

test("pushRecentDm drops the oldest past the cap", () => {
	let list = [];
	for (const name of ["A", "B", "C", "D"]) {
		list = pushRecentDm(list, name);
	}
	assert.equal(list.length, RECENT_DM_CAP);
	assert.deepEqual(list, ["B", "C", "D"]);
});

test("pushRecentDm ignores a name already in the buffer", () => {
	const list = ["Kira", "Vix"];
	assert.equal(pushRecentDm(list, "Kira"), list);
});

test("pushRecentDm ignores an empty name", () => {
	const list = ["Kira"];
	assert.equal(pushRecentDm(list, ""), list);
});

test("rosterRank orders admin, op, friend, then the rest", () => {
	const admin = rosterRank({ isAdmin: true, isOp: false, isFriend: false });
	const op = rosterRank({ isAdmin: false, isOp: true, isFriend: false });
	const friend = rosterRank({ isAdmin: false, isOp: false, isFriend: true });
	const other = rosterRank({ isAdmin: false, isOp: false, isFriend: false });
	assert.ok(admin < op && op < friend && friend < other);
});

test("sortRosterNames ranks first, then breaks ties alphabetically", () => {
	const ranks = { Zed: 0, amy: 2, Bob: 3, Eve: 3 };
	assert.deepEqual(
		sortRosterNames(["Eve", "Zed", "Bob", "amy"], (n) => ranks[n]),
		["Zed", "amy", "Bob", "Eve"],
	);
});
