import { test } from "node:test";
import assert from "node:assert/strict";

import { RowCache, moderatorFor } from "../app/components/presence/character.js";

test("moderatorFor prefers global admin over room op", () => {
	assert.equal(moderatorFor({ admin: true }, new Set(["a"]), "a"), "global");
	assert.equal(moderatorFor({ admin: false }, new Set(["a"]), "a"), "room");
	assert.equal(moderatorFor({ admin: false }, new Set(), "a"), undefined);
	assert.equal(moderatorFor(undefined, new Set(), "a"), undefined);
});

test("presenceOf returns the live record or one stable placeholder", () => {
	const cache = new RowCache();
	const live = { name: "Kira", online: true };
	assert.equal(cache.presenceOf("Kira", live), live);
	const first = cache.presenceOf("Vix", undefined);
	const second = cache.presenceOf("Vix", undefined);
	assert.equal(first, second);
	assert.equal(first.name, "Vix");
	assert.equal(first.online, false);
});

test("value reuses a row while the record and moderator mark are unchanged", () => {
	const cache = new RowCache();
	const rec = { name: "Kira", online: true };
	let builds = 0;
	const build = () => ({ built: ++builds });

	const a = cache.value("Kira", rec, "room", build);
	const b = cache.value("Kira", rec, "room", build);
	assert.equal(a, b);
	assert.equal(builds, 1);

	// A new record identity rebuilds.
	const c = cache.value("Kira", { name: "Kira", online: true }, "room", build);
	assert.notEqual(a, c);
	assert.equal(builds, 2);

	// A changed moderator mark rebuilds.
	cache.value("Kira", rec, "global", build);
	assert.equal(builds, 3);
});

test("isStale matches value's rebuild rule", () => {
	const cache = new RowCache();
	const rec = { name: "Kira", online: true };
	assert.equal(cache.isStale("Kira", rec, "room"), true); // nothing cached
	cache.value("Kira", rec, "room", () => ({}));
	assert.equal(cache.isStale("Kira", rec, "room"), false);
	assert.equal(cache.isStale("Kira", rec, "global"), true);
	assert.equal(cache.isStale("Kira", { name: "Kira", online: true }, "room"), true);
});

test("prune drops values and placeholders outside the kept set", () => {
	const cache = new RowCache();
	const rec = { name: "Kira", online: true };
	cache.value("Kira", rec, undefined, () => "k");
	cache.value("Vix", cache.presenceOf("Vix", undefined), undefined, () => "v");

	cache.prune(new Set(["Kira"]));
	assert.equal(cache.isStale("Kira", rec, undefined), false);
	// Vix's value (and its placeholder) are gone, so it re-materializes.
	assert.equal(
		cache.isStale("Vix", cache.presenceOf("Vix", undefined), undefined),
		true,
	);
});

test("clear empties every entry", () => {
	const cache = new RowCache();
	cache.value("Kira", { name: "Kira", online: true }, undefined, () => "k");
	cache.presenceOf("Vix", undefined);
	cache.clear();
	assert.equal(
		cache.isStale("Kira", { name: "Kira", online: true }, undefined),
		true,
	);
});
