import { test } from "node:test";
import assert from "node:assert/strict";

import { filterPaletteItems } from "../app/components/primitives/palette.js";

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