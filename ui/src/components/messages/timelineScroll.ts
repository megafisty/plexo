// timelineScroll.ts — the scroll/pin/defer/anchor state machine for MessageList.
//
// Pure decision functions plus the two DOM anchor helpers; MessageList wires
// them from its lifecycle hooks. Kept separate so the gating that keeps an
// unrelated redraw layout-free (the window-rev check, the deferred-frame
// check) can be unit-tested.

import type { EntryWindow } from "../../store/state.js";

/** PIN_MARGIN is how close to the bottom counts as pinned to the live edge. */
export const PIN_MARGIN = 60;

/** DEFER_ROW_THRESHOLD is the retained-window size above which a switch's rows
 * are withheld for one frame. Below it the mount is cheap enough that a
 * placeholder reads as flicker; above it, splitting the frame that acknowledges
 * the switch from the one that mounts the rows keeps a large channel from
 * painting the sidebar highlight, the header, and every row in a single long
 * frame. */
export const DEFER_ROW_THRESHOLD = 40;

/** isPinned reports whether the scroll container is at (or within PIN_MARGIN
 * of) the live edge. */
export function isPinned(
	scrollTop: number,
	clientHeight: number,
	scrollHeight: number,
): boolean {
	return scrollTop + clientHeight >= scrollHeight - PIN_MARGIN;
}

/** rowsStale reports whether the cached row vnodes no longer describe the
 * window. */
export function rowsStale(
	rowsWin: EntryWindow | undefined,
	rowsRev: number | undefined,
	win: EntryWindow,
): boolean {
	return rowsWin !== win || rowsRev !== win.rev;
}

/** shouldDeferRows reports whether a first sight of a heavy window should
 * withhold its rows for one frame. */
export function shouldDeferRows(ackShown: boolean, rowCount: number): boolean {
	return !ackShown && rowCount > DEFER_ROW_THRESHOLD;
}

/** ScrollAction is the post-render scroll fix-up MessageList must perform. */
export type ScrollAction = "restore-anchor" | "scroll-edge" | "none";

/** timelineScrollAction decides the post-render scroll fix-up. Restoring an
 * anchor outranks the live edge; the edge scroll happens only when the window
 * changed, so an unrelated redraw does not force a layout read of scrollHeight.
 * "Changed" means either a new EntryWindow object (a delta/full materialization
 * replaces the container, and its rev restarts at 0) or a bumped rev on the
 * same window; comparing rev alone would mistake a replaced window whose rev
 * restarted for the one already scrolled to. A deferred frame has no rows yet,
 * so scrolling there is meaningless and recording the window would suppress the
 * scroll once they land. */
export function timelineScrollAction(args: {
	hasAnchor: boolean;
	pinned: boolean;
	deferred: boolean;
	win: EntryWindow | undefined;
	scrolledWin: EntryWindow | undefined;
	rev: number | undefined;
	scrolledRev: number | undefined;
}): ScrollAction {
	if (args.hasAnchor) {
		return "restore-anchor";
	}
	if (
		args.pinned &&
		!args.deferred &&
		(args.win !== args.scrolledWin || args.rev !== args.scrolledRev)
	) {
		return "scroll-edge";
	}
	return "none";
}

/** AnchorState is the subset of MessageList's state the anchor helpers use. */
export interface AnchorState {
	anchorId?: string;
	anchorOffset: number;
}

/** captureAnchor records the first visible row and its offset from the top of
 * the scroll viewport, so restoreAnchor can hold it in place. */
export function captureAnchor(el: HTMLElement, state: AnchorState): void {
	const anchor = firstVisibleRow(el);
	if (anchor === null) {
		return;
	}
	state.anchorId = anchor.getAttribute("data-entry") ?? undefined;
	state.anchorOffset =
		anchor.getBoundingClientRect().top - el.getBoundingClientRect().top;
}

/** restoreAnchor scrolls so the anchored row sits at its recorded offset. */
export function restoreAnchor(el: HTMLElement, state: AnchorState): void {
	const id = state.anchorId;
	if (id === undefined) {
		return;
	}
	let target: HTMLElement | null = null;
	for (const row of el.querySelectorAll<HTMLElement>(".msg[data-entry]")) {
		if (row.getAttribute("data-entry") === id) {
			target = row;
			break;
		}
	}
	if (target === null) {
		return; // anchored row was trimmed; leave the scroll position alone
	}
	const top = el.getBoundingClientRect().top;
	const delta = target.getBoundingClientRect().top - top - state.anchorOffset;
	el.scrollTop += delta;
}

/** firstVisibleRow returns the topmost row at least partly inside the viewport. */
export function firstVisibleRow(el: HTMLElement): HTMLElement | null {
	const top = el.getBoundingClientRect().top;
	for (const row of el.querySelectorAll<HTMLElement>(".msg[data-entry]")) {
		if (row.getBoundingClientRect().bottom > top) {
			return row;
		}
	}
	return null;
}