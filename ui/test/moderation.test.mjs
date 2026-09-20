// Headless tests for the shared room-moderation authority model. These cover
// the capability matrix and the command mapping; no network or DOM is involved.

import { test } from "bun:test";
import assert from "node:assert/strict";

import {
	canManageRoom,
	canModerate,
	conversationContext,
	memberCapabilities,
	requestFor,
	roomContext,
	roomOps,
} from "../src/lib/moderation.js";
import { live } from "./helpers.mjs";

/** ctx builds a RoomContext with sensible channel defaults. */
function ctx(o = {}) {
	return roomContext(
		{
			kind: o.kind ?? "room",
			readOnly: o.readOnly,
			role: o.role ?? "mod",
			members: o.members ?? ["Vix", "Kira", "Bob"],
			ops: o.ops ?? ["Vix", "Kira"],
		},
		o.self ?? "Vix",
		o.selfAdmin ?? false,
		o.owner,
	);
}

const member = (name, admin = false) => ({ name, admin });

test("memberCapabilities denies every action outside a channel", () => {
	for (const kind of ["dm", "broadcast", "warp"]) {
		const caps = memberCapabilities(ctx({ kind, role: "owner" }), member("Bob"));
		assert.deepEqual(caps, {
			op: false,
			deop: false,
			kick: false,
			ban: false,
			unban: false,
			timeout: false,
		});
	}
});

test("memberCapabilities denies a read-only or role-less member", () => {
	assert.equal(memberCapabilities(ctx({ readOnly: true }), member("Bob")).kick, false);
	assert.equal(memberCapabilities(ctx({ role: "none" }), member("Bob")).kick, false);
	assert.equal(canModerate(ctx({ role: "none" })), false);
});

test("a room moderator may kick, ban, unban, and time out, but not op", () => {
	const caps = memberCapabilities(ctx({ role: "mod" }), member("Bob"));
	assert.deepEqual(caps, {
		op: false,
		deop: false,
		kick: true,
		ban: true,
		unban: true,
		timeout: true,
	});
	assert.equal(canModerate(ctx({ role: "mod" })), true);
});

test("a room owner may op/deop members, and only owner or admin may", () => {
	const owner = ctx({ role: "owner", ops: ["Vix", "Kira"] });
	assert.equal(memberCapabilities(owner, member("Bob")).op, true);
	assert.equal(memberCapabilities(owner, member("Bob")).deop, false);
	assert.equal(memberCapabilities(owner, member("Kira")).op, false);
	assert.equal(memberCapabilities(owner, member("Kira")).deop, true);
	// A plain moderator cannot op even a non-member target.
	assert.equal(memberCapabilities(ctx({ role: "mod" }), member("Bob")).op, false);
});

test("a global moderator has owner-level authority without a room role", () => {
	const admin = ctx({ role: "none", selfAdmin: true, ops: ["Kira"] });
	assert.equal(memberCapabilities(admin, member("Bob")).op, true);
	assert.equal(memberCapabilities(admin, member("Kira")).deop, true);
});

test("no one may act on themselves except unban", () => {
	const caps = memberCapabilities(ctx({ role: "owner" }), member("Vix"));
	assert.equal(caps.kick, false);
	assert.equal(caps.ban, false);
	assert.equal(caps.timeout, false);
	assert.equal(caps.op, false);
	assert.equal(caps.deop, false);
	assert.equal(caps.unban, true);
});

test("member actions require membership; unban does not", () => {
	const caps = memberCapabilities(ctx({ role: "owner" }), member("Ghost"));
	assert.equal(caps.op, false);
	assert.equal(caps.kick, false);
	assert.equal(caps.ban, false);
	assert.equal(caps.unban, true);
});

test("a plain moderator may not target the owner or a global moderator", () => {
	const mod = ctx({ role: "mod", owner: "Kira" });
	assert.equal(memberCapabilities(mod, member("Kira")).kick, false);
	assert.equal(memberCapabilities(mod, member("Kira")).ban, false);
	assert.equal(memberCapabilities(mod, member("Kira")).timeout, false);
	assert.equal(memberCapabilities(mod, member("Bob", true)).kick, false);
	assert.equal(memberCapabilities(mod, member("Bob")).kick, true);
});

test("only a global moderator may target the owner or another global moderator", () => {
	const admin = ctx({ role: "mod", selfAdmin: true, owner: "Kira" });
	assert.equal(memberCapabilities(admin, member("Kira")).kick, true);
	assert.equal(memberCapabilities(admin, member("Bob", true)).ban, true);
});

test("names compare case-insensitively", () => {
	const owner = ctx({ role: "owner", ops: ["VIX", "kira"], members: ["vix", "KIRA", "bob"] });
	assert.equal(memberCapabilities(owner, member("Kira")).deop, true);
	assert.equal(memberCapabilities(owner, member("BOB")).op, true);
	assert.equal(memberCapabilities(owner, member("vix")).kick, false);
});

test("canManageRoom reflects true authority but stays room-shaped", () => {
	assert.equal(canManageRoom(ctx({ kind: "room", role: "mod" })), true);
	assert.equal(canManageRoom(ctx({ kind: "room", role: "owner" })), true);
	assert.equal(canManageRoom(ctx({ kind: "room", role: "none", selfAdmin: true })), true);
	assert.equal(canManageRoom(ctx({ kind: "room", role: "none" })), false);
	assert.equal(canManageRoom(ctx({ kind: "room", role: "mod", readOnly: true })), false);
	// The management dialog is room-only; official channels are excluded even
	// though their ops may moderate members.
	assert.equal(canManageRoom(ctx({ kind: "official", role: "mod", selfAdmin: true })), false);
});

test("requestFor maps each verb to its wire command", () => {
	assert.deepEqual(requestFor("op", "Bob"), { action: "add_mod", character: "Bob" });
	assert.deepEqual(requestFor("deop", "Bob"), { action: "remove_mod", character: "Bob" });
	assert.deepEqual(requestFor("kick", "Bob"), { action: "kick", character: "Bob" });
	assert.deepEqual(requestFor("ban", "Bob"), { action: "ban", character: "Bob" });
	assert.deepEqual(requestFor("unban", "Bob"), { action: "unban", character: "Bob" });
	assert.deepEqual(requestFor("timeout", "Bob", 10), {
		action: "timeout",
		character: "Bob",
		length: 10,
	});
});

/** conv is a minimal live room conversation for the roomOps tests. */
const conv = {
	key: "room:ADH-x",
	conv: { kind: "room", id: "ADH-x" },
	session: "Vix",
	role: "owner",
	members: ["Vix", "Bob"],
	ops: ["Vix"],
};

test("roomOps sends the mapped command and reports the outcome", async () => {
	const { store } = live();
	const calls = [];
	const results = [];
	const actions = {
		roomAdmin: async (session, ref, req) => {
			calls.push({ session, ref, req });
			return null;
		},
	};
	const ops = roomOps(store, actions, "Vix", conv, (action, name, error) => {
		results.push({ action, name, error });
	});
	assert.equal((await ops.op("Bob")).ok, true);
	await ops.deop("Bob");
	await ops.kick("Bob");
	await ops.ban("Bob");
	await ops.unban("Bob");
	await ops.timeout("Bob", 5);
	assert.deepEqual(
		calls.map((c) => c.req),
		[
			{ action: "add_mod", character: "Bob" },
			{ action: "remove_mod", character: "Bob" },
			{ action: "kick", character: "Bob" },
			{ action: "ban", character: "Bob" },
			{ action: "unban", character: "Bob" },
			{ action: "timeout", character: "Bob", length: 5 },
		],
	);
	assert.equal(calls.every((c) => c.session === "Vix"), true);
	assert.deepEqual(calls[0].ref, { kind: "room", id: "ADH-x" });
	assert.deepEqual(
		results.map((r) => r.action),
		["op", "deop", "kick", "ban", "unban", "timeout"],
	);
	assert.equal(results.every((r) => r.error === null), true);
});

test("roomOps surfaces a rejected action", async () => {
	const { store } = live();
	const actions = { roomAdmin: async () => "not an operator" };
	const ops = roomOps(store, actions, "Vix", conv);
	const result = await ops.kick("Bob");
	assert.equal(result.ok, false);
	assert.equal(result.error, "not an operator");
});

test("roomOps rejects a sub-minute timeout before dispatch", async () => {
	const { store } = live();
	const calls = [];
	const results = [];
	const actions = {
		roomAdmin: async () => {
			calls.push(1);
			return null;
		},
	};
	const ops = roomOps(store, actions, "Vix", conv, (action, name, error) =>
		results.push({ action, name, error }),
	);
	const result = await ops.timeout("Bob", 0);
	assert.equal(result.ok, false);
	assert.match(result.error, /one minute/);
	assert.equal(calls.length, 0);
	assert.equal(results.length, 1);
	assert.equal(results[0].error, result.error);
});

test("roomOps exposes capabilities for its context", () => {
	const { store } = live();
	const actions = { roomAdmin: async () => null };
	const ops = roomOps(store, actions, "Vix", conv);
	assert.equal(ops.capabilities(member("Bob")).op, true);
	assert.equal(ops.capabilities(member("Vix")).op, false);
});

test("conversationContext reads self and global-admin status from the store", () => {
	const { store } = live();
	store.sessions.Vix.self.admin = true;
	const context = conversationContext(store, conv);
	assert.equal(context.self, "Vix");
	assert.equal(context.selfAdmin, true);
	assert.equal(canManageRoom(context), true);
});