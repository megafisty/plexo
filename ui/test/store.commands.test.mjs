import { test, afterEach } from "node:test";
import assert from "node:assert/strict";

import { loadWarpmarks } from "../app/store/commands.js";
import { applySearchResults } from "../app/store/search.js";
import { live } from "./helpers.mjs";

const realFetch = globalThis.fetch;
afterEach(() => {
	globalThis.fetch = realFetch;
});

const marks = (body) => ({
	ok: true,
	status: 200,
	json: async () => body,
});

test("loadWarpmarks does not resurrect marks for a session closed mid-fetch", async () => {
	const { store } = live();
	globalThis.fetch = async () => {
		delete store.sessions.Vix; // the character logs out while the read is in flight
		return marks({ warpmarks: [{ entryId: "e1" }] });
	};
	await loadWarpmarks(store, "Vix");
	assert.equal(store.warpmarks.Vix, undefined);
});

test("loadWarpmarks stores marks for a live session", async () => {
	const { store } = live();
	globalThis.fetch = async () => marks({ warpmarks: [{ entryId: "e1" }] });
	await loadWarpmarks(store, "Vix");
	assert.deepEqual(store.warpmarks.Vix, [{ entryId: "e1" }]);
});

test("applySearchResults ignores a result set for a closed session", () => {
	const { store } = live();
	delete store.sessions.Vix;
	applySearchResults(store, "Vix", {
		characters: [{ name: "Kira", online: true }],
		revision: 2,
	});
	assert.equal(store.search.Vix, undefined);
});