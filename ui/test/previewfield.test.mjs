import { test, afterEach } from "bun:test";
import assert from "node:assert/strict";

import {
	newPreviewState,
	resetPreview,
	togglePreview,
} from "../src/components/composer/previewfield.js";

const realFetch = globalThis.fetch;
afterEach(() => {
	globalThis.fetch = realFetch;
});

const ok = (body) => ({
	ok: true,
	status: 200,
	json: async () => body,
	text: async () => JSON.stringify(body),
});

/** tick drains the microtasks and the fetch/json chain before assertions. */
const tick = () => new Promise((resolve) => setTimeout(resolve, 0));

function state(overrides = {}) {
	return { ...newPreviewState(), ...overrides };
}

test("an empty draft previews without a round trip", () => {
	let calls = 0;
	globalThis.fetch = async () => {
		calls += 1;
		return ok({ html: "" });
	};
	const s = state();
	togglePreview(s);
	assert.equal(s.preview, true);
	assert.equal(s.previewBusy, false);
	assert.equal(s.previewHTML, "");
	assert.equal(calls, 0, "an empty body is rendered locally");
});

test("toggling to preview renders the current text", async () => {
	let body;
	globalThis.fetch = async (_url, init) => {
		body = JSON.parse(init.body).bbcode;
		return ok({ html: "<b>brb</b>" });
	};
	const s = state({ text: "[b]brb[/b]" });
	togglePreview(s);
	assert.equal(s.preview, true);
	assert.equal(s.previewBusy, true);
	await tick();
	assert.equal(body, "[b]brb[/b]");
	assert.equal(s.previewBusy, false);
	assert.equal(s.previewHTML, "<b>brb</b>");
	assert.equal(s.previewFor, "[b]brb[/b]");
	assert.equal(s.previewError, null);
});

test("an unchanged draft is not re-fetched", async () => {
	let calls = 0;
	globalThis.fetch = async () => {
		calls += 1;
		return ok({ html: "<b>x</b>" });
	};
	const s = state({ text: "[b]x[/b]" });
	togglePreview(s);
	await tick();
	togglePreview(s); // back to edit
	assert.equal(s.preview, false);
	togglePreview(s); // preview again, same text
	assert.equal(s.previewBusy, false);
	assert.equal(calls, 1);
});

test("a failed render shows an error and can be retried", async () => {
	globalThis.fetch = async () => {
		throw new TypeError("fetch failed");
	};
	const s = state({ text: "[b]x[/b]" });
	togglePreview(s);
	await tick();
	assert.equal(s.previewBusy, false);
	assert.equal(s.previewError, "Could not render a preview.");
	assert.equal(s.previewHTML, null);

	globalThis.fetch = async () => ok({ html: "<b>x</b>" });
	togglePreview(s); // back to edit
	togglePreview(s); // retry
	await tick();
	assert.equal(s.previewError, null);
	assert.equal(s.previewHTML, "<b>x</b>");
});

test("a result for stale text is discarded", async () => {
	globalThis.fetch = async () => ok({ html: "<b>old</b>" });
	const s = state({ text: "old" });
	togglePreview(s);
	// The field changed before the render landed.
	s.text = "new";
	await tick();
	assert.equal(s.previewHTML, null, "the stale fragment is dropped");
	assert.equal(s.previewError, null);
});

test("resetPreview drops the fragment and returns to editing", () => {
	const s = state({
		text: "x",
		preview: true,
		previewHTML: "<b>x</b>",
		previewFor: "x",
		previewBusy: true,
		previewError: "nope",
	});
	resetPreview(s);
	assert.equal(s.preview, false);
	assert.equal(s.previewHTML, null);
	assert.equal(s.previewFor, null);
	assert.equal(s.previewBusy, false);
	assert.equal(s.previewError, null);
});