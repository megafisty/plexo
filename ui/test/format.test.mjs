// Unit tests for the composer format palette's list, the one shell opened by
// the composer rather than shortcuts.ts. The list is pure data + an apply
// closure from the context, so it can be exercised without a DOM.
import { test } from "bun:test";
import assert from "node:assert/strict";

import {
	AdvancedFormatList,
	CharacterSourceList,
	CharacterStyleList,
	ColorList,
	ExactNameList,
	FormatMarksList,
	MyCharactersList,
	SeenCharacterList,
	UrlList,
	advancedStartList,
} from "../src/components/commands/format.js";

test("the marks list offers superscript, subscript, strikethrough, and underline", () => {
	const rows = FormatMarksList.list({});
	assert.deepEqual(
		rows.map((r) => r.id).sort(),
		["s", "sub", "sup", "u"],
	);
});

test("choosing a mark applies its tag through the context closure", () => {
	const calls = [];
	const context = {
		format: (tag, param, value) => calls.push({ tag, param, value }),
	};
	const byId = new Map(FormatMarksList.list({}).map((r) => [r.id, r]));
	for (const id of ["sup", "sub", "s", "u"]) {
		FormatMarksList.onSelect(byId.get(id), context);
	}
	assert.deepEqual(calls, [
		{ tag: "sup", param: false, value: undefined },
		{ tag: "sub", param: false, value: undefined },
		{ tag: "s", param: false, value: undefined },
		{ tag: "u", param: false, value: undefined },
	]);
});

test("a chosen mark is a no-op without an apply closure", () => {
	const row = FormatMarksList.list({})[0];
	assert.doesNotThrow(() => FormatMarksList.onSelect(row, {}));
});

test("the advanced root offers the Colors, Make Link, and Link Character subcommands, plus Spoiler", () => {
	const rows = AdvancedFormatList.list({});
	assert.deepEqual(
		rows.map((r) => r.id),
		["colors", "url", "character-link", "spoiler"],
	);
	assert.equal(rows[0].next, ColorList);
	assert.equal(rows[1].next, UrlList);
	assert.equal(rows[2].next, CharacterSourceList);
	// Spoiler is a direct leaf, not a subcommand.
	assert.equal(rows[3].next, undefined);
});

test("Spoiler wraps the selection with no parameter", () => {
	const calls = [];
	const context = {
		format: (tag, param, value, content) =>
			calls.push({ tag, param, value, content }),
	};
	const row = AdvancedFormatList.list({}).find((r) => r.id === "spoiler");
	AdvancedFormatList.onSelect(row, context);
	assert.deepEqual(calls, [
		{ tag: "spoiler", param: false, value: undefined, content: undefined },
	]);
});

test("Link Character offers the three character sources", () => {
	assert.deepEqual(
		CharacterSourceList.list().map((r) => r.id),
		["mine", "seen", "exact"],
	);
	assert.equal(CharacterSourceList.list()[0].next, MyCharactersList);
	assert.equal(CharacterSourceList.list()[1].next, SeenCharacterList);
	assert.equal(CharacterSourceList.list()[2].next, ExactNameList);
});

test("My Characters rows carry the name for the link step", () => {
	const context = {
		store: {
			account: { status: "ok", characters: ["Zeta", "Alpha"] },
			characters: {},
		},
	};
	const rows = MyCharactersList.list(context);
	assert.deepEqual(
		rows.map((r) => r.id),
		["Alpha", "Zeta"],
	);
	assert.equal(rows[0].value, "Alpha");
	assert.equal(rows[0].next, CharacterStyleList);
});

test("Characters in Chat excludes own logged-in characters", () => {
	const context = {
		store: {
			characters: {
				Online: { name: "Online", online: true },
				Offline: { name: "Offline", online: false },
				Me: { name: "Me", online: true },
			},
			sessions: { s1: { character: "Me" } },
		},
	};
	assert.deepEqual(
		SeenCharacterList.list(context).map((r) => r.id),
		["Online"],
	);
});

test("the link style applies [icon] or [user] with the character name", () => {
	const calls = [];
	const context = {
		format: (tag, param, value, content) =>
			calls.push({ tag, param, value, content }),
	};
	const previous = CharacterStyleList.transformPrevious({ value: "Seraph" });
	const rows = CharacterStyleList.list(context, previous);
	CharacterStyleList.onSelect(
		rows.find((r) => r.id === "icon"),
		context,
		previous,
	);
	CharacterStyleList.onSelect(
		rows.find((r) => r.id === "user"),
		context,
		previous,
	);
	assert.deepEqual(calls, [
		{ tag: "icon", param: false, value: undefined, content: "Seraph" },
		{ tag: "user", param: false, value: undefined, content: "Seraph" },
	]);
});

test("Exact Name applies the style to the typed name", () => {
	const calls = [];
	const context = {
		format: (tag, param, value, content) =>
			calls.push({ tag, param, value, content }),
	};
	const row = ExactNameList.list(context).find((r) => r.id === "user");
	// Free-text mode attaches the typed input to the chosen row.
	ExactNameList.onSelect({ ...row, input: "Some Guy" }, context);
	assert.deepEqual(calls, [
		{ tag: "user", param: false, value: undefined, content: "Some Guy" },
	]);
});

test("a start id opens the advanced palette on that sub-list", () => {
	const colors = advancedStartList("format-colors");
	assert.equal(colors.current, ColorList);
	assert.equal(colors.parent.id, "colors");

	const url = advancedStartList("format-url");
	assert.equal(url.current, UrlList);
	assert.equal(url.parent.id, "url");

	const character = advancedStartList("format-character-source");
	assert.equal(character.current, CharacterSourceList);
	assert.equal(character.parent.id, "character-link");

	// No start (or an unknown one) opens the root.
	assert.equal(advancedStartList(undefined), undefined);
	assert.equal(advancedStartList("nope"), undefined);
});

test("a character-link start with a selection skips to exact name", () => {
	const shortcut = advancedStartList("format-character-source", "Seraph");
	assert.equal(shortcut.current, ExactNameList);
	assert.equal(shortcut.parent.id, "exact");
	assert.equal(shortcut.query, "Seraph");

	// An empty selection keeps the source picker.
	const sources = advancedStartList("format-character-source", "");
	assert.equal(sources.current, CharacterSourceList);
	assert.equal(sources.query, undefined);
});

test("the URL list picks its mode from whether the selection is a URL", () => {
	// A URL selection: set the link text, or reuse the URL for both.
	assert.deepEqual(
		UrlList.list({ selection: "https://example.com" }).map((r) => r.id),
		["link-text", "just-url"],
	);

	// Anything else is link text, so the input supplies the URL.
	assert.deepEqual(UrlList.list({}).map((r) => r.id), ["set-url"]);
	assert.deepEqual(
		UrlList.list({ selection: "example.com" }).map((r) => r.id),
		["set-url"],
	);
});

test("Just URL uses the URL as both target and body", () => {
	const calls = [];
	const context = {
		selection: "https://example.com",
		format: (tag, param, value, content) =>
			calls.push({ tag, param, value, content }),
	};
	const byId = new Map(UrlList.list(context).map((r) => [r.id, r]));
	UrlList.onSelect(byId.get("just-url"), context);
	assert.deepEqual(calls, [
		{
			tag: "url",
			param: true,
			value: "https://example.com",
			content: "https://example.com",
		},
	]);
});

test("Set Link Text uses the palette input as the body", () => {
	const calls = [];
	const context = {
		selection: "https://example.com",
		format: (tag, param, value, content) =>
			calls.push({ tag, param, value, content }),
	};
	const row = UrlList.list(context).find((r) => r.id === "link-text");
	// The palette attaches the typed input to the chosen item in free-text mode.
	UrlList.onSelect({ ...row, input: "click here" }, context);
	assert.deepEqual(calls, [
		{
			tag: "url",
			param: true,
			value: "https://example.com",
			content: "click here",
		},
	]);
});

test("Set URL uses the palette input as the target and the selection as the body", () => {
	const calls = [];
	const context = {
		selection: "click here",
		format: (tag, param, value, content) =>
			calls.push({ tag, param, value, content }),
	};
	const row = UrlList.list(context).find((r) => r.id === "set-url");
	// No validation: anything typed becomes the target.
	UrlList.onSelect({ ...row, input: "not-even-a-url" }, context);
	assert.deepEqual(calls, [
		{
			tag: "url",
			param: true,
			value: "not-even-a-url",
			content: "click here",
		},
	]);
});

test("the colors list applies the color tag with a named parameter", () => {
	const calls = [];
	const context = {
		format: (tag, param, value) => calls.push({ tag, param, value }),
	};
	const byId = new Map(ColorList.list({}).map((r) => [r.id, r]));
	ColorList.onSelect(byId.get("color:red"), context);
	ColorList.onSelect(byId.get("color:gray"), context);
	assert.deepEqual(calls, [
		{ tag: "color", param: true, value: "red" },
		{ tag: "color", param: true, value: "gray" },
	]);
});

test("the colors list offers only the curated named set", () => {
	const names = ColorList.list({}).map((r) => r.value.value);
	assert.deepEqual(names, [
		"red",
		"orange",
		"yellow",
		"green",
		"cyan",
		"purple",
		"blue",
		"pink",
		"black",
		"brown",
		"white",
		"gray",
	]);
	assert.equal(names.every((n) => /^[a-z]+$/.test(n)), true);
});
