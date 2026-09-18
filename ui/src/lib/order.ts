// order.ts — ordering helpers: one collator for case-insensitive text, and
// the conversation title/order/split shared by the sidebar and keyboard
// navigation. Absorbs collate.ts and convorder.ts.

import type { Conversation } from "../store/state.js";


// ==========================================================================
// collate.ts
// ==========================================================================
// Case-insensitive text ordering, shared by every sorted list.
//
// `String.prototype.localeCompare(b, undefined, { sensitivity: "base" })`
// reads fine but is pathologically slow when called from a sort comparator:
// engines re-parse the options and rebuild a collator on *every* comparison.
// Measured in V8, sorting 200 names that way costs ~23 ms at 6x throttle
// versus ~0.9 ms for a single reused Intl.Collator (and ~0.25 ms for plain
// localeCompare). The sidebar and Alt+arrow navigation sort on every
// conversation change, so this is the hot comparator.
//
// One collator is built once at module load. Engines without Intl (or where
// the constructor throws) fall back to a lowercased localeCompare, which keeps
// the comparison case-insensitive without the per-call options object.
const collator: Intl.Collator | null = (() => {
	try {
		if (typeof Intl !== "undefined" && typeof Intl.Collator === "function") {
			return new Intl.Collator(undefined, { sensitivity: "base" });
		}
	} catch {
		// fall through to the localeCompare fallback
	}
	return null;
})();

/** compareText orders two strings case-insensitively, for use as an Array.sort
 * comparator. It is the one sanctioned replacement for an options-carrying
 * localeCompare in a comparator. */
export function compareText(a: string, b: string): number {
	if (collator !== null) {
		return collator.compare(a, b);
	}
	return a.toLowerCase().localeCompare(b.toLowerCase());
}

// ==========================================================================
// convorder.ts
// ==========================================================================
// Conversation ordering shared by the sidebar (which splits it into its two
// blocks) and keyboard navigation (which walks it as one list). Pure: it reads
// only the conversation records and never the store or view, so both callers
// agree on exactly the order the user sees.

/** convTitle is a conversation's displayed name: its title when the core has
 * sent one, otherwise its id. */
export function convTitle(conv: { title?: string; conv: { id: string } }): string {
	return conv.title !== undefined && conv.title !== ""
		? conv.title
		: conv.conv.id;
}

/** byTitle orders by the displayed name, case-insensitively, so the order is
 * stable regardless of message activity. compareText reuses one collator; see
 * collate.ts for why an options-carrying localeCompare is not used here. */
function byTitle(a: Conversation, b: Conversation): number {
	return compareText(convTitle(a), convTitle(b));
}

/** splitConversations partitions a session's conversations into the sidebar's
 * three stable blocks — channels/rooms, direct messages, and warp panes — each
 * sorted by title. A warp pane is a read-only virtual conversation. */
export function splitConversations(
	per: Record<string, Conversation> | undefined,
): { channels: Conversation[]; dms: Conversation[]; warps: Conversation[] } {
	const all = Object.values(per ?? {});
	return {
		channels: all
			.filter((c) => c.conv.kind !== "dm" && c.conv.kind !== "warp")
			.sort(byTitle),
		dms: all.filter((c) => c.conv.kind === "dm").sort(byTitle),
		warps: all.filter((c) => c.conv.kind === "warp").sort(byTitle),
	};
}

/** orderedConversations flattens the sidebar into the order the user sees:
 * channels/rooms, then direct messages, then warp panes. Ctrl+Tab cycling
 * follows it, so a visible pane is always reachable by cycle. */
export function orderedConversations(
	per: Record<string, Conversation> | undefined,
): Conversation[] {
	const { channels, dms, warps } = splitConversations(per);
	return channels.concat(dms, warps);
}
