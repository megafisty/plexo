// conversations.ts — conversation-kind predicates and the active-conversation
// lookup. Pure: the predicates read only the argument and the lookup takes the
// Store/View as arguments, so no feature folder owns the definition and the
// callers that branch on a conversation's kind cannot drift apart.

import type { Conversation, Store, View } from "../store/state.js";
import type { ConvKind } from "../transport/enums.js";

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
