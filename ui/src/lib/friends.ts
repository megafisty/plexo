// friends.ts — the account-wide friend/bookmark union. The core sends the two
// lists split; the store keeps them separate. Views that only care about "is
// this character a contact?" (the friends popout, the Ctrl-P friends list,
// roster ranking) use unionFriends to get the deduplicated union they used to
// read directly from the store.
//
// The result is memoized on the two list references, so a Mithril view can key
// derived state (a roster sort, a popout order) on the returned identity and
// rebuild only when a list actually changes. A character that is both a friend
// and a bookmark appears once.

import type { MemberInfo } from "../transport/protocol.js";
import type { Store } from "../store/state.js";
import { compareText } from "./order.js";

let cachedFriends: readonly MemberInfo[] | null = null;
let cachedBookmarks: readonly MemberInfo[] | null = null;
let cachedUnion: MemberInfo[] = [];

/** unionFriends returns the deduplicated union of the account's friends and
 * bookmarks, sorted by name. A character that is both appears once (as the
 * friend record). The array reference is stable until either list changes. */
export function unionFriends(store: Store): MemberInfo[] {
	const { friends, bookmarks } = store;
	if (friends === cachedFriends && bookmarks === cachedBookmarks) {
		return cachedUnion;
	}
	const byName = new Map<string, MemberInfo>();
	for (const f of friends) {
		byName.set(f.name, f);
	}
	for (const b of bookmarks) {
		if (!byName.has(b.name)) {
			byName.set(b.name, b);
		}
	}
	cachedUnion = [...byName.values()].sort((a, b) =>
		compareText(a.name, b.name),
	);
	cachedFriends = friends;
	cachedBookmarks = bookmarks;
	return cachedUnion;
}

/** isBookmarked reports whether the account has the named character bookmarked,
 * per the online projection the core sends. The core withholds contacts it
 * cannot name authoritatively, so an offline bookmark is not represented here; a
 * false means "not known to be bookmarked", not "definitely not". */
export function isBookmarked(store: Store, name: string): boolean {
	const key = name.toLowerCase();
	return store.bookmarks.some((b) => b.name.toLowerCase() === key);
}