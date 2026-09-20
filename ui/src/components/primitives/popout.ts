// popout.ts — the top-bar popout shell shared by the friends list and the
// warpmarks list: a toolbar button, a full-viewport click-catching overlay, and
// the popover slot mounted only while open. Presentational: it takes `open` and
// the intents as attrs and never reads the View.
//
// The caller passes the popover as children in a single-element keyed fragment
// (`open ? [m(Popover, { key })] : null`). Mithril requires a fragment's
// children to be uniformly keyed, so the keyed popover must be its own fragment
// beside the unkeyed button and overlay; keying it remounts the popover on a
// session switch, which is what refetches the active character's content.

import m from "../../mithril.js";
import type * as Mithril from "mithril";

export interface PopoutMenuAttrs {
	/** label is the toolbar button text. */
	label: string;
	title: string;
	/** buttonClass selects the button family (the CSS groups them). */
	buttonClass: string;
	/** open raises the button's open state and mounts the overlay and slot. */
	open: boolean;
	onToggle: () => void;
	onClose: () => void;
}

export const PopoutMenu: Mithril.Component<PopoutMenuAttrs> = {
	view: ({ attrs, children }) =>
		m("div.popout-menu", [
			m(
				"button",
				{
					type: "button",
					class: `${attrs.buttonClass}${attrs.open ? " is-open" : ""}`,
					title: attrs.title,
					onclick: attrs.onToggle,
				},
				attrs.label,
			),
			attrs.open ? m("div.popout-overlay", { onclick: attrs.onClose }) : null,
			children,
		]),
};
