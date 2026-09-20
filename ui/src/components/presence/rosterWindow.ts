// rosterWindow.ts — windowing math and DOM measurement for ChannelRoster.
//
// Split out from roster.ts so the two pieces that decide what to render (the
// virtual window) and how tall a row is (the measured stride) sit together and
// can be unit-tested without a DOM. The DOM readers live here too, but they are
// only reached from the list's oncreate/onupdate hooks, gated by
// syncRosterMeasurement so an unrelated redraw never forces a layout read.

/** VIRTUAL_MIN is the member count above which only the visible window is
 * rendered. Below it the full list is cheap enough (and keeps find-in-page
 * working). */
export const VIRTUAL_MIN = 120;

/** OVERSCAN is the extra rows rendered above and below the viewport, covering
 * the gap between a scroll event and the next redraw. */
export const OVERSCAN = 8;

/** DEFAULT_STRIDE seeds the window before the first row has been measured. */
export const DEFAULT_STRIDE = 28;

export interface WindowRange {
	start: number;
	end: number;
	virtual: boolean;
}

/** rosterWindow returns the half-open row window to render for a scroll
 * position. Below VIRTUAL_MIN every row renders; above it only the rows
 * intersecting the viewport plus OVERSCAN, with the measured stride (or
 * DEFAULT_STRIDE before the first measurement) driving the estimate. */
export function rosterWindow(
	total: number,
	scrollTop: number,
	stride: number,
	viewportH: number,
): WindowRange {
	if (total <= VIRTUAL_MIN) {
		return { start: 0, end: total, virtual: false };
	}
	const s = stride > 0 ? stride : DEFAULT_STRIDE;
	const first = Math.floor(scrollTop / s);
	const screen = viewportH > 0 ? viewportH : s * 12;
	const rowsPerScreen = Math.ceil(screen / s) + 1;
	const start = Math.max(0, first - OVERSCAN);
	let end = Math.min(total, first + rowsPerScreen + OVERSCAN);
	if (end <= start) {
		end = Math.min(total, start + 1);
	}
	return { start, end, virtual: true };
}

/** rosterStride derives a row's vertical pitch from the first one or two
 * rendered rows: the gap between two rows is the most portable measurement,
 * falling back to row height + computed gap when only one row is rendered. */
export function rosterStride(
	first: { height: number; top: number },
	second: { height: number; top: number } | undefined,
	gap: number,
): number {
	let stride = first.height + gap;
	if (second !== undefined) {
		const delta = second.top - first.top;
		if (delta > 0) {
			stride = delta;
		}
	}
	return stride;
}

/** RosterMeasureState is the subset of ChannelRoster's state the measurement
 * helpers own; ChannelRosterState satisfies it. */
export interface RosterMeasureState {
	scrollTop: number;
	viewportH: number;
	stride: number;
	winW: number;
	winH: number;
	resetScroll: boolean;
}

/** syncRosterMeasurement applies a pending scroll reset and re-measures only
 * when the window size changed (or before the first successful measurement).
 * Doing it on every redraw would force a layout read (clientHeight, row
 * offsets) right after Mithril's DOM writes; gating it keeps unrelated redraws
 * layout-free. Returns whether the measured inputs changed the window, so the
 * caller can request a redraw. */
export function syncRosterMeasurement(
	el: HTMLElement,
	state: RosterMeasureState,
	force: boolean,
): boolean {
	if (state.resetScroll) {
		el.scrollTop = 0;
		state.resetScroll = false;
	}
	if (
		force ||
		state.viewportH <= 0 ||
		state.stride <= 0 ||
		window.innerWidth !== state.winW ||
		window.innerHeight !== state.winH
	) {
		return measureRoster(el, state);
	}
	return false;
}

/** measureRoster records the scroll offset, viewport height, and row stride,
 * reporting whether any window input changed. */
function measureRoster(el: HTMLElement, state: RosterMeasureState): boolean {
	state.scrollTop = el.scrollTop;
	state.winW = window.innerWidth;
	state.winH = window.innerHeight;

	let changed = false;
	const h = el.clientHeight;
	if (h > 0 && h !== state.viewportH) {
		state.viewportH = h;
		changed = true;
	}
	// Spacers are also <li>, so measure between the first two actual rows.
	const rows = el.querySelectorAll<HTMLElement>(".roster-row");
	const first = rows[0];
	if (first !== undefined) {
		const second = rows[1];
		const stride = rosterStride(
			{ height: first.offsetHeight, top: first.offsetTop },
			second === undefined
				? undefined
				: { height: second.offsetHeight, top: second.offsetTop },
			rowGap(el),
		);
		if (stride > 0 && Math.abs(stride - state.stride) > 0.5) {
			state.stride = stride;
			changed = true;
		}
	}
	return changed;
}

/** rowGap reads the list's flex row gap in px, or 0 when the engine reports
 * "normal" or does not support flex gap (in which case layout has no gaps
 * either, so a bare row height is the correct stride). */
function rowGap(el: HTMLElement): number {
	const cs = window.getComputedStyle(el);
	const row = parseFloat(cs.getPropertyValue("row-gap"));
	if (!Number.isNaN(row)) {
		return row;
	}
	const gap = parseFloat(cs.getPropertyValue("gap"));
	return Number.isNaN(gap) ? 0 : gap;
}