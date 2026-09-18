import { fetchSearchResults } from "../api.js";
import type { SearchPayload } from "../transport/protocol.js";
import { sessionAlive, type Store } from "./state.js";
// Search cache helpers: the store-side half of the core's session-backed FKS
// results. The core announces a `search` notice carrying only a revision; the
// rows are pulled over HTTP and applied here. The notice handler (apply.ts) and
// the search dialog share these, so the revision rule lives in one place.

/** noteSearchRevision records a notice's revision without pulling rows. The
 * caller decides whether to fetch. */
export function noteSearchRevision(
	store: Store,
	session: string,
	revision: number,
): void {
	if (revision > (store.searchRevision[session] ?? 0)) {
		store.searchRevision[session] = revision;
	}
}

/** applySearchResults writes a fetched result set, ignoring one older than the
 * latest revision already known. */
export function applySearchResults(
	store: Store,
	session: string,
	payload: SearchPayload,
): void {
	if (payload.revision < (store.searchRevision[session] ?? 0)) {
		return;
	}
	if (!sessionAlive(store, session)) {
		return; // session closed while the pull was in flight
	}
	store.search[session] = payload.characters ?? [];
	store.searchRevision[session] = payload.revision;
}

/** recallSearch pulls a session's cached FKS result set and applies it. It is
 * safe to call at any time: an unknown session, a dropped request, or an
 * offline core leaves the store unchanged. */
export async function recallSearch(
	store: Store,
	session: string,
): Promise<void> {
	let payload: SearchPayload | null = null;
	try {
		payload = await fetchSearchResults(session);
	} catch {
		return;
	}
	if (payload !== null) {
		applySearchResults(store, session, payload);
	}
}