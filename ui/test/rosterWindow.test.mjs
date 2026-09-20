import { test } from "bun:test";
import assert from "node:assert/strict";

import {
	DEFAULT_STRIDE,
	VIRTUAL_MIN,
	rosterStride,
	rosterWindow,
} from "../src/components/presence/rosterWindow.js";

test("a roster at or below the virtualization threshold is not windowed", () => {
	assert.deepEqual(rosterWindow(0, 0, 28, 280), {
		start: 0,
		end: 0,
		virtual: false,
	});
	assert.deepEqual(rosterWindow(50, 5000, 28, 280), {
		start: 0,
		end: 50,
		virtual: false,
	});
	assert.deepEqual(rosterWindow(VIRTUAL_MIN, 5000, 28, 280), {
		start: 0,
		end: VIRTUAL_MIN,
		virtual: false,
	});
});

test("one past the threshold starts windowing", () => {
	const range = rosterWindow(VIRTUAL_MIN + 1, 0, 28, 280);
	assert.equal(range.virtual, true);
	assert.equal(range.start, 0);
	assert.ok(range.end <= VIRTUAL_MIN + 1);
});

test("the window covers the viewport plus overscan at the top", () => {
	// stride 28, viewport 280 -> 10 rows + 1, plus 8 overscan each side.
	assert.deepEqual(rosterWindow(1000, 0, 28, 280), {
		start: 0,
		end: 19,
		virtual: true,
	});
});

test("the window follows the scroll position and clamps overscan at the top", () => {
	assert.deepEqual(rosterWindow(1000, 2800, 28, 280), {
		start: 92,
		end: 119,
		virtual: true,
	});
	// first row is 8, so overscan clamps start to 0 while end stays absolute.
	assert.deepEqual(rosterWindow(1000, 8 * 28, 28, 280), {
		start: 0,
		end: 27,
		virtual: true,
	});
});

test("the window clamps to the member count at the bottom", () => {
	assert.deepEqual(rosterWindow(1000, 28000, 28, 280), {
		start: 992,
		end: 1000,
		virtual: true,
	});
});

test("an unmeasured stride/viewport falls back to the seeded estimates", () => {
	// both unmeasured: stride 28, viewport 28*12 -> 12 rows +1, +8 overscan.
	assert.deepEqual(rosterWindow(1000, 0, 0, 0), {
		start: 0,
		end: 21,
		virtual: true,
	});
	// only the stride is missing.
	assert.deepEqual(rosterWindow(1000, 0, 0, 280), {
		start: 0,
		end: 19,
		virtual: true,
	});
	assert.equal(DEFAULT_STRIDE, 28);
});

test("rosterStride falls back to height + gap with a single row", () => {
	assert.equal(rosterStride({ height: 20, top: 100 }, undefined, 4), 24);
	assert.equal(rosterStride({ height: 25, top: 0 }, undefined, 0), 25);
});

test("rosterStride prefers the offset delta between two rows", () => {
	assert.equal(
		rosterStride({ height: 20, top: 100 }, { height: 20, top: 130 }, 4),
		30,
	);
	// a non-positive delta (layout not settled) keeps height + gap.
	assert.equal(
		rosterStride({ height: 20, top: 100 }, { height: 20, top: 100 }, 4),
		24,
	);
});