import { test } from "node:test";
import assert from "node:assert/strict";

import { createStore, WINDOW } from "../app/store/state.js";
import { ensureWindow, insertLive, toEntry } from "../app/store/window.js";

const row = (convSeq) => ({
	id: `e${convSeq}`,
	convSeq,
	kind: "msg",
	speaker: "Other",
	html: "hi",
	time: convSeq,
});

test("toEntry maps a wire entry and defaults self to false", () => {
	const wire = {
		id: "e1",
		convSeq: 5,
		kind: "dm",
		speaker: "Kira",
		html: ": hi",
		createdAtMs: Date.parse("2024-01-01T00:00:00.000Z"),
	};
	assert.deepEqual(toEntry(wire), {
		id: "e1",
		convSeq: 5,
		kind: "dm",
		speaker: "Kira",
		html: ": hi",
		time: Date.parse("2024-01-01T00:00:00.000Z"),
		self: false,
	});
	assert.equal(toEntry(wire, true).self, true);
});

test("insertLive trims the edge opposite the viewport and tracks the live edge", () => {
	const store = createStore();

	const pinned = ensureWindow(store, "Vix", "official:Frontpage");
	for (let i = 1; i <= WINDOW + 1; i++) {
		insertLive(pinned, row(i), true);
	}
	assert.equal(pinned.items.length, WINDOW);
	assert.equal(pinned.items[0].convSeq, 2, "reading at the bottom drops the oldest");
	assert.equal(pinned.items[WINDOW - 1].convSeq, WINDOW + 1);
	assert.equal(pinned.hasOlder, true);
	assert.equal(pinned.liveSeq, WINDOW + 1);

	const detached = ensureWindow(store, "Vix", "dm:Kira");
	for (let i = 1; i <= WINDOW + 3; i++) {
		insertLive(detached, row(i), false);
	}
	assert.equal(detached.items.length, WINDOW);
	assert.equal(detached.items[0].convSeq, 1, "scrolled up keeps the oldest history");
	assert.equal(detached.items[WINDOW - 1].convSeq, WINDOW);
	assert.equal(detached.hasNewer, true);
	assert.equal(detached.liveSeq, WINDOW + 3);
});