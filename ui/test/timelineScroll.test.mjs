import { test } from "bun:test";
import assert from "node:assert/strict";

import {
	DEFER_ROW_THRESHOLD,
	isPinned,
	rowsStale,
	shouldDeferRows,
	timelineScrollAction,
} from "../src/components/messages/timelineScroll.js";

test("isPinned treats the bottom margin as still pinned", () => {
	assert.equal(isPinned(0, 100, 100), true);
	assert.equal(isPinned(0, 100, 500), false);
	// exactly one margin from the bottom is pinned; one pixel further is not.
	assert.equal(isPinned(340, 100, 500), true);
	assert.equal(isPinned(339, 100, 500), false);
});

test("rowsStale keys on the window identity and its rev", () => {
	const win = { rev: 5 };
	assert.equal(rowsStale(undefined, undefined, win), true);
	assert.equal(rowsStale(win, 5, win), false);
	assert.equal(rowsStale(win, 4, win), true);
	assert.equal(rowsStale({ rev: 5 }, 5, win), true);
});

test("only a first sight of a heavy window defers its rows", () => {
	assert.equal(shouldDeferRows(false, DEFER_ROW_THRESHOLD + 1), true);
	assert.equal(shouldDeferRows(false, DEFER_ROW_THRESHOLD), false);
	assert.equal(shouldDeferRows(true, 10000), false);
});

test("an anchor restore outranks the live edge", () => {
	assert.equal(
		timelineScrollAction({
			hasAnchor: true,
			pinned: false,
			deferred: false,
			rev: 5,
			scrolledRev: 5,
		}),
		"restore-anchor",
	);
});

test("the live edge scrolls only when the window rev advanced", () => {
	assert.equal(
		timelineScrollAction({
			hasAnchor: false,
			pinned: true,
			deferred: false,
			rev: 5,
			scrolledRev: 4,
		}),
		"scroll-edge",
	);
	// an unrelated redraw must not re-read scrollHeight.
	assert.equal(
		timelineScrollAction({
			hasAnchor: false,
			pinned: true,
			deferred: false,
			rev: 5,
			scrolledRev: 5,
		}),
		"none",
	);
	// a deferred frame has no rows yet, so scrolling there is meaningless.
	assert.equal(
		timelineScrollAction({
			hasAnchor: false,
			pinned: true,
			deferred: true,
			rev: 5,
			scrolledRev: 4,
		}),
		"none",
	);
	// not pinned, or no rev at all.
	assert.equal(
		timelineScrollAction({
			hasAnchor: false,
			pinned: false,
			deferred: false,
			rev: 5,
			scrolledRev: 4,
		}),
		"none",
	);
	assert.equal(
		timelineScrollAction({
			hasAnchor: false,
			pinned: true,
			deferred: false,
			rev: undefined,
			scrolledRev: undefined,
		}),
		"none",
	);
});