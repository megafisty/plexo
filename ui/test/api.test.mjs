import { test, afterEach } from "bun:test";
import assert from "node:assert/strict";

import {
	fetchAds,
	fetchHistory,
	fetchSearchResults,
	fetchWarpmarks,
	loginSession,
	postSearch,
	renderBBCode,
} from "../src/api.js";

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

const failing = async () => {
	throw new TypeError("fetch failed");
};

test("a transport failure resolves to the read's failure contract, not a rejection", async () => {
	globalThis.fetch = failing;
	assert.equal(
		await fetchHistory({ session: "Vix", convKind: "dm", convId: "Kira" }),
		null,
	);
	assert.equal(await loginSession("pw"), false);
	assert.deepEqual(await fetchAds("Vix"), []);
	assert.equal(await fetchWarpmarks("Vix"), null);
	assert.equal(await fetchSearchResults("Vix"), null);
});

test("a non-2xx status keeps the failure contract", async () => {
	globalThis.fetch = async () => ({
		ok: false,
		status: 503,
		json: async () => ({}),
		text: async () => "down",
	});
	assert.equal(await fetchSearchResults("Vix"), null);
	assert.deepEqual(await fetchAds("Vix"), []);
});

test("postSearch surfaces the server's short error text", async () => {
	globalThis.fetch = async () => ({
		ok: false,
		status: 503,
		text: async () => "search unavailable\n",
	});
	assert.deepEqual(await postSearch("Vix", { kinks: [] }), {
		ok: false,
		error: "search unavailable",
	});
});

test("a malformed JSON body is a failure, not a rejection", async () => {
	globalThis.fetch = async () => ({
		ok: true,
		status: 200,
		json: async () => {
			throw new SyntaxError("bad json");
		},
	});
	assert.equal(await fetchSearchResults("Vix"), null);
	assert.deepEqual(await fetchAds("Vix"), []);
});

test("a successful read parses its body", async () => {
	globalThis.fetch = async () => ok({ characters: [], revision: 3 });
	assert.deepEqual(await fetchSearchResults("Vix"), {
		characters: [],
		revision: 3,
	});
});

test("renderBBCode posts the raw body and returns the rendered fragment", async () => {
	let seen;
	globalThis.fetch = async (url, init) => {
		seen = { url, init };
		return ok({ html: "<b>hi</b>" });
	};
	assert.equal(await renderBBCode("[b]hi[/b]"), "<b>hi</b>");
	assert.equal(seen.url, "/api/render");
	assert.equal(seen.init.method, "POST");
	assert.deepEqual(JSON.parse(seen.init.body), { bbcode: "[b]hi[/b]" });
});

test("renderBBCode resolves to null on failure", async () => {
	globalThis.fetch = failing;
	assert.equal(await renderBBCode("[b]hi[/b]"), null);
	globalThis.fetch = async () => ({ ok: false, status: 400, json: async () => ({}) });
	assert.equal(await renderBBCode("[b]hi[/b]"), null);
});