import type { Entry, EntryWindow, Store } from "./state.js";
import { WINDOW } from "./state.js";
import type { Entry as WireEntry } from "../transport/protocol.js";
// The bounded timeline window: a contiguous slice of one conversation's
// entries, capped at WINDOW. apply.ts (live events) and commands.ts (history
// paging) share these helpers so bounds and trimming stay consistent.

/** toEntry maps a wire entry into the store's immutable Entry. `self` is true
 * for the account's own speaker: a live event carries it explicitly, a history
 * page derives it from the session. The wire time is epoch milliseconds. */
export function toEntry(e: WireEntry, self = false): Entry {
	return {
		id: e.id,
		convSeq: e.convSeq,
		kind: e.kind,
		speaker: e.speaker,
		html: e.html,
		time: e.createdAtMs,
		self,
	};
}

/** ensureWindow returns the conversation's window, creating an empty one. */
export function ensureWindow(
	store: Store,
	session: string,
	key: string,
): EntryWindow {
	const per = (store.entries[session] ??= {});
	return (per[key] ??= {
		items: [],
		hasOlder: false,
		hasNewer: false,
		rev: 0,
	});
}

/** refreshBounds recomputes a window's seq bounds and hasNewer from its items
 * and the live-edge high-water mark. Optimistic (in-flight send) entries carry
 * a sentinel seq and are ignored for the bounds. */
export function refreshBounds(win: EntryWindow): void {
	let oldest: number | undefined;
	let newest: number | undefined;
	for (const e of win.items) {
		if (e.send !== undefined) {
			continue; // optimistic copy; not a real server seq
		}
		if (oldest === undefined || e.convSeq < oldest) {
			oldest = e.convSeq;
		}
		if (newest === undefined || e.convSeq > newest) {
			newest = e.convSeq;
		}
	}
	win.oldestSeq = oldest;
	win.newestSeq = newest;
	win.hasNewer =
		win.liveSeq !== undefined &&
		(newest === undefined || win.liveSeq > newest);
}

/** trimWindow caps the window at WINDOW entries. A pinned (bottom) view keeps
 * the newest end and raises hasOlder; a detached (reading history) view keeps
 * the oldest end, leaving the dropped newer entries recoverable via loadNewer. */
export function trimWindow(win: EntryWindow, pinned: boolean): void {
	if (win.items.length > WINDOW) {
		if (pinned) {
			win.items = win.items.slice(win.items.length - WINDOW);
			win.hasOlder = true;
		} else {
			win.items = win.items.slice(0, WINDOW);
		}
	}
	refreshBounds(win);
}

/** insertLive places one live entry into the window, then re-trims. While
 * detached the newest end is trimmed, so a busy channel cannot evict the
 * history the user is reading. */
export function insertLive(
	win: EntryWindow,
	entry: Entry,
	pinned: boolean,
): void {
	if (entry.send === undefined) {
		win.liveSeq = Math.max(win.liveSeq ?? 0, entry.convSeq);
	}

	// Ignore anything at or below a materialized window's oldest bound: it was
	// dropped on purpose, not missed.
	if (
		win.oldestSeq !== undefined &&
		entry.convSeq < win.oldestSeq &&
		win.items.length > 0
	) {
		refreshBounds(win);
		return;
	}

	// Fast path: the common case is an in-order append.
	const last = win.items[win.items.length - 1];
	if (
		last === undefined ||
		entry.convSeq > last.convSeq ||
		(entry.convSeq === last.convSeq && entry.id.localeCompare(last.id) > 0)
	) {
		win.items.push(entry);
	} else {
		// Slow path: dedup and place in order (backfill or a late self echo).
		if (win.items.some((e) => e.id === entry.id)) {
			return;
		}
		win.items.push(entry);
		win.items.sort(
			(a, b) => a.convSeq - b.convSeq || a.id.localeCompare(b.id),
		);
	}
	win.rev++;
	trimWindow(win, pinned);
}

/** mergeHistory merges a fetched page into the window and re-trims. Returns
 * whether anything was added. */
export function mergeHistory(
	win: EntryWindow,
	entries: Entry[],
	pinned: boolean,
): boolean {
	const seen = new Set(win.items.map((e) => e.id));
	let added = false;
	for (const e of entries) {
		if (seen.has(e.id)) {
			continue;
		}
		seen.add(e.id);
		win.items.push(e);
		added = true;
	}
	if (added) {
		win.items.sort(
			(a, b) => a.convSeq - b.convSeq || a.id.localeCompare(b.id),
		);
		win.rev++;
		trimWindow(win, pinned);
	}
	return added;
}
