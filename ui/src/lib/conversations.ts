// conversations.ts — conversation-kind predicates and the active-conversation
// lookup. Pure: the predicates read only the argument and the lookup takes the
// Store/View as arguments, so no feature folder owns the definition and the
// callers that branch on a conversation's kind cannot drift apart.

import type { Conversation, Store, View } from "../store/state.js";
import type { ConvKind } from "../transport/enums.js";
import type { ConvRef } from "../transport/protocol.js";

/** isChannelKind reports whether a conversation kind is an official channel or
 * a room: the two kinds with a live member roster, room moderation, and a
 * join/leave lifecycle. DMs, broadcasts, and warp panes are not channels. */
export function isChannelKind(kind: ConvKind): kind is "official" | "room" {
	return kind === "official" || kind === "room";
}

/** isMemberConv reports whether a conversation carries a live member roster
 * (an official channel or a room), tolerating an absent conversation. */
export function isMemberConv(conv: Conversation | undefined): boolean {
	return conv !== undefined && isChannelKind(conv.conv.kind);
}

/** activeConv resolves the reporting session's active conversation, or
 * undefined when there is no active session or conversation. */
export function activeConv(store: Store, view: View): Conversation | undefined {
	const session = view.activeSession;
	if (session === null) {
		return undefined;
	}
	const key = view.activeConv[session];
	return key === undefined ? undefined : store.conversations[session]?.[key];
}

/** RoomVisibility is a room's published state as far as the client can tell.
 * `unknown` is a first-class answer: absence from the catalog only proves the
 * room is closed once the open-room (ORS) list has actually loaded, so before
 * that the state is unknown rather than assumed private. */
export type RoomVisibility = "public" | "private" | "unknown";

/** roomVisibility derives a channel's or room's published state from the
 * core-wide catalog. Official channels are always public. A room is public iff
 * it is in the open-room list; it is private only once that list has loaded
 * (`channels.loaded`), so an unknown room is never silently reported as
 * private. The catalog is a set-to snapshot refreshed on a TTL, so a room
 * opened by someone else after the last refresh can read stale until the next
 * one; that is the accepted limit of this client-side derivation. */
export function roomVisibility(store: Store, conv: ConvRef): RoomVisibility {
	if (conv.kind === "official") {
		return "public";
	}
	if (conv.kind !== "room") {
		return "unknown";
	}
	if (!store.channels.loaded) {
		return "unknown";
	}
	const id = conv.id.toLowerCase();
	return store.channels.rooms.some((room) => room.name.toLowerCase() === id)
		? "public"
		: "private";
}
