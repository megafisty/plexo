// Unit tests for the composer's pure decision helpers. The component itself is
// covered by rendering in the manual harness; these pin the selection-wrapping
// and send-key routing that the chat editor and dialogs both rely on.
import { test } from "bun:test";
import assert from "node:assert/strict";

import {
	isSendKey,
	wrapSelection,
} from "../src/components/composer/composer.js";
import { pastedUrl } from "../src/lib/format.js";

test("wrapSelection wraps a selection and keeps the whole tag selected", () => {
	const r = wrapSelection("hello", 0, 5, "b", false);
	assert.equal(r.value, "[b]hello[/b]");
	assert.equal(r.start, 0);
	assert.equal(r.end, 12);
});

test("wrapSelection nests when the previous tag is selected", () => {
	const b = wrapSelection("hello", 0, 5, "b", false);
	const i = wrapSelection(b.value, b.start, b.end, "i", false);
	assert.equal(i.value, "[i][b]hello[/b][/i]");
	assert.equal(i.start, 0);
	assert.equal(i.end, i.value.length);
});

test("wrapSelection with no selection puts the caret between the tags", () => {
	const r = wrapSelection("hi", 1, 1, "i", false);
	assert.equal(r.value, "h[i][/i]i");
	assert.equal(r.start, 4);
	assert.equal(r.end, 4);
});

test("wrapSelection for a parameterized tag puts the caret in the value", () => {
	const r = wrapSelection("hi", 0, 2, "url", true);
	assert.equal(r.value, "[url=]hi[/url]");
	assert.equal(r.start, 5); // between "=" and "]"
	assert.equal(r.end, 5);
});

test("wrapSelection with a parameter value keeps the wrapped tag selected", () => {
	const r = wrapSelection("hi", 0, 2, "color", true, "red");
	assert.equal(r.value, "[color=red]hi[/color]");
	assert.equal(r.start, 0);
	assert.equal(r.end, r.value.length);
});

test("wrapSelection with a parameter value and no selection puts the caret between tags", () => {
	const r = wrapSelection("hi", 1, 1, "color", true, "#00ff00");
	assert.equal(r.value, "h[color=#00ff00][/color]i");
	assert.equal(r.start, 16); // between the opening and closing tags
	assert.equal(r.end, 16);
});

test("wrapSelection with content overrides the selected body", () => {
	// The URL palette's "Just URL": the URL is selected and also the body.
	const r = wrapSelection("https://example.com", 0, 19, "url", true, "https://example.com", "https://example.com");
	assert.equal(r.value, "[url=https://example.com]https://example.com[/url]");
	assert.equal(r.start, 0);
	assert.equal(r.end, r.value.length);
});

test("wrapSelection with an empty content puts the caret between tags", () => {
	// The URL palette's "Set Link Text" with an empty input.
	const r = wrapSelection("https://example.com", 0, 19, "url", true, "https://example.com", "");
	assert.equal(r.value, "[url=https://example.com][/url]");
	assert.equal(r.start, 25); // between the opening and closing tags
	assert.equal(r.end, 25);
});

const key = (o = {}) => ({
	key: "Enter",
	shiftKey: false,
	ctrlKey: false,
	metaKey: false,
	...o,
});

test("isSendKey: Enter sends, Shift+Enter newlines", () => {
	assert.equal(isSendKey(key(), false), true);
	assert.equal(isSendKey(key({ shiftKey: true }), false), false);
});

test("isSendKey: in newline mode only Ctrl/Cmd+Enter sends", () => {
	assert.equal(isSendKey(key(), true), false);
	assert.equal(isSendKey(key({ ctrlKey: true }), true), true);
	assert.equal(isSendKey(key({ metaKey: true }), true), true);
});

test("isSendKey ignores non-Enter keys", () => {
	assert.equal(isSendKey(key({ key: "a" }), false), false);
});

test("pastedUrl accepts a bare http(s) URL, trimmed", () => {
	assert.equal(pastedUrl("https://example.com"), "https://example.com");
	assert.equal(
		pastedUrl("  http://example.com/x?y=1\n"),
		"http://example.com/x?y=1",
	);
});

test("pastedUrl leaves anything but a single bare URL to a normal paste", () => {
	assert.equal(pastedUrl(""), undefined);
	assert.equal(pastedUrl("example.com"), undefined);
	assert.equal(pastedUrl("see https://example.com"), undefined);
	assert.equal(pastedUrl("https://example.com and more"), undefined);
	assert.equal(pastedUrl("ftp://example.com"), undefined);
});