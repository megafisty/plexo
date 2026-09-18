// timeline.ts — the bounded message timeline: MessageList and its immutable
// MessageRow leaf. Absorbs MessageList.ts and MessageRow.ts.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import type { Entry, EntryWindow, Store } from "../../store/state.js";
import { formatClock } from "../../lib/format.js";
import { pure } from "../../render.js";
import { CharacterLink } from "../presence/character.js";
import { useStore, useView } from "../../context.js";
import { isEditable } from "../../lib/dom.js";
import { deferFrame, request } from "../../render.js";
import { loadNewer, loadOlder } from "../../store/commands.js";
import { convScopeKey, dialogOpen, type View } from "../../store/state.js";


// ==========================================================================
// MessageRow.ts
// ==========================================================================
// MessageRow: one timeline entry. Content arrives as sanitized HTML from the
// core; the speaker's gender color resolves once presence is known (F-Chat
// never changes a character's gender while it is online, so this cannot drift).
// The entry body is static, but the record changes identity when the send state
// changes (pending -> sent/failed), which is why the pure check compares both
// the entry reference and the gender. See render.ts.

export interface MessageRowAttrs {
	entry: Entry;
	/** speaker's gender, from presence; drives the name color. */
	gender?: string;
}

const RawMessageRow: Mithril.Component<MessageRowAttrs> = {
	view: ({ attrs }) => {
		const e = attrs.entry;
		return m("div.msg", { key: e.id, class: rowClass(e), "data-entry": e.id }, [
			m("div.msg-body", [
				m(CharacterLink, {
					name: e.speaker,
					gender: attrs.gender,
					class: "msg-speaker",
				}),
				m.trust(e.html),
			]),
			m("div.msg-meta", [
				m("span.msg-time", { "data-entry": e.id }, formatClock(e.time)),
				stateMark(e),
			]),
		]);
	},
};

/** MessageRow: one timeline entry. The body is immutable HTML and the Entry
 * record is replaced wholesale on any change (send state included), so entry
 * reference equality is a complete check; gender is the only field read from
 * outside the entry. */
export const MessageRow: Mithril.Component<MessageRowAttrs> = pure(
	RawMessageRow,
	(next, prev) => next.entry === prev.entry && next.gender === prev.gender,
);

function rowClass(e: Entry): string {
	let cls = `kind-${e.kind}`;
	if (e.self === true) {
		cls += " is-self";
	}
	if (e.send !== undefined) {
		cls += ` is-${e.send}`;
	}
	return cls;
}

function stateMark(e: Entry): Mithril.Vnode | null {
	if (e.send === "pending") {
		return m("span.msg-state", { title: "Sending…" }, "…");
	}
	if (e.send === "failed") {
		return m(
			"span.msg-state.msg-state-failed",
			{ title: e.error ?? "Send failed" },
			"!",
		);
	}
	return null;
}

// ==========================================================================
// MessageList.ts
// ==========================================================================
// MessageList: the bounded timeline of the active conversation. Sole scroll
// owner: it tracks whether the view is pinned to the bottom (and records that
// in View.msgPinned for apply.ts) and silently pages newer entries back in when
// the view returns to the bottom. Unread is owned by the client (store/unread).
//
// Older paging anchors on the first visible row's data-entry id, so prepending
// (and the newest-edge trim that follows) never moves the user's place.
//
// Performance: the rendered row vnodes are memoized against the window's
// mutation counter, so a redraw driven by typing/presence does not rebuild up
// to WINDOW rows.

const PIN_MARGIN = 60;

// DEFER_ROW_THRESHOLD is the retained-window size above which a switch's rows
// are withheld for one frame. Below it the mount is cheap enough that a
// placeholder reads as flicker; above it, splitting the frame that
// acknowledges the switch from the one that mounts the rows keeps a large
// channel from painting the sidebar highlight, the header, and every row in a
// single long frame. See render.deferFrame.
const DEFER_ROW_THRESHOLD = 40;

/** Mithril lets a handler suppress its automatic redraw with `redraw = false`. */
type MithrilEvent = Event & { redraw?: boolean };

export const MessageList: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as ListState;
		state.pinned = true;
		state.loadingOlder = false;
		state.loadingNewer = false;
		state.lastKey = undefined;
		state.ackShown = false;
		state.deferred = false;
		state.deferTimer = undefined;
		state.rows = [];
		state.onscroll = (e: Event) => {
			const ev = e as MithrilEvent;
			if (state.loadingOlder) {
				ev.redraw = false;
				return;
			}
			const el = e.target as HTMLElement;
			const pinned =
				el.scrollTop + el.clientHeight >= el.scrollHeight - PIN_MARGIN;
			if (pinned === state.pinned) {
				// Nothing to repaint: suppress Mithril's automatic redraw.
				ev.redraw = false;
				return;
			}
			state.pinned = pinned;
			const { refView, refSession, refKey } = state;
			if (
				refView !== undefined &&
				refSession !== undefined &&
				refKey !== undefined
			) {
				refView.msgPinned[convScopeKey(refSession, refKey)] = pinned;
			}
			// Mithril redraws after the handler.
		};
	},
	oncreate: (vnode) => {
		const state = vnode.state as ListState;
		const el = vnode.dom as HTMLElement;
		state.el = el;
		state.onKey = (e: KeyboardEvent) => handlePageKey(state, e);
		document.addEventListener("keydown", state.onKey);
		if (!state.pinned || state.deferred) {
			// A deferred mount has no rows yet; leave scrolledRev unset so the
			// post-deferral onupdate scrolls to the live edge once they land.
			return;
		}
		// First paint: jump to the live edge without waiting for a redraw.
		el.scrollTop = el.scrollHeight;
		state.scrolledRev = currentRev(state);
	},
	onremove: (vnode) => {
		const state = vnode.state as ListState;
		if (state.onKey !== undefined) {
			document.removeEventListener("keydown", state.onKey);
		}
		if (state.deferTimer !== undefined) {
			window.clearTimeout(state.deferTimer);
		}
	},
	onupdate: (vnode) => {
		const state = vnode.state as ListState;
		const el = vnode.dom as HTMLElement;
		state.el = el;

		const rev = currentRev(state);
		if (rev === undefined) {
			return;
		}

		if (state.anchorId !== undefined) {
			restoreAnchor(el, state);
			state.anchorId = undefined;
		} else if (state.pinned && !state.deferred && rev !== state.scrolledRev) {
			// Keep the live edge in view, but only when the window changed: an
			// unrelated redraw must not force a layout read of scrollHeight. A
			// deferred frame has no rows yet, so scrolling there is meaningless;
			// recording scrolledRev would then suppress the scroll once the rows
			// land (mirrors the oncreate guard).
			el.scrollTop = el.scrollHeight;
			state.scrolledRev = rev;
		}

		const { refStore, refSession, refKey } = state;
		if (
			refStore === undefined ||
			refSession === undefined ||
			refKey === undefined
		) {
			return;
		}
		const win = refStore.entries[refSession]?.[refKey];
		if (win === undefined) {
			return;
		}

		// Silent auto-refill: at the bottom, page in anything that arrived while
		// the view was scrolled up. Repeat per redraw until caught up.
		if (state.pinned && win.hasNewer && !state.loadingNewer) {
			state.loadingNewer = true;
			void loadNewer(refStore, refSession, refKey).then(() => {
				state.loadingNewer = false;
				request();
			});
		}
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const state = vnode.state as ListState;

		const session = view.activeSession;
		const key = session === null ? undefined : view.activeConv[session];
		const win =
			session === null || key === undefined
				? undefined
				: store.entries[session]?.[key];

		state.refStore = store;
		state.refView = view;
		state.refSession = session ?? undefined;
		state.refKey = key;

		// Reset pinning when the conversation changes; a fresh mount takes its
		// own acknowledgement frame, so ackShown restarts too.
		if (key !== state.lastKey) {
			state.lastKey = key;
			state.pinned = true;
			state.scrolledRev = undefined;
			state.ackShown = false;
			state.deferred = false;
			state.rowsWin = undefined;
			state.rows = [];
			if (session !== null && key !== undefined) {
				view.msgPinned[convScopeKey(session, key)] = true;
			}
		}

		// An absent window shows the loading placeholder, which is itself the
		// acknowledgement frame: the row mount that follows is already a later
		// frame, so it is not deferred again.
		if (win === undefined) {
			state.ackShown = true;
			state.deferred = false;
		}

		// Rebuild row vnodes only when the window actually changed. A large
		// window that is already present on the acknowledgement frame is withheld
		// for one frame so the shell (sidebar highlight, header, roster) paints
		// before the rows; deferFrame then triggers the build a frame later.
		if (
			win !== undefined &&
			(state.rowsWin !== win || state.rowsRev !== win.rev)
		) {
			if (!state.ackShown && win.items.length > DEFER_ROW_THRESHOLD) {
				state.ackShown = true;
				state.deferred = true;
				state.deferTimer = deferFrame(() => {
					state.deferTimer = undefined;
					request();
				});
			} else {
				state.ackShown = true;
				state.deferred = false;
				state.rowsWin = win;
				state.rowsRev = win.rev;
				state.rows = win.items.map((e: Entry) =>
					m(MessageRow, {
						key: e.id,
						entry: e,
						gender: store.characters[e.speaker]?.gender,
					}),
				);
			}
		}

		const deferring = state.deferred;

		const olderButton =
			win !== undefined && win.hasOlder
				? m(
						"button.load-older",
						{
							type: "button",
							disabled: state.loadingOlder,
							onclick: () => {
								if (session === null || key === undefined) {
									return;
								}
								startLoadOlder(state, store, session, key);
							},
						},
						state.loadingOlder
							? "Loading…"
							: "Load older messages — click or press PgUp again",
					)
				: null;

		if (session === null || key === undefined || win === undefined || deferring) {
			return m(
				"div.message-list",
				{ onscroll: state.onscroll },
				m(
					"p.muted.msg-placeholder",
					key !== undefined && (win === undefined || deferring)
						? "Loading messages…"
						: "Select a conversation.",
				),
			);
		}

		return m(
			"div.message-list",
			{ onscroll: state.onscroll },
			olderButton,
			win.items.length === 0
				? m("p.muted.msg-placeholder", "No messages yet.")
				: state.rows,
		);
	},
};

interface ListState {
	pinned: boolean;
	loadingOlder: boolean;
	loadingNewer: boolean;
	lastKey: string | undefined;
	onscroll: (e: Event) => void;
	/** onKey is the document PageUp/PageDown listener installed while mounted. */
	onKey?: (e: KeyboardEvent) => void;
	/** el is the scroll container, refreshed after every render. */
	el?: HTMLElement;
	/** anchorId/anchorOffset hold the first visible row across a prepend. */
	anchorId?: string;
	anchorOffset: number;
	/** Refs captured each render so the event/scroll hooks can page. Prefixed
	 * because Mithril uses the component object as vnode.state and reserves
	 * `view`; overwriting it breaks every subsequent redraw. */
	refStore?: Store;
	refView?: View;
	refSession?: string;
	refKey?: string;
	/** rows memo: rebuilt only when rowsWin/rowsRev no longer match. */
	rowsWin?: EntryWindow;
	rowsRev?: number;
	rows: Mithril.Vnode<{ entry: Entry }, {}>[];
	/** ackShown is true once this mount has painted a frame without the heavy
	 * rows: a loading placeholder, or a deliberate one-frame deferral. */
	ackShown: boolean;
	/** deferred is true while a large window's rows are withheld for a frame. */
	deferred: boolean;
	/** deferTimer is the pending one-frame build; cleared on unmount. */
	deferTimer?: number;
	/** scrolledRev is the window rev last scrolled to the bottom, so unrelated
	 * redraws do not re-issue the scroll (and re-read scrollHeight). */
	scrolledRev?: number;
}

/** currentRev returns the active window's mutation counter, or undefined when
 * there is no active conversation or window yet. */
function currentRev(state: ListState): number | undefined {
	const { refStore, refSession, refKey } = state;
	if (
		refStore === undefined ||
		refSession === undefined ||
		refKey === undefined
	) {
		return undefined;
	}
	return refStore.entries[refSession]?.[refKey]?.rev;
}

/** handlePageKey scrolls the timeline a page per PageUp/PageDown when no text
 * entry has focus; the composer owns these keys while it is focused. At the
 * very top a further PageUp pages in older history instead of scrolling. */
function handlePageKey(state: ListState, e: KeyboardEvent): void {
	if (e.key !== "PageUp" && e.key !== "PageDown") {
		return;
	}
	// Modifier chords (and AltGr) belong to the browser or the OS.
	if (e.ctrlKey || e.metaKey || e.altKey || e.shiftKey) {
		return;
	}
	if (isEditable(e.target)) {
		return;
	}
	const el = state.el;
	const view = state.refView;
	const store = state.refStore;
	const session = state.refSession;
	const key = state.refKey;
	if (
		el === undefined ||
		view === undefined ||
		store === undefined ||
		session === undefined ||
		key === undefined ||
		dialogOpen(view)
	) {
		return;
	}
	e.preventDefault();
	if (e.key === "PageDown") {
		el.scrollTop += el.clientHeight;
		return;
	}
	if (el.scrollTop <= 1) {
		if (store.entries[session]?.[key]?.hasOlder === true) {
			startLoadOlder(state, store, session, key);
		}
		return;
	}
	el.scrollTop -= el.clientHeight;
}

/** startLoadOlder pages in older history, holding the visible rows in place.
 * Shared by the button and the PageUp-at-top path. */
function startLoadOlder(
	state: ListState,
	store: Store,
	session: string,
	key: string,
): void {
	if (state.loadingOlder) {
		return;
	}
	state.loadingOlder = true;
	void loadOlder(store, session, key).then(() => {
		state.loadingOlder = false;
		// Capture the anchor before the merge redraws, so the visible rows
		// stay put.
		captureAnchor(state);
		request();
	});
}

/** captureAnchor records the first visible row and its offset from the top of
 * the scroll viewport, so restoreAnchor can hold it in place. */
function captureAnchor(state: ListState): void {
	const el = state.el;
	if (el === undefined) {
		return;
	}
	const anchor = firstVisibleRow(el);
	if (anchor === null) {
		return;
	}
	state.anchorId = anchor.getAttribute("data-entry") ?? undefined;
	state.anchorOffset = anchor.getBoundingClientRect().top - el.getBoundingClientRect().top;
}

/** restoreAnchor scrolls so the anchored row sits at its recorded offset. */
function restoreAnchor(el: HTMLElement, state: ListState): void {
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
function firstVisibleRow(el: HTMLElement): HTMLElement | null {
	const top = el.getBoundingClientRect().top;
	for (const row of el.querySelectorAll<HTMLElement>(".msg[data-entry]")) {
		if (row.getBoundingClientRect().bottom > top) {
			return row;
		}
	}
	return null;
}
