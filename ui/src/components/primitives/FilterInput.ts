// FilterInput.ts — a search field that reports every keystroke live and a
// debounced value once typing pauses. It owns the quiet period and the
// redraw-suppression dance, so the shells that filter a client-side list
// (ads, the join catalog) do not each re-implement them.
//
// Presentational like the other primitives: no store, view, or dispatch. It
// takes its value and intents as attrs; the debounce is an internal timer,
// cancelled on unmount. The caller keeps the live value (for the controlled
// input) and the debounced value (for filtering).

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { debounce, type Debounced } from "../../lib/debounce.js";
import { request } from "../../render.js";

/** FILTER_DEBOUNCE_MS is the quiet period before `onfilter` fires. It coalesces
 * keystrokes into one filter pass without a visible lag while typing. */
export const FILTER_DEBOUNCE_MS = 120;

export interface FilterInputAttrs {
	/** value is the caller-owned text, updated synchronously from `oninput`. */
	value: string;
	placeholder?: string;
	/** class is the field's own class (styles live with the feature). */
	class?: string;
	/** oninput reports every keystroke; must update the caller's value. */
	oninput: (value: string) => void;
	/** onfilter reports the value once typing pauses. The component requests the
	 * redraw that renders the filtered list. */
	onfilter: (value: string) => void;
}

interface FilterInputState {
	debounce: Debounced;
}

export const FilterInput: Mithril.Component<
	FilterInputAttrs,
	FilterInputState
> = {
	oninit: (vnode) => {
		vnode.state.debounce = debounce(FILTER_DEBOUNCE_MS);
	},
	onremove: (vnode) => {
		vnode.state.debounce.cancel();
	},
	view: ({ attrs, state }) =>
		m("input", {
			type: "search",
			class: attrs.class,
			placeholder: attrs.placeholder,
			value: attrs.value,
			oninput: (e: Event) => {
				const value = (e.target as HTMLInputElement).value;
				attrs.oninput(value);
				state.debounce.schedule(() => {
					attrs.onfilter(value);
					request();
				});
				// The debounced request() repaints the list; skipping Mithril's
				// automatic redraw keeps keystrokes cheap on slow clients.
				(e as Event & { redraw?: boolean }).redraw = false;
			},
		}),
};
