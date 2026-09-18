// render.ts — redraw and subtree-skip toolkit: request() schedules the one
// redraw per frame, and memo()/pure() are the two sanctioned ways to cut a
// subtree out of that walk. Absorbs the former scheduler.ts.

import type * as Mithril from "mithril";
import m from "./mithril.js";

// ==========================================================================
// render.ts
// ==========================================================================
// render.ts — the two sanctioned ways to make Mithril skip work, and nothing
// else. Mithril's redraw coalesces to one render per frame, but a full render
// still walks the whole vnode tree; these are the only two places a subtree is
// cut out of that walk:
//
//   * memo() — a *parent* caches a built vnode (or any value) against a small
//              key list. Returning the same object makes Mithril's parent diff
//              short-circuit on `old === new`, so the whole subtree is skipped.
//   * pure() — a *leaf* component skips its own view() and subtree while its
//              attrs are unchanged, so a parent that rebuilt a fresh vnode for
//              it still costs no render.
//
// Components must not hand-roll either: no ad-hoc vnode caches on state and no
// raw onbeforeupdate. Both mechanisms depend on the store's mutation contract
// (records are replaced wholesale; windows bump `rev` on every content change),
// so a wrong equality check is a stale-UI bug. Keeping the checks here makes
// them auditable and testable in one place.

export interface Memo<T> {
	keys: readonly unknown[] | null;
	value: T | undefined;
}

/** memoInit creates an empty slot; call it from oninit. */
export function memoInit<T>(): Memo<T> {
	return { keys: null, value: undefined };
}

/** memo returns the cached value while `keys` are element-wise unchanged,
 * rebuilding and caching it otherwise. Keys should be cheap and exact: a store
 * revision counter, or a reference that changes only when a rebuild is
 * required. */
export function memo<T>(
	slot: Memo<T>,
	keys: readonly unknown[],
	build: () => T,
): T {
	if (slot.keys !== null && sameKeys(slot.keys, keys)) {
		return slot.value as T;
	}
	const value = build();
	slot.keys = keys.slice();
	slot.value = value;
	return value;
}

/** memoVNode is memo() typed for the common case of a single vnode. */
export function memoVNode(
	slot: Memo<Mithril.Vnode>,
	keys: readonly unknown[],
	build: () => Mithril.Vnode,
): Mithril.Vnode {
	return memo(slot, keys, build);
}

/** AttrsEq reports whether a component renders identically for two attrs
 * objects. It must compare every field the view reads. Reference equality is
 * enough for store records, which the mutation contract replaces wholesale;
 * compare fields only when the record is mutated under a revision instead. */
export type AttrsEq<A> = (next: A, prev: A) => boolean;

/** pure wraps a presentational component so Mithril skips its view() (and the
 * subtree it returns) while `eq` says the attrs are unchanged. The result has
 * the same object shape, so keying and lifecycle behavior are unchanged and no
 * extra vnode is introduced. The wrapped component must not define its own
 * onbeforeupdate: pure is the single skip hook, not a layer over another. */
export function pure<A, S>(
	component: Mithril.Component<A, S>,
	eq: AttrsEq<A>,
): Mithril.Component<A, S> {
	if (component.onbeforeupdate !== undefined) {
		throw new Error("pure: component already defines onbeforeupdate");
	}
	const wrapped: Mithril.Component<A, S> = {
		...component,
		onbeforeupdate: (vnode, old) => !eq(vnode.attrs, old.attrs),
	};
	return wrapped;
}

function sameKeys(a: readonly unknown[], b: readonly unknown[]): boolean {
	if (a.length !== b.length) {
		return false;
	}
	for (let i = 0; i < a.length; i++) {
		if (a[i] !== b[i]) {
			return false;
		}
	}
	return true;
}

// ==========================================================================
// scheduler.ts
// ==========================================================================
// Single redraw entry point. Every state mutation calls request(). Mithril's
// own redraw() already coalesces to at most one render per animation frame (it
// sets a pending flag and schedules a rAF), so request() delegates to it rather
// than adding a second rAF hop of our own. m.redraw.sync() is never used.

/** request schedules a redraw for the current or next animation frame. */
export function request(): void {
	m.redraw();
}

/** deferFrame runs `cb` after the current frame has had a chance to paint and
 * returns its timer id so the caller can cancel it. Call it from a render (a
 * view or onupdate, which run inside Mithril's requestAnimationFrame) to move
 * one expensive pass -- building and mounting a long message list -- out of the
 * frame that acknowledges a user action. The rendering step finishes before the
 * timer task runs, so a request() in `cb` redraws one frame later. */
export function deferFrame(cb: () => void): number {
	return window.setTimeout(cb, 0);
}
