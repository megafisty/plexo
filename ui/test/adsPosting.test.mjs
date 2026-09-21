import { test } from "bun:test";
import assert from "node:assert/strict";

import {
	adBody,
	assignAdToAll,
	channelAllowsAds,
	channelKey,
	deleteAd,
	emptyCampaign,
	findTarget,
	mergeAvailable,
	nextEligibleLabel,
	normalizeCampaign,
	removeChannel,
	saveAd,
	setChannelAd,
} from "../src/components/ads/posting.js";
import { formatClock } from "../src/lib/format.js";

const official = (id, ads = []) => ({ kind: "official", id, name: id, ads });

test("normalizeCampaign fills the collections the wire omits", () => {
	const c = normalizeCampaign({ enabled: true, ads: [{ name: "a", body: "b" }] });
	assert.deepEqual(c, { enabled: true, ads: [{ name: "a", body: "b" }], channels: [] });
	assert.deepEqual(normalizeCampaign(undefined), emptyCampaign());
});

test("mergeAvailable adds only candidates not already present", () => {
	const campaign = { enabled: true, ads: [], channels: [official("Existing")] };
	const merged = mergeAvailable(campaign, [
		{ kind: "official", id: "Existing", name: "Existing" },
		{ kind: "official", id: "New", name: "New" },
	]);
	assert.deepEqual(
		merged.channels.map((c) => c.id),
		["Existing", "New"],
	);
	assert.deepEqual(merged.channels[1].ads, []);
	// No additions keeps the same object.
	assert.equal(
		mergeAvailable(campaign, [{ kind: "official", id: "Existing", name: "Existing" }]),
		campaign,
	);
});

test("channelKey is case-insensitive on the id", () => {
	assert.equal(channelKey("official", "FrontPage"), channelKey("official", "frontpage"));
	assert.notEqual(channelKey("official", "A"), channelKey("room", "A"));
});

test("saveAd upserts by name, appends a new name, and rejects empty input", () => {
	let res = saveAd([], " Intro ", " hello ");
	assert.equal(res.error, undefined);
	assert.deepEqual(res.ads, [{ name: "Intro", body: "hello" }]);
	assert.equal(res.selected, "Intro");

	// Saving an existing name overwrites its body in place, keeping canonical case.
	res = saveAd(res.ads, "intro", "second");
	assert.deepEqual(res.ads, [{ name: "Intro", body: "second" }]);

	// A name that does not already exist appends a new ad.
	res = saveAd(res.ads, "Renamed", "third");
	assert.deepEqual(res.ads, [
		{ name: "Intro", body: "second" },
		{ name: "Renamed", body: "third" },
	]);

	assert.equal(saveAd(res.ads, "", "x").error, "Give the ad a name.");
	assert.equal(saveAd(res.ads, "x", "  ").error, "The ad body is empty.");
});

test("deleteAd removes the body and every channel reference", () => {
	const campaign = {
		enabled: true,
		ads: [
			{ name: "keep", body: "k" },
			{ name: "drop", body: "d" },
		],
		channels: [official("A", ["drop"]), official("B", ["keep", "drop"])],
	};
	const next = deleteAd(campaign, "DROP");
	assert.deepEqual(next.ads, [{ name: "keep", body: "k" }]);
	assert.deepEqual(next.channels[0].ads, []);
	assert.deepEqual(next.channels[1].ads, ["keep"]);
});

test("assignAdToAll, setChannelAd, and removeChannel rewrite assignments", () => {
	const campaign = { enabled: true, ads: [], channels: [official("A"), official("B", ["x"])] };
	const all = assignAdToAll(campaign, "intro");
	assert.deepEqual(all.channels.map((c) => c.ads), [["intro"], ["intro"]]);

	const set = setChannelAd(campaign, official("b"), "intro");
	assert.deepEqual(set.channels[0].ads, []);
	assert.deepEqual(set.channels[1].ads, ["intro"]);

	const cleared = setChannelAd(campaign, official("B"), null);
	assert.deepEqual(cleared.channels[1].ads, []);

	const removed = removeChannel(campaign, official("A"));
	assert.deepEqual(removed.channels.map((c) => c.id), ["B"]);
});

test("adBody resolves case-insensitively", () => {
	assert.equal(adBody([{ name: "Intro", body: "hi" }], "intro"), "hi");
	assert.equal(adBody([], "intro"), "");
	assert.equal(adBody([{ name: "Intro", body: "hi" }], null), "");
});

test("findTarget and channelAllowsAds use live status", () => {
	const ch = official("Frontpage");
	const status = {
		running: true,
		enabled: true,
		targets: [{ kind: "official", id: "frontpage", state: "skipped", reason: "chat-only" }],
	};
	const target = findTarget(status, ch);
	assert.equal(target?.reason, "chat-only");
	assert.equal(findTarget(undefined, ch), undefined);

	assert.equal(channelAllowsAds(target), false, "core chat-only disables the selector");
	assert.equal(channelAllowsAds(undefined), true, "an unknown target allows ads");
	assert.equal(
		channelAllowsAds({ kind: "official", id: "Frontpage", state: "active" }),
		true,
	);
});

test("nextEligibleLabel shows the clock time, now, or a skip reason", () => {
	const future = new Date(Date.now() + 5 * 60 * 1000).toISOString();
	assert.equal(
		nextEligibleLabel({ kind: "official", id: "A", state: "active", nextEligibleAt: future }, true, Date.now()),
		formatClock(Date.parse(future)),
	);
	assert.equal(
		nextEligibleLabel({ kind: "official", id: "A", state: "active" }, true, Date.now()),
		"now",
	);
	const past = new Date(Date.now() - 60_000).toISOString();
	assert.equal(
		nextEligibleLabel({ kind: "official", id: "A", state: "active", nextEligibleAt: past }, true, Date.now()),
		"now",
	);
	assert.equal(
		nextEligibleLabel({ kind: "official", id: "A", state: "skipped", reason: "not-joined" }, true, Date.now()),
		"not joined",
	);
	assert.equal(nextEligibleLabel(undefined, false, Date.now()), "—");
	assert.equal(
		nextEligibleLabel({ kind: "official", id: "A", state: "disabled", reason: "chat-only" }, true, Date.now()),
		"—",
	);
});
