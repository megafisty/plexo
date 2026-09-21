// posting.ts — pure draft transforms for the ads Post tab. The component owns
// the draft and its HTTP; these functions only map one campaign to the next, so
// the merge, upsert, assignment, and display rules are unit-testable without a
// DOM. See docs/settings.md (Advertisement campaigns).
import { formatClock } from "../../lib/format.js";
import type { AdBody, AdCampaign, AdChannel, AdTargetStatus, AdsStatus } from "../../transport/protocol.js";

/** CampaignDraft is a campaign with its collections made non-optional, so the
 * editor never has to guard them. It is assignable to the wire AdCampaign on
 * save. */
export interface CampaignDraft {
	enabled: boolean;
	ads: AdBody[];
	channels: AdChannel[];
}

/** emptyCampaign is the draft for a character with no saved campaign. */
export function emptyCampaign(): CampaignDraft {
	return { enabled: false, ads: [], channels: [] };
}

/** normalizeCampaign copies a fetched campaign into an editable draft, filling
 * the fields the wire omits. */
export function normalizeCampaign(c: AdCampaign | undefined | null): CampaignDraft {
	if (c === undefined || c === null) {
		return emptyCampaign();
	}
	return {
		enabled: c.enabled === true,
		ads: (c.ads ?? []).map((a) => ({ name: a.name, body: a.body })),
		channels: (c.channels ?? []).map((ch) => ({
			kind: ch.kind,
			id: ch.id,
			name: ch.name ?? ch.id,
			ads: [...(ch.ads ?? [])],
		})),
	};
}

/** channelKey folds a conversation kind and id for case-insensitive matching;
 * F-Chat lowercases channel lookups, so casing must not fork a row. */
export function channelKey(kind: string, id: string): string {
	return `${kind}:${id.toLowerCase()}`;
}

/** mergeAvailable returns a draft whose channel list also contains every
 * candidate channel from the core that is not already present. The core decides
 * candidacy (joined and currently allows ads), so a chat-only channel never
 * appears here; a channel already in the campaign is left untouched, so a saved
 * assignment or a dead entry survives. */
export function mergeAvailable(campaign: CampaignDraft, available: AdChannel[]): CampaignDraft {
	const seen = new Set(campaign.channels.map((ch) => channelKey(ch.kind, ch.id)));
	const added: AdChannel[] = [];
	for (const ch of available) {
		const key = channelKey(ch.kind, ch.id);
		if (seen.has(key)) {
			continue;
		}
		seen.add(key);
		added.push({
			kind: ch.kind,
			id: ch.id,
			name: ch.name ?? ch.id,
			ads: [...(ch.ads ?? [])],
		});
	}
	if (added.length === 0) {
		return campaign;
	}
	return { ...campaign, channels: [...campaign.channels, ...added] };
}

/** findTarget returns the live status for one campaign channel, or undefined
 * when the core reports none (not running, or the channel was never a target). */
export function findTarget(status: AdsStatus | undefined, ch: AdChannel): AdTargetStatus | undefined {
	if (status === undefined) {
		return undefined;
	}
	const key = channelKey(ch.kind, ch.id);
	return (status.targets ?? []).find((t) => channelKey(t.kind, t.id) === key);
}

/** channelAllowsAds reports whether a channel's ad selector should be shown. The
 * core is authoritative: a target it rejected as chat-only gets the note in
 * place of the selector. A candidate channel is never chat-only, so it never
 * reaches the campaign with that reason; an unknown target is treated as
 * allowing ads. */
export function channelAllowsAds(target: AdTargetStatus | undefined): boolean {
	return !(target !== undefined && target.reason === "chat-only");
}

/** nextEligibleLabel renders the Next eligible cell. It is the wall-clock time
 * the scheduler may next post to the channel ("now" when it is already due),
 * or a short reason when the channel is skipped. */
export function nextEligibleLabel(target: AdTargetStatus | undefined, running: boolean, now: number): string {
	if (!running || target === undefined) {
		return "—";
	}
	switch (target.state) {
		case "disabled":
			return "—";
	}
	switch (target.reason) {
		case "not-joined":
			return "not joined";
		case "no-ad":
			return "no ad";
		case "too-long":
			return "ad too long";
	}
	const at = target.nextEligibleAt;
	if (at === undefined || at === "") {
		return "now";
	}
	const ms = Date.parse(at);
	if (Number.isNaN(ms) || ms <= now) {
		return "now";
	}
	return formatClock(ms);
}

/** adBody returns the body of the named ad, or "" when it is not in the draft. */
export function adBody(ads: AdBody[], name: string | null): string {
	if (name === null) {
		return "";
	}
	const lc = name.toLowerCase();
	return ads.find((a) => a.name.toLowerCase() === lc)?.body ?? "";
}

/** AdSaveResult is the outcome of upserting a named body. `error` is set on a
 * rejected edit, in which case the ads are unchanged. */
export interface AdSaveResult {
	ads: AdBody[];
	selected: string;
	error?: string;
}

/** saveAd upserts a named body: a name that matches an existing ad
 * (case-insensitively) overwrites its body in place, any other name appends a
 * new ad. An empty name or body is rejected. */
export function saveAd(ads: AdBody[], name: string, body: string): AdSaveResult {
	const trimmedName = name.trim();
	const trimmedBody = body.trim();
	if (trimmedName === "") {
		return { ads, selected: "", error: "Give the ad a name." };
	}
	if (trimmedBody === "") {
		return { ads, selected: "", error: "The ad body is empty." };
	}
	const lc = trimmedName.toLowerCase();
	const existing = ads.find((a) => a.name.toLowerCase() === lc);
	if (existing !== undefined) {
		return {
			ads: ads.map((a) =>
				a === existing ? { name: existing.name, body: trimmedBody } : a,
			),
			selected: existing.name,
		};
	}
	return { ads: [...ads, { name: trimmedName, body: trimmedBody }], selected: trimmedName };
}

/** deleteAd removes a named body and every channel reference to it, so the
 * saved document never contains a dangling reference. */
export function deleteAd(campaign: CampaignDraft, name: string): CampaignDraft {
	const lc = name.toLowerCase();
	return {
		...campaign,
		ads: campaign.ads.filter((a) => a.name.toLowerCase() !== lc),
		channels: campaign.channels.map((ch) => ({
			...ch,
			ads: (ch.ads ?? []).filter((r) => r.toLowerCase() !== lc),
		})),
	};
}

/** assignAdToAll sets every channel's assignment to the named ad. */
export function assignAdToAll(campaign: CampaignDraft, name: string): CampaignDraft {
	return {
		...campaign,
		channels: campaign.channels.map((ch) => ({ ...ch, ads: [name] })),
	};
}

/** setChannelAd sets one channel's single ad assignment; null clears it. */
export function setChannelAd(campaign: CampaignDraft, target: AdChannel, name: string | null): CampaignDraft {
	const key = channelKey(target.kind, target.id);
	return {
		...campaign,
		channels: campaign.channels.map((ch) =>
			channelKey(ch.kind, ch.id) === key ? { ...ch, ads: name === null ? [] : [name] } : ch,
		),
	};
}

/** removeChannel drops one channel from the campaign. */
export function removeChannel(campaign: CampaignDraft, target: AdChannel): CampaignDraft {
	const key = channelKey(target.kind, target.id);
	return {
		...campaign,
		channels: campaign.channels.filter((ch) => channelKey(ch.kind, ch.id) !== key),
	};
}
