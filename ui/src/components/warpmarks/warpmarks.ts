// warpmarks.ts — the warpmark feature's components: WarpmarksMenu, the top-bar
// Warp button and its scrollable popout list with a naive text filter, and
// WarpmarkDialog, the label prompt opened from a message timestamp. A warpmark
// is a private annotation on one stored entry; opening it activates a read-only
// virtual conversation. See docs/warpmarks.md.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useDispatch, useStore, useView } from "../../context.js";
import { convLabel, formatClock } from "../../lib/format.js";
import {
	loadWarpmarks,
	openWarpmark,
	removeWarpmark,
	saveWarpmark,
} from "../../store/commands.js";
import type { Warpmark } from "../../transport/protocol.js";
import { closeModal, closePopout, togglePopout } from "../../store/state.js";
import { Dialog } from "../primitives/dialog.js";
import { Button } from "../primitives/form.js";
import { PopoutMenu } from "../primitives/popout.js";

// ==========================================================================
// WarpmarksMenu.ts
// ==========================================================================
// WarpmarksMenu: the top-bar Warp button and the slot for its popout. Like the
// friends popout it is a self-contained button + overlay + popover pair and is
// bound to the active session. Opening a mark activates its read-only warp pane
// and dismisses the popout.

export const WarpmarksMenu: Mithril.Component = {
	view: () => {
		const store = useStore();
		const view = useView();

		const session = view.activeSession;
		if (session === null || store.sessions[session] === undefined) {
			return null;
		}

		const open = view.popout === "warpmarks";
		return m(
			PopoutMenu,
			{
				label: "Warp",
				title: "Open your warpmarks",
				buttonClass: "search-button",
				open,
				onToggle: () => togglePopout(view, "warpmarks"),
				onClose: () => closePopout(view),
			},
			// `key: session` remounts (and refetches) the active character's marks.
			open ? [m(WarpmarksPopout, { key: session, session })] : null,
		);
	},
};

// WarpmarksPopout is the list itself: the active character's marks, newest
// first, filtered client-side by a substring match over label, speaker, and
// conversation. Opening a row activates its read-only warp pane; deleting drops
// the mark and any open pane for it.
interface WarpmarksPopoutAttrs {
	session: string;
}

interface WarpmarksPopoutState {
	/** query is the raw search box value; the match is case-insensitive. */
	query: string;
}

const WarpmarksPopout: Mithril.Component<
	WarpmarksPopoutAttrs,
	WarpmarksPopoutState
> = {
	oninit: (vnode) => {
		const state = vnode.state as WarpmarksPopoutState;
		state.query = "";
		// The list is HTTP-only and has no live event, so refetch on mount (and
		// on each tab switch, which remounts the keyed component).
		void loadWarpmarks(useStore(), vnode.attrs.session);
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const state = vnode.state as WarpmarksPopoutState;
		const { session } = vnode.attrs;

		const marks = store.warpmarks[session] ?? [];
		const query = state.query.trim().toLowerCase();
		const filtered =
			query === ""
				? marks
				: marks.filter((mark) => matchesWarpmark(mark, query));
		const close = (): void => {
			closePopout(view);
		};

		return m("div.warpmarks-popover", [
			m("div.warpmarks-popover-head", [
				m("h3.warpmarks-popover-title", "Warps"),
				m("span.warpmarks-count.muted", `${marks.length}`),
			]),
			m("input.warpmarks-search", {
				type: "search",
				placeholder: "Filter warps…",
				value: state.query,
				"aria-label": "Filter warpmarks",
				oninput: (e: Event) => {
					state.query = (e.target as HTMLInputElement).value;
				},
			}),
			m(
				"p.muted.warpmarks-hint",
				"Click a message's timestamp to mark it. A warp opens that message in its conversation, read-only.",
			),
			filtered.length === 0
				? m(
						"p.muted.warpmarks-empty",
						marks.length === 0 ? "No warpmarks yet." : "No matches.",
					)
				: m(
						"ul.warpmark-list",
						filtered.map((mark) =>
							warpRow(store, view, dispatch, session, mark, close),
						),
					),
		]);
	},
};

/** matchesWarpmark is the naive filter: a case-folded substring test against a
 * mark's label, speaker, and conversation title/id. */
function matchesWarpmark(mark: Warpmark, query: string): boolean {
	return (
		mark.label.toLowerCase().includes(query) ||
		mark.speaker.toLowerCase().includes(query) ||
		convLabel(mark.convName, mark.conv.id).toLowerCase().includes(query)
	);
}

/** warpRow renders one mark. A dangling mark keeps its snapshot context but
 * cannot be opened. `dismiss` closes the popout when a mark is opened. */
function warpRow(
	store: ReturnType<typeof useStore>,
	view: ReturnType<typeof useView>,
	dispatch: ReturnType<typeof useDispatch>,
	session: string,
	mark: Warpmark,
	dismiss: () => void,
): Mithril.Vnode {
	const missing = mark.missing === true;
	const label =
		mark.label !== ""
			? mark.label
			: `${mark.speaker} · ${convLabel(mark.convName, mark.conv.id)}`;
	return m("li.warpmark-row", { key: mark.entryId }, [
		m(
			"button.warpmark-open",
			{
				type: "button",
				disabled: missing,
				title: missing ? "This message is no longer stored" : "Open this message",
				onclick: () => {
					openWarpmark(store, view, dispatch, session, mark);
					dismiss();
				},
			},
			[
				m("span.warpmark-label", label),
				m(
					"span.warpmark-meta.muted",
					`${convLabel(mark.convName, mark.conv.id)} · ${mark.speaker} · ${formatClock(Date.parse(mark.createdAt) || 0)}`,
				),
				missing
					? m("span.warpmark-missing.muted", "Message no longer stored")
					: m("div.warpmark-snippet", m.trust(mark.html ?? "")),
			],
		),
		m(Button, {
			label: "Delete",
			variant: "secondary",
			small: true,
			class: "warpmark-delete",
			title: "Delete this warpmark",
			onclick: () => {
				void removeWarpmark(store, view, session, mark.entryId);
			},
		}),
	]);
}

// ==========================================================================
// WarpmarkDialog.ts
// ==========================================================================
// WarpmarkDialog: the label prompt for the timestamp a user clicked. The input
// is controlled: every keystroke is mirrored into the modal payload's `label`,
// so a redraw re-renders the same value. (An uncontrolled input with a fixed
// `value` would be reset by Mithril on the redraw each keystroke triggers.) An
// existing mark offers delete.

export const WarpmarkDialog: Mithril.Component = {
	view: () => {
		const store = useStore();
		const view = useView();
		const dialog = view.modal?.kind === "warpmark" ? view.modal : null;
		if (dialog === null) {
			return null;
		}
		const close = (): void => {
			closeModal(view);
		};

		return m(
			Dialog,
			{
				title: dialog.existing ? "Edit warpmark" : "Add warpmark",
				subtitle: dialog.speaker,
				onClose: close,
			},
			[
				m("input.warpmark-input", {
					type: "text",
					maxlength: "128",
					placeholder: "Label (optional)",
					value: dialog.label,
					"aria-label": "Warpmark label",
					oncreate: (inputVnode: Mithril.VnodeDOM) => {
						(inputVnode.dom as HTMLInputElement).focus();
					},
					oninput: (e: Event) => {
						dialog.label = (e.target as HTMLInputElement).value;
					},
					onkeydown: (e: KeyboardEvent) => {
						if (e.key === "Enter") {
							e.preventDefault();
							void saveWarpmark(store, view, dialog.label);
						}
					},
				}),
				m("div.warpmark-dialog-actions", [
					dialog.existing
						? m(Button, {
								label: "Delete",
								variant: "secondary",
								onclick: () => {
									void removeWarpmark(store, view, dialog.session, dialog.entryId);
								},
							})
						: null,
					m(Button, {
						label: "Cancel",
						variant: "secondary",
						onclick: close,
					}),
					m(Button, {
						label: "Save",
						onclick: () => {
							void saveWarpmark(store, view, dialog.label);
						},
					}),
				]),
			],
		);
	},
};