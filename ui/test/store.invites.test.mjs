import { test } from "node:test";
import assert from "node:assert/strict";

import { applyEnvelope } from "../app/store/apply.js";
import { closeInvites } from "../app/store/commands.js";
import { INVITES_KEY } from "../app/store/state.js";
import { batch, conv, live } from "./helpers.mjs";

// The invites list is a session-scoped set-to record. applyInvites mirrors it,
// deduplicates by room (the core already does, but the client keys on convKey
// too), and drives the visibility of the client-only virtual conversation: a
// new room key reopens a closed pane, an empty list closes it.

const invitesEvent = (session, invites) => ({
	session,
	kind: "state",
	payload: { key: `invites/${session}`, value: { invites } },
});

const roomInvite = (id, title, invitedBy) => ({
	conv: conv("room", id),
	title,
	invitedBy,
});

test("a pushed invite list is stored on the session", () => {
	const { store, view } = live();
	applyEnvelope(store, view, batch([
		invitesEvent("Vix", [roomInvite("ADH-secret", "Secret Lair", "Kira")]),
	]));

	assert.equal(store.sessions.Vix.invites.length, 1);
	assert.equal(store.sessions.Vix.invites[0].conv.id, "ADH-secret");
	assert.equal(view.invitesClosed.Vix, undefined);
});

test("a room key already present replaces rather than duplicates", () => {
	const { store, view } = live();
	applyEnvelope(store, view, batch([
		invitesEvent("Vix", [roomInvite("ADH-secret", "Secret Lair", "Kira")]),
	]));
	applyEnvelope(store, view, batch([
		invitesEvent("Vix", [roomInvite("ADH-secret", "Renamed Lair", "Kira")]),
	]));

	assert.equal(store.sessions.Vix.invites.length, 1);
	assert.equal(store.sessions.Vix.invites[0].title, "Renamed Lair");
});

test("a set-to re-emit does not reopen a closed invites conversation", () => {
	const { store, view } = live();
	const list = [roomInvite("ADH-secret", "Secret Lair", "Kira")];
	applyEnvelope(store, view, batch([invitesEvent("Vix", list)]));
	closeInvites(view, "Vix");
	assert.equal(view.invitesClosed.Vix, true);

	applyEnvelope(store, view, batch([invitesEvent("Vix", list)]));

	assert.equal(
		view.invitesClosed.Vix,
		true,
		"the same keys are not a new invitation",
	);
});

test("a newly arrived room key reopens a closed invites conversation", () => {
	const { store, view } = live();
	applyEnvelope(store, view, batch([
		invitesEvent("Vix", [roomInvite("ADH-one", "One", "Kira")]),
	]));
	closeInvites(view, "Vix");

	applyEnvelope(store, view, batch([
		invitesEvent("Vix", [
			roomInvite("ADH-one", "One", "Kira"),
			roomInvite("ADH-two", "Two", "Rin"),
		]),
	]));

	assert.equal(view.invitesClosed.Vix, undefined, "a new key reopens");
});

test("an empty list closes the pane, resets the flag, and deselects it", () => {
	const { store, view } = live();
	applyEnvelope(store, view, batch([
		invitesEvent("Vix", [roomInvite("ADH-secret", "Secret Lair", "Kira")]),
	]));
	view.activeConv.Vix = INVITES_KEY;
	closeInvites(view, "Vix");

	applyEnvelope(store, view, batch([invitesEvent("Vix", [])]));

	assert.deepEqual(store.sessions.Vix.invites, []);
	assert.equal(view.activeConv.Vix, undefined, "the empty pane is deselected");
	assert.equal(
		view.invitesClosed.Vix,
		undefined,
		"the flag resets so a later invite shows",
	);
});

test("a snapshot seeds pending invitations", () => {
	const { store, view } = live();
	applyEnvelope(store, view, {
		t: "snapshot",
		d: {
			sessions: [
				{
					character: "Vix",
					state: "live",
					self: { character: "Vix", online: true, admin: false },
					adCount: 0,
					conversations: [],
					invites: [roomInvite("ADH-secret", "Secret Lair", "Kira")],
				},
			],
		},
	});

	assert.equal(store.sessions.Vix.invites.length, 1);
});

test("session_lost drops the invites closed flag", () => {
	const { store, view } = live();
	view.invitesClosed.Vix = true;

	applyEnvelope(store, view, batch([
		{ session: "Vix", kind: "state", payload: { key: "session/Vix", removed: true } },
	]));

	assert.equal(view.invitesClosed.Vix, undefined);
});