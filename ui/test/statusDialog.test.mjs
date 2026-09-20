import { test, afterEach } from "bun:test";
import assert from "node:assert/strict";

import { applyAuto, copyAutoToDraft } from "../src/components/presence/statusDialog.js";

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
	return {
		status: "online",
		text: "",
		seed: "Vix",
		auto: null,
		autoBusy: null,
		autoError: null,
		autoNote: null,
		autoHTML: null,
		autoHTMLFor: null,
		previewKey: 0,
		onformat: () => {},
		...overrides,
	};
}

test("an empty automatic status message renders locally", () => {
	let calls = 0;
	globalThis.fetch = async () => {
		calls += 1;
		return ok({ html: "" });
	};
	const s = state();
	applyAuto(s, { status: "away", message: "" });
	assert.equal(s.auto.status, "away");
	assert.equal(s.autoHTML, "");
	assert.equal(calls, 0);
});

test("the automatic status message is rendered and cached", async () => {
	let calls = 0;
	globalThis.fetch = async () => {
		calls += 1;
		return ok({ html: "<i>brb</i>" });
	};
	const s = state();
	applyAuto(s, { status: "away", message: "brb [i]soon[/i]" });
	assert.equal(s.autoHTML, null, "starts in flight");
	await tick();
	assert.equal(s.autoHTML, "<i>brb</i>");
	assert.equal(s.autoHTMLFor, "brb [i]soon[/i]");

	// The same message is not re-rendered (e.g. on a re-save).
	applyAuto(s, { status: "away", message: "brb [i]soon[/i]" });
	assert.equal(calls, 1);
});

test("a failed automatic-status render falls back to an empty fragment", async () => {
	globalThis.fetch = async () => {
		throw new TypeError("fetch failed");
	};
	const s = state();
	applyAuto(s, { status: "away", message: "[i]x[/i]" });
	await tick();
	assert.equal(s.autoHTML, "", "the FeaturedCharacter falls back to the label");
});

test("clearing the automatic status drops the rendered fragment", () => {
	globalThis.fetch = async () => ok({ html: "<b>x</b>" });
	const s = state({ auto: { status: "away", message: "x" }, autoHTML: "<b>x</b>" });
	applyAuto(s, null);
	assert.equal(s.auto, null);
	assert.equal(s.autoHTML, "");
});

test("an automatic-status render for another session is discarded", async () => {
	globalThis.fetch = async () => ok({ html: "<b>old</b>" });
	const s = state();
	applyAuto(s, { status: "away", message: "x" });
	s.seed = "Kira";
	await tick();
	assert.equal(s.autoHTML, null, "the stale fragment is dropped");
});

test("copying the automatic status overwrites the draft and leaves editing", () => {
	const s = state({
		text: "current draft",
		status: "online",
		previewKey: 4,
		auto: { status: "away", message: "brb [b]soon[/b]" },
	});
	copyAutoToDraft(s);
	assert.equal(s.text, "brb [b]soon[/b]");
	assert.equal(s.status, "online", "only the message round-trips");
	assert.equal(s.previewKey, 5, "bumping the key drops any message preview");
});

test("copying with nothing saved is a no-op", () => {
	const s = state({ text: "draft" });
	copyAutoToDraft(s);
	assert.equal(s.text, "draft");
	assert.equal(s.previewKey, 0);
});
