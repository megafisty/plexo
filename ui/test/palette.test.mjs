import { test } from "bun:test";
import assert from "node:assert/strict";

import { filterPaletteItems, matchPaletteItems, wrapActive } from "../src/components/primitives/palette.js";

const item = (id, filterable, title = filterable) => ({
	id,
	title,
	filterable,
});

const LIST = [
	item("frontpage", "Frontpage"),
	item("lounge", "The Lounge"),
	item("kira", "Kira"),
];

test("an empty query keeps every row", () => {
	assert.equal(filterPaletteItems(LIST, "").length, 3);
	assert.equal(filterPaletteItems(LIST, "   ").length, 3);
});

test("matching is case-insensitive on filterable", () => {
	assert.deepEqual(
		filterPaletteItems(LIST, "LOUNGE").map((i) => i.id),
		["lounge"],
	);
});

test("filtering ignores the title and uses filterable alone", () => {
	const rows = [item("dnd", "Do not disturb dnd", "⛔ Do not disturb")];
	assert.deepEqual(
		filterPaletteItems(rows, "dnd").map((i) => i.id),
		["dnd"],
	);
	assert.equal(filterPaletteItems(rows, "⛔").length, 0);
});

test("a non-match yields no rows", () => {
	assert.equal(filterPaletteItems(LIST, "zzz").length, 0);
});

test("matchPaletteItems caps collected rows but counts every match", () => {
	const rows = Array.from({ length: 250 }, (_, i) => item(`c${i}`, `Channel ${i}`));
	const bounded = matchPaletteItems(rows, "", 100);
	assert.equal(bounded.items.length, 100);
	assert.equal(bounded.total, 250);
});

test("matchPaletteItems counts beyond the cap under a query", () => {
	const rows = Array.from({ length: 40 }, (_, i) => item(`c${i}`, `Lounge ${i}`));
	rows.push(item("other", "Frontpage"));
	const bounded = matchPaletteItems(rows, "lounge", 10);
	assert.equal(bounded.items.length, 10);
	assert.equal(bounded.total, 40);
});

test("matchPaletteItems with limit 0 is unbounded", () => {
	const rows = Array.from({ length: 250 }, (_, i) => item(`c${i}`, `Channel ${i}`));
	const all = matchPaletteItems(rows, "", 0);
	assert.equal(all.items.length, 250);
	assert.equal(all.total, 250);
});

test("wrapActive folds an index into the row set", () => {
	assert.equal(wrapActive(1, 3), 1);
	assert.equal(wrapActive(3, 3), 0);
	assert.equal(wrapActive(-1, 3), 2);
	assert.equal(wrapActive(4, 3), 1);
});

test("wrapActive leaves a single-row set on that row", () => {
	assert.equal(wrapActive(0, 1), 0);
	assert.equal(wrapActive(1, 1), 0);
	assert.equal(wrapActive(-1, 1), 0);
});