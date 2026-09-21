import { test } from "bun:test";
import assert from "node:assert/strict";

import { RowCache, moderatorFor, FeaturedCharacter } from "../src/components/presence/character.js";

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

/** walk yields a vnode and everything reachable through its children. The
 * preloaded `m` stub stores raw { tag, attrs, children }, and — unlike real
 * Mithril — treats an array second argument as attrs, so a children-only call
 * like m("span", [a, b]) lands in attrs; traverse both. */
function* walk(node) {
	if (node === null || node === undefined || typeof node !== "object") {
		return;
	}
	yield node;
	const children = node.children;
	if (Array.isArray(children)) {
		for (const child of children) {
			yield* walk(child);
		}
	} else {
		yield* walk(children);
	}
	if (Array.isArray(node.attrs)) {
		for (const child of node.attrs) {
			yield* walk(child);
		}
	}
}

/** nodeByTag returns the first string-tagged vnode matching `tag`. */
function nodeByTag(root, tag) {
	for (const node of walk(root)) {
		if (node.tag === tag) {
			return node;
		}
	}
	return null;
}

test("a featured row keeps its status message outside the activation element", () => {
	const statusMsg = '<a href="https://x/" target="_blank">link</a>';
	const vnode = FeaturedCharacter.view({
		attrs: {
			character: { name: "Kira", online: true, statusMsg },
			row: true,
		},
	});
	const main = nodeByTag(vnode, "button.featured-character-main");
	assert.ok(main, "the row has a name-header button");
	assert.equal(main.attrs["data-character"], "Kira");
	// The avatar is a second mouse target, hidden from AT and out of the tab
	// order so the name button stays the single accessible control.
	const avatar = nodeByTag(vnode, "button.featured-character-avatar");
	assert.ok(avatar, "the avatar is a redundant mouse target");
	assert.equal(avatar.attrs["data-character"], "Kira");
	assert.equal(avatar.attrs["aria-hidden"], "true");
	assert.equal(avatar.attrs["tabindex"], "-1");
	const status = nodeByTag(vnode, "span.featured-character-status-msg");
	assert.ok(status, "the row renders the status message");
	// The status carries links, so it must not sit inside either element that
	// the delegated handler resolves as the character.
	assert.equal([...walk(main)].includes(status), false);
	assert.equal([...walk(avatar)].includes(status), false);
});

test("a featured row without row mode has no activation element", () => {
	const vnode = FeaturedCharacter.view({
		attrs: { character: { name: "Kira", online: true, statusMsg: "hi" } },
	});
	let activation = 0;
	for (const node of walk(vnode)) {
		if (node.attrs !== undefined && node.attrs["data-character"] !== undefined) {
			activation++;
		}
	}
	assert.equal(activation, 0);
});
