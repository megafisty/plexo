// roster.ts — the channel/room member list shown in the right column.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useStore } from "../../context.js";
import { rosterRank, sortRosterNames } from "../../lib/order.js";
import { request } from "../../render.js";
import type { Character, Conversation } from "../../store/state.js";
import type { MemberInfo } from "../../transport/protocol.js";
import { RosterCharacter, type RosterCharacterState } from "./character.js";


// ==========================================================================
// ChannelRoster.ts
// ==========================================================================
// ChannelRoster: the right column while an official channel or private room is
// active. Lists the conversation's members with RosterCharacter, ordered by
// global-admin status, then room-op status, then friends/bookmarks, then name.
//
// The sort is cached against the input references (members / ops / friends
// arrays are replaced wholesale on update), so a redraw driven by presence or
// typing does not re-sort the roster. Global-admin status is assumed stable for
// the session (the core reports it from the login-time ADL), so it does not
// invalidate the cached order.
//
// Performance: a channel can hold 500+ members and a channel switch replaces
// the whole membership set, so the switch cost is O(members) node creation.
// Above VIRTUAL_MIN members only the rows intersecting the viewport (plus
// overscan) are rendered, with top/bottom padding standing in for the rest; the
// `ul` is the scroll container and the "Members (N)" header stays put. That
// makes a switch O(visible). Below the threshold every row renders and the
// original memoization applies:
//
//   - Each row's vnode is cached by name and reused while its presence record
//     is referentially unchanged (applyPresence replaces records wholesale, so
//     reference equality is a valid "renders identically" check).
//   - The whole <ul> vnode is cached and returned verbatim while its window and
//     rows are unchanged: Mithril's parent diff short-circuits on `old === new`,
//     so unrelated redraws (messages, typing, presence elsewhere) touch zero
//     roster vnodes.
//
// Rows are `li > RosterCharacter(row)` — one vnode fewer than the previous
// `li > button > RosterCharacter`.

const NO_MEMBERS: string[] = [];
const NO_OPS: string[] = [];

/** VIRTUAL_MIN is the member count above which only the visible window is
 * rendered. Below it the full list is cheap enough (and keeps find-in-page
 * working). */
const VIRTUAL_MIN = 120;

/** OVERSCAN is the extra rows rendered above and below the viewport, covering
 * the gap between a scroll event and the next redraw. */
const OVERSCAN = 8;

/** DEFAULT_STRIDE seeds the window before the first row has been measured. */
const DEFAULT_STRIDE = 28;

export interface ChannelRosterAttrs {
	session: string;
	conv: Conversation;
}

type Moderator = "room" | "global" | undefined;

/** CachedRow is one rendered row plus the inputs it was built from. */
interface CachedRow {
	character: RosterCharacterState;
	moderator: Moderator;
	vnode: Mithril.Vnode;
}

interface ChannelRosterState {
	members?: string[];
	ops?: string[];
	friends?: MemberInfo[];
	sorted: string[];
	opsSet: Set<string>;
	/** placeholders caches the fallback record per unknown name so
	 * RosterCharacter's render.pure reference check still holds. */
	placeholders: Map<string, { name: string; online: boolean }>;
	/** rows caches the rendered row per name currently in the window. */
	rows: Map<string, CachedRow>;
	/** list is the cached <ul> vnode, reused verbatim while its window and rows
	 * are unchanged. */
	list: Mithril.Vnode | null;
	/** listSession is the session the cached list's delegated handler belongs
	 * to; a tab switch forces a rebuild. */
	listSession?: string;
	/** lastConv identifies the conversation the caches and scroll belong to. */
	lastConv?: string;
	/** scrollTop is the last known scroll offset of the list. */
	scrollTop: number;
	/** viewportH is the list's measured client height; 0 until measured. */
	viewportH: number;
	/** stride is the measured row height plus gap; 0 until measured. */
	stride: number;
	/** winStart / winEnd are the half-open window the cached list was built for.
	 * -1 forces a build. */
	winStart: number;
	winEnd: number;
	/** resetScroll is set on a conversation switch and cleared once applied. */
	resetScroll: boolean;
	/** winW / winH are the last observed window dimensions, used to re-measure
	 * only on resize instead of on every redraw. */
	winW: number;
	winH: number;
}

export const ChannelRoster: Mithril.Component<ChannelRosterAttrs> = {
	oninit: (vnode) => {
		const state = vnode.state as ChannelRosterState;
		state.sorted = [];
		state.opsSet = new Set();
		state.placeholders = new Map();
		state.rows = new Map();
		state.list = null;
		state.scrollTop = 0;
		state.viewportH = 0;
		state.stride = 0;
		state.winStart = -1;
		state.winEnd = -1;
		state.resetScroll = false;
		state.winW = 0;
		state.winH = 0;
	},
	view: (vnode) => {
		const store = useStore();
		const { session, conv } = vnode.attrs;
		const state = vnode.state as ChannelRosterState;

		const members = conv.members ?? NO_MEMBERS;
		const ops = conv.ops ?? NO_OPS;
		const friends = store.friends;

		// Switching conversation drops the cached list and scrolls to the top.
		const convId = `${session}\u0000${conv.key}`;
		if (state.lastConv !== convId) {
			state.lastConv = convId;
			state.list = null;
			state.scrollTop = 0;
			state.resetScroll = true;
			state.winStart = -1;
			state.winEnd = -1;
		}

		// Re-sort only when membership, ops, or the friends list changed.
		if (
			state.members !== members ||
			state.ops !== ops ||
			state.friends !== friends
		) {
			state.members = members;
			state.ops = ops;
			state.friends = friends;
			state.opsSet = new Set(ops);
			const friendSet = new Set(friends.map((f) => f.name));
			state.sorted = sortRosterNames(members, (name) =>
				rosterRank({
					isAdmin: store.characters[name]?.admin === true,
					isOp: state.opsSet.has(name),
					isFriend: friendSet.has(name),
				}),
			);
			// Order or membership changed: the cached list no longer applies.
			state.list = null;
			state.winStart = -1;
			state.winEnd = -1;
		}

		const total = state.sorted.length;
		const characters = store.characters;

		if (total === 0) {
			state.list = null;
			state.winStart = -1;
			state.winEnd = -1;
			state.rows.clear();
			state.placeholders.clear();
			return m("aside.roster-panel", [
				m("h2.sidebar-title", `Members (${members.length})`),
				m("p.roster-empty.muted", "No members."),
			]);
		}

		const virtual = total > VIRTUAL_MIN;
		let start = 0;
		let end = total;
		if (virtual) {
			const stride = state.stride > 0 ? state.stride : DEFAULT_STRIDE;
			const first = Math.floor(state.scrollTop / stride);
			const screen = state.viewportH > 0 ? state.viewportH : stride * 12;
			const rowsPerScreen = Math.ceil(screen / stride) + 1;
			start = Math.max(0, first - OVERSCAN);
			end = Math.min(total, first + rowsPerScreen + OVERSCAN);
			if (end <= start) {
				end = Math.min(total, start + 1);
			}
		}

		if (
			state.list === null ||
			state.listSession !== session ||
			state.winStart !== start ||
			state.winEnd !== end ||
			rowsChanged(state, characters, start, end)
		) {
			state.list = buildList(
				state,
				characters,
				start,
				end,
				total,
				virtual,
			);
			state.listSession = session;
			state.winStart = start;
			state.winEnd = end;
		}

		return m("aside.roster-panel", [
			m("h2.sidebar-title", `Members (${members.length})`),
			state.list,
		]);
	},
};

/** moderatorFor resolves the row's moderator mark from global-admin status and
 * the room op set. */
function moderatorFor(
	character: Character | undefined,
	ops: Set<string>,
	name: string,
): Moderator {
	if (character?.admin === true) {
		return "global";
	}
	if (ops.has(name)) {
		return "room";
	}
	return undefined;
}

/** rowsChanged reports whether any rendered row's presence record or moderator
 * mark changed since the cached list was built. The scan covers only the
 * current window and is pointer compares only; presence records and the
 * placeholder map are stable references. */
function rowsChanged(
	state: ChannelRosterState,
	characters: Record<string, Character>,
	start: number,
	end: number,
): boolean {
	for (let i = start; i < end; i++) {
		const name = state.sorted[i];
		if (name === undefined) {
			continue;
		}
		const cached = state.rows.get(name);
		if (cached === undefined) {
			return true;
		}
		const record = characters[name];
		if (
			cached.character !== presence(state, record, name) ||
			cached.moderator !== moderatorFor(record, state.opsSet, name)
		) {
			return true;
		}
	}
	return false;
}

/** buildList rebuilds the <ul> for the half-open window [start, end), reusing
 * the cached vnode of every row whose record and moderator mark are unchanged.
 * When windowed, top/bottom padding preserves the full list height and the
 * scroll/measure hooks keep the window in sync. */
function buildList(
	state: ChannelRosterState,
	characters: Record<string, Character>,
	start: number,
	end: number,
	total: number,
	virtual: boolean,
): Mithril.Vnode {
	const next = new Map<string, CachedRow>();
	const rows: Mithril.Vnode[] = [];
	for (let i = start; i < end; i++) {
		const name = state.sorted[i];
		if (name === undefined) {
			continue;
		}
		const record = characters[name];
		const character = presence(state, record, name);
		const moderator = moderatorFor(record, state.opsSet, name);
		const cached = state.rows.get(name);
		if (
			cached !== undefined &&
			cached.character === character &&
			cached.moderator === moderator
		) {
			next.set(name, cached);
			rows.push(cached.vnode);
			continue;
		}
		const vnode = m(
			"li",
			{ key: name },
			m(RosterCharacter, { character, moderator, row: true }),
		);
		next.set(name, { character, moderator, vnode });
		rows.push(vnode);
	}
	state.rows = next;
	// Drop placeholder records for names no longer in the window: the map is
	// keyed by name and would otherwise grow with every churn of unknown members.
	// `next` covers exactly the rendered window, so anything outside it is stale.
	for (const name of state.placeholders.keys()) {
		if (!next.has(name)) {
			state.placeholders.delete(name);
		}
	}

	const attrs: Mithril.Attributes = {};
	if (virtual) {
		attrs.onscroll = (e: Event) => {
			// Mithril redraws after the handler, so the view picks up the new
			// offset without an explicit request().
			state.scrollTop = (e.target as HTMLElement).scrollTop;
		};
		attrs.oncreate = (vnode: Mithril.VnodeDOM) =>
			syncMeasurement(vnode, state, true);
		attrs.onupdate = (vnode: Mithril.VnodeDOM) =>
			syncMeasurement(vnode, state, false);
	}
	// Top/bottom spacers stand in for the unrendered rows so the scrollbar
	// reflects the full membership. They are keyed <li>s (a fragment must be
	// all-keyed or all-unkeyed); padding on the scroller itself would make its
	// minimum height huge and stop it shrinking to the panel.
	const stride = state.stride > 0 ? state.stride : DEFAULT_STRIDE;
	const children: Mithril.Vnode[] = virtual
		? [
				m("li.roster-spacer", {
					key: "#top",
					style: { height: `${start * stride}px` },
				}),
				...rows,
				m("li.roster-spacer", {
					key: "#bottom",
					style: { height: `${(total - end) * stride}px` },
				}),
			]
		: rows;
	return m("ul.roster-list", attrs, children);
}

/** syncMeasurement applies a pending scroll reset and re-measures only when the
 * window size changed (or before the first successful measurement). Doing it on
 * every redraw would force a layout read (clientHeight, row offsets) right after
 * Mithril's DOM writes; gating it keeps unrelated redraws layout-free. */
function syncMeasurement(
	vnode: Mithril.VnodeDOM,
	state: ChannelRosterState,
	force: boolean,
): void {
	const el = vnode.dom as HTMLElement;
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
		measure(vnode, state);
	}
}

/** measure records the scroll offset, viewport height, and row stride after the
 * list is created or updated, then requests a redraw when those inputs change
 * the window. */
function measure(vnode: Mithril.VnodeDOM, state: ChannelRosterState): void {
	const el = vnode.dom as HTMLElement;
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
		// The gap between two rows is the most portable stride measurement; fall
		// back to row height + computed gap when only one row is rendered.
		let stride = first.offsetHeight + rowGap(el);
		const second = rows[1];
		if (second !== undefined) {
			const delta = second.offsetTop - first.offsetTop;
			if (delta > 0) {
				stride = delta;
			}
		}
		if (stride > 0 && Math.abs(stride - state.stride) > 0.5) {
			state.stride = stride;
			changed = true;
		}
	}
	if (changed) {
		request();
	}
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

/** presence returns the store's presence record, or a stable per-name
 * placeholder so unknown members do not defeat RosterCharacter's reference
 * check on every redraw. */
function presence(
	state: ChannelRosterState,
	known: RosterCharacterState | undefined,
	name: string,
): RosterCharacterState {
	if (known !== undefined) {
		return known;
	}
	let placeholder = state.placeholders.get(name);
	if (placeholder === undefined) {
		placeholder = { name, online: false };
		state.placeholders.set(name, placeholder);
	}
	return placeholder;
}
