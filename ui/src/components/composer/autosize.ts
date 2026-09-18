import { isMsgPinned } from "../../store/state.js";
import type { View } from "../../store/state.js";
// Composer autosizing and resize tracking, extracted from Composer.ts so the
// component stays focused on drafts/sending/formatting. The measurement model
// is subtle, so it is documented here in full:
//
// Autosizing is the one place a keystroke can force layout, so it is layered:
//
//   * Where the engine sizes a textarea from its content (CSS `field-sizing`,
//     which the vendored normalize already applies to every textarea; the
//     composer asserts it too), the box is sized by normal layout and script
//     does nothing at all -- no measurement, no height write. NATIVE_AUTOSIZE
//     detects this and every scripted height path is skipped.
//   * Elsewhere the box is measured at `height:auto` (naturalHeight) and only
//     rewritten when the value actually changes. A keystroke that does not
//     change the height never touches the box, so it cannot force a reflow of
//     the conversation pane.
//
// The measurement is deliberately not taken on every keystroke. Two facts make
// the obvious shortcuts unsafe:
//
//   * `scrollHeight` on a fixed-height textarea is floored at the box height,
//     so a *shrink* is invisible without the `auto` reset; and
//   * in the overflow state the vertical scrollbar narrows the content box and
//     re-wraps the text, so the raw `scrollHeight` *overstates* growth.
//
// So the overflow reading is used only as a trigger, never as a height:
//
//   * content overflows -> measure at `auto` and write (exact and wrap-safe),
//                          unless the box already sits at its CSS ceiling, where
//                          it cannot grow and the measure is skipped;
//   * content fits      -> the box may now be too tall (a deletion or a re-wrap)
//                          but confirming that needs the `auto` measurement, so
//                          it is deferred to the trailing settle. A burst of
//                          keystrokes therefore never pays for it.
//
// The settle also runs on blur/unmount and after a resize, so the box always
// converges; only shrink lags, and by no more than SHRINK_SETTLE_MS.

/** NATIVE_AUTOSIZE is true when the engine sizes a textarea to its content
 * (CSS `field-sizing`). The vendored normalize sets it on every textarea and we
 * assert it on .composer-input as well, so the behaviour is intentional rather
 * than an inherited accident. Where it holds we never measure or write a
 * height: the engine's own layout pass does the work, and a keystroke costs no
 * scripted layout read at all. Detected once; the JS autosizer below is the
 * fallback for engines that lack it (including the QtWebKit target). */
export const NATIVE_AUTOSIZE =
	typeof CSS !== "undefined" &&
	typeof CSS.supports === "function" &&
	CSS.supports("field-sizing", "content");

/** HAS_RO is true when ResizeObserver exists. It drives timeline re-pinning on
 * modern engines; on old ones the synchronous paths re-pin instead. */
const HAS_RO = typeof ResizeObserver !== "undefined";

/** SHRINK_SETTLE_MS is the idle slack before the deferred shrink measurement.
 * Long enough that a backspace burst measures once instead of per key, short
 * enough that a too-tall box is never visibly wrong. */
const SHRINK_SETTLE_MS = 200;

/** AutosizeState is the autosize/resize slice of the composer's state. The
 * composer's own state extends it. */
export interface AutosizeState {
	/** el is the textarea being sized, refreshed at mount. */
	el?: HTMLTextAreaElement;
	/** border is the textarea's top+bottom border width, minH/maxH its CSS
	 * height floor/ceiling; all cached by measureMetrics. */
	border: number;
	minH: number;
	maxH: number;
	/** floor is the height no content can shrink below: the larger of the CSS
	 * min-height and the rows=2 intrinsic height. Measured once on mount (the
	 * shrink gate uses it to skip while the box already sits at its floor). */
	floor: number;
	/** appliedH is the border-box height last written to the textarea, or
	 * undefined before the first measurement. It is compared against the
	 * overflow reading so an unchanged height is never rewritten. */
	appliedH?: number;
	/** settle is the pending trailing shrink measurement, if any. */
	settle?: number;
	/** refView keeps the current View reachable from the resize hooks, which
	 * fire outside a render; lastW lets them spot a width change. ro/onWinResize
	 * are torn down by untrackResize. */
	refView?: View;
	lastW?: number;
	ro?: ResizeObserver;
	onWinResize?: () => void;
}

/** activeTarget returns the conversation the hooks should act on, read from the
 * View reference captured each render. The resize hooks fire outside a render,
 * so they cannot close over the current session/key directly. */
function activeTarget(
	state: AutosizeState,
): { view: View; session: string; key: string } | null {
	const view = state.refView;
	if (view === undefined) {
		return null;
	}
	const session = view.activeSession;
	if (session === null) {
		return null;
	}
	const key = view.activeConv[session];
	if (key === undefined) {
		return null;
	}
	return { view, session, key };
}

/** measureMetrics caches the textarea's border width and its CSS height
 * floor/ceiling. getComputedStyle resolves 3.5rem/50vh to px and reports
 * "none" (NaN) for an unbounded max-height. Re-run after a viewport resize
 * because the 50vh cap moves with the window. */
export function measureMetrics(el: HTMLTextAreaElement, state: AutosizeState): void {
	const cs = getComputedStyle(el);
	// clientHeight excludes borders, so this recovers the border-box delta.
	state.border = el.offsetHeight - el.clientHeight;
	state.minH = parseFloat(cs.minHeight) || 0;
	const max = parseFloat(cs.maxHeight);
	state.maxH = Number.isNaN(max) ? Infinity : max;
}

/** clampHeight keeps a measured content height inside the CSS floor/ceiling.
 * The ceiling matters: once content passes 50vh the box stops growing, so an
 * overflowing measurement must not keep being written. */
function clampHeight(h: number, state: AutosizeState): number {
	return Math.min(Math.max(h, state.minH), state.maxH);
}

/** naturalHeight measures the textarea's content height, clamped, by briefly
 * letting it size to its content. Measuring at `auto` (rather than reading the
 * fixed-height box) is what makes the result correct: a fixed-height textarea
 * floors scrollHeight at its own box height, and in the overflow state its
 * scrollbar narrows the content box and re-wraps the text. The caller must
 * write the result back, since the inline height is left at "auto". */
export function naturalHeight(el: HTMLTextAreaElement, state: AutosizeState): number {
	el.style.height = "auto";
	return clampHeight(el.scrollHeight + state.border, state);
}

/** writeHeight records and applies a border-box height. State is updated first
 * so a later comparison never sees a stale value. */
function writeHeight(el: HTMLTextAreaElement, state: AutosizeState, h: number): void {
	state.appliedH = h;
	el.style.height = `${h}px`;
}

/** autosize keeps the textarea at its content height using as little layout as
 * possible. See the file header for the measurement model. */
export function autosize(el: HTMLTextAreaElement, state: AutosizeState): void {
	if (NATIVE_AUTOSIZE) {
		return;
	}
	const prev = state.appliedH;
	if (prev === undefined) {
		// First measurement after mount: no previous height to compare against.
		writeHeight(el, state, naturalHeight(el, state));
		maybeRepin(el, state);
		return;
	}
	// Overflow is only a trigger, never the height itself: in that state the
	// scrollbar has narrowed the content box and re-wrapped the text, so the raw
	// scrollHeight would overstate the result. naturalHeight() measures at auto.
	if (el.scrollHeight > prev - state.border) {
		// At the CSS ceiling the box cannot grow: naturalHeight() would clamp
		// straight back to maxH after an auto-measure (two extra layout passes),
		// and writing that same height would be a no-op. Skip both, so a post
		// parked at max-height does not pay a full re-measure per keystroke.
		// scrollHeight is an integer and can exceed the content box by a pixel
		// even when the text fits, so only skip while it *clearly* overflows;
		// the near-fit case falls through and the auto-measure still shrinks it.
		if (prev >= state.maxH && el.scrollHeight > el.clientHeight + 1) {
			return;
		}
		writeHeight(el, state, naturalHeight(el, state));
		maybeRepin(el, state);
		return;
	}
	// Content fits the box. It may now be too tall (a deletion or a re-wrap),
	// but a fixed-height textarea hides shrinking, so defer the confirming
	// measurement -- a burst of keystrokes never pays for it.
	if (prev > state.floor) {
		scheduleSettle(el, state);
	}
}

/** scheduleSettle (re)arms the trailing shrink measurement. */
function scheduleSettle(el: HTMLTextAreaElement, state: AutosizeState): void {
	if (state.settle !== undefined) {
		window.clearTimeout(state.settle);
	}
	state.settle = window.setTimeout(() => {
		state.settle = undefined;
		settleShrink(el, state);
	}, SHRINK_SETTLE_MS);
}

/** settleNow runs the pending settle immediately (blur, send). */
export function settleNow(state: AutosizeState): void {
	if (state.settle !== undefined) {
		window.clearTimeout(state.settle);
		state.settle = undefined;
	}
	if (state.el !== undefined) {
		settleShrink(state.el, state);
	}
}

/** settleShrink is the only path that can shrink the box: it takes the auto
 * measurement once the keyboard has paused (or on blur/send/resize) and writes
 * only when the height really changed. It never runs before the first
 * measurement or while the box already sits at its floor, where shrinking is
 * impossible. */
function settleShrink(el: HTMLTextAreaElement, state: AutosizeState): void {
	const prev = state.appliedH;
	if (prev === undefined || prev <= state.floor) {
		return;
	}
	const next = naturalHeight(el, state);
	if (next === prev) {
		el.style.height = `${prev}px`; // naturalHeight left it at "auto"
		return;
	}
	writeHeight(el, state, next);
	maybeRepin(el, state);
}

/** repin returns the timeline to its live edge when the composer grew while the
 * view was pinned to the bottom, so the newest message stays visible. */
function repin(el: HTMLTextAreaElement, state: AutosizeState): void {
	const target = activeTarget(state);
	if (target === null || !isMsgPinned(target.view, target.session, target.key)) {
		return;
	}
	const list = el.closest(".conversation-pane")?.querySelector(".message-list");
	if (list !== null && list !== undefined) {
		(list as HTMLElement).scrollTop = (list as HTMLElement).scrollHeight;
	}
}

/** maybeRepin re-pins synchronously only when no ResizeObserver will do it. */
function maybeRepin(el: HTMLTextAreaElement, state: AutosizeState): void {
	if (HAS_RO) {
		return; // the observer re-pins on every size change, without a forced read
	}
	repin(el, state);
}

/** trackResize re-measures after the box width or the viewport changes.
 * Wrapping depends on the available width, so a resize invalidates the cached
 * content height, and the 50vh ceiling moves with the viewport height.
 *
 * The ResizeObserver catches pane-width changes (a sidebar toggle, say). Its
 * callback must not resize the observed textarea synchronously -- that is what
 * trips "ResizeObserver loop completed with undelivered notifications" -- so it
 * only schedules the re-measure and re-pins (a read-only scroll). Engines
 * without ResizeObserver (QtWebKit) fall back to the window resize listener; a
 * pane-only width change there is corrected by the next keystroke. */
export function trackResize(el: HTMLTextAreaElement, state: AutosizeState): void {
	const onWinResize = (): void => {
		measureMetrics(el, state);
		scheduleSettle(el, state);
		repin(el, state);
	};
	window.addEventListener("resize", onWinResize);
	state.onWinResize = onWinResize;

	if (!HAS_RO) {
		return;
	}
	state.lastW = el.clientWidth;
	state.ro = new ResizeObserver(() => {
		if (el.clientWidth !== state.lastW) {
			state.lastW = el.clientWidth;
			measureMetrics(el, state);
			scheduleSettle(el, state);
		}
		repin(el, state);
	});
	state.ro.observe(el);
}

/** untrackResize tears down everything trackResize installed plus any pending
 * settle. Call from the component's onremove. */
export function untrackResize(state: AutosizeState): void {
	if (state.settle !== undefined) {
		window.clearTimeout(state.settle);
		state.settle = undefined;
	}
	state.ro?.disconnect();
	state.ro = undefined;
	if (state.onWinResize !== undefined) {
		window.removeEventListener("resize", state.onWinResize);
		state.onWinResize = undefined;
	}
}