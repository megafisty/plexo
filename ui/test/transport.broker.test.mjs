import { test } from "node:test";
import assert from "node:assert/strict";

import { createBroker } from "../app/transport/broker.js";

test("ask resolves with the matching ack", async () => {
	const broker = createBroker(() => "u-1");
	const p = broker.ask({ op: "join" });
	broker.resolve("u-1", { accepted: true });
	assert.deepEqual(await p, { accepted: true });
});

test("ask fails immediately when not connected", async () => {
	const broker = createBroker(() => "");
	const r = await broker.ask({ op: "join" });
	assert.equal(r.accepted, false);
	assert.match(r.errorMsg, /not connected/i);
});

test("ask times out instead of hanging forever", async () => {
	const broker = createBroker(() => "u-1");
	const r = await broker.ask({ op: "join" }, 5);
	assert.equal(r.accepted, false);
	assert.match(r.errorMsg, /did not respond/i);
});

test("a late ack after the timeout is ignored", async () => {
	const broker = createBroker(() => "u-1");
	const p = broker.ask({ op: "join" }, 5);
	const first = await p;
	broker.resolve("u-1", { accepted: true }); // must not throw or re-settle
	assert.equal(first.accepted, false);
	assert.deepEqual(await p, first);
});

test("abort fails every pending waiter and clears its timeout", async () => {
	let n = 0;
	const broker = createBroker(() => `u-${++n}`);
	const a = broker.ask({ op: "join" });
	const b = broker.ask({ op: "login" }, 60000);
	broker.abort("Connection lost.");
	const [ra, rb] = await Promise.all([a, b]);
	assert.equal(ra.accepted, false);
	assert.match(ra.errorMsg, /connection lost/i);
	assert.equal(rb.accepted, false);
	assert.match(rb.errorMsg, /connection lost/i);
});