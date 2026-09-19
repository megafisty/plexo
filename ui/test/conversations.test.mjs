import { test } from "node:test";
import assert from "node:assert/strict";

import { filterConversations } from "../app/components/commands/conversations.js";

const conversation = (kind, id, title) => ({
	key: `${kind}:${id}`,
	conv: { kind, id },
	session: "Vix",
	title,
	unread: false,
	highlight: false,
	lastActivity: 0,
	typing: {},
	materialized: true,
});

const LIST = [
	conversation("official", "Frontpage", "Frontpage"),
	conversation("room", "the_lounge", "The Lounge"),
	conversation("dm", "Kira", undefined),
];

test("an empty query keeps every conversation", () => {
	assert.equal(filterConversations(LIST, "").length, 3);
	assert.equal(filterConversations(LIST, "   ").length, 3);
});

test("matching is case-insensitive on the displayed title", () => {
	assert.deepEqual(
		filterConversations(LIST, "LOUNGE").map((c) => c.key),
		["room:the_lounge"],
	);
});

test("a conversation with no title matches on its id", () => {
	assert.deepEqual(
		filterConversations(LIST, "kira").map((c) => c.key),
		["dm:Kira"],
	);
});

test("a non-match yields no rows", () => {
	assert.equal(filterConversations(LIST, "zzz").length, 0);
});