import { test } from "node:test";
import assert from "node:assert/strict";

import { boundMatches } from "../app/lib/list.js";

test("boundMatches keeps everything under the cap", () => {
	const all = [1, 2, 3];
	assert.deepEqual(boundMatches(all, 5), { visible: all, hidden: 0 });
});

test("boundMatches trims to the cap and reports the remainder", () => {
	assert.deepEqual(boundMatches([1, 2, 3, 4, 5], 2), {
		visible: [1, 2],
		hidden: 3,
	});
});

test("a non-positive cap is uncapped", () => {
	const all = [1, 2, 3];
	assert.deepEqual(boundMatches(all, 0), { visible: all, hidden: 0 });
	assert.deepEqual(boundMatches(all, -1), { visible: all, hidden: 0 });
});

test("boundMatches on an empty list", () => {
	assert.deepEqual(boundMatches([], 3), { visible: [], hidden: 0 });
});
