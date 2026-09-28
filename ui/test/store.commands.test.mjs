import { test, afterEach } from "bun:test";
import assert from "node:assert/strict";

import { loadWarpmarks, loadAutoStatus, saveAutoStatus, refetchCharacterPresence, requestCharacterPresence } from "../src/store/commands.js";
import { applySearchResults } from "../src/store/search.js";
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

test("saveAutoStatus replaces only the automatic status in the whole document", async () => {
	const calls = [];
	globalThis.fetch = async (url, init) => {
		if (init?.method === "PUT") {
			calls.push({ url, body: JSON.parse(init.body) });
			return { ok: true, status: 204, text: async () => "" };
		}
		return {
			ok: true,
			status: 200,
			json: async () => ({
				global: {},
				character: {
					highlights: ["Kira"],
					autoJoin: [
						{ kind: "official", id: "Frontpage", name: "Frontpage" },
					],
				},
				hasGlobal: false,
				hasCharacter: true,
			}),
		};
	};
	assert.equal(await saveAutoStatus("Vix", { status: "away", message: "brb" }), null);
	assert.equal(calls.length, 1);
	assert.deepEqual(calls[0].body, {
		highlights: ["Kira"],
		autoJoin: [{ kind: "official", id: "Frontpage", name: "Frontpage" }],
		autoStatus: { status: "away", message: "brb" },
	});
});

test("loadAutoStatus distinguishes a failed read from none saved", async () => {
	globalThis.fetch = async () => {
		throw new TypeError("fetch failed");
	};
	assert.equal(await loadAutoStatus("Vix"), undefined);
	globalThis.fetch = async () => ({
		ok: true,
		status: 200,
		json: async () => ({
			global: {},
			character: {},
			hasGlobal: false,
			hasCharacter: true,
		}),
	});
	assert.equal(await loadAutoStatus("Vix"), null);
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

test("refetchCharacterPresence applies the matching row to the registry", async () => {
	const { store } = live();
	let called = "";
	globalThis.fetch = async (url) => {
		called = String(url);
		return marks([
			{
				name: "Kira",
				gender: "Female",
				status: "looking",
				statusMsg: "<b>hi</b>",
				admin: false,
				online: true,
			},
		]);
	};
	// Query casing is not exact; the applied record must use the server spelling.
	await refetchCharacterPresence(store, "Vix", "kira");
	assert.ok(called.includes("/api/presence?"));
	assert.ok(called.includes("q=kira"));
	assert.deepEqual(store.characters.Kira, {
		name: "Kira",
		gender: "Female",
		status: "looking",
		statusMsg: "<b>hi</b>",
		admin: false,
		online: true,
		presenceKnown: true,
	});
});

test("refetchCharacterPresence ignores a non-exact match", async () => {
	const { store } = live();
	globalThis.fetch = async () =>
		marks([
			{ name: "Kira", gender: "Female", online: true },
			{ name: "KiraTwo", gender: "Female", online: true },
		]);
	// "kirat" is a substring hit for KiraTwo but exacts neither row.
	await refetchCharacterPresence(store, "Vix", "kirat");
	assert.equal(store.characters.KiraTwo, undefined);
});

test("requestCharacterPresence fetches once per target for a mount", async () => {
	const { store } = live();
	let calls = 0;
	globalThis.fetch = async () => {
		calls++;
		return marks([{ name: "Kira", gender: "Female", online: true }]);
	};
	const state = {};
	requestCharacterPresence(state, store, "Vix", "Kira", { online: true });
	requestCharacterPresence(state, store, "Vix", "Kira", { online: true });
	await Promise.resolve();
	assert.equal(calls, 1);
	assert.equal(state.requested, "Vix\u0000Kira");
});

test("requestCharacterPresence skips offline and already-known characters", () => {
	const { store } = live();
	globalThis.fetch = async () => {
		throw new Error("should not fetch");
	};
	const state = {};
	requestCharacterPresence(state, store, "Vix", "Off", { online: false });
	requestCharacterPresence(state, store, "Vix", "Known", {
		online: true,
		statusMsg: "<b>hi</b>",
	});
	assert.equal(state.requested, undefined);
});

test("requestCharacterPresence fetches again when the target changes", async () => {
	const { store } = live();
	let calls = 0;
	globalThis.fetch = async () => {
		calls++;
		return marks([]);
	};
	const state = {};
	requestCharacterPresence(state, store, "Vix", "Kira", { online: true });
	requestCharacterPresence(state, store, "Vix", "Other", { online: true });
	await Promise.resolve();
	assert.equal(calls, 2);
});