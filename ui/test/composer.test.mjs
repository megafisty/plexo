// Unit tests for the composer's pure decision helpers. The component itself is
// covered by rendering in the manual harness; these pin the selection-wrapping
// and send-key routing that the chat editor and dialogs both rely on.
import { test } from "node:test";
import assert from "node:assert/strict";

import {
	isSendKey,
	wrapSelection,
} from "../app/components/composer/composer.js";

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