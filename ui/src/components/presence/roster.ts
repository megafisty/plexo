// roster.ts — the channel/room member list shown in the right column.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useStore } from "../../context.js";
import { rosterRank, sortRosterNames } from "../../lib/order.js";
import { request } from "../../render.js";
import type { Character, Conversation } from "../../store/state.js";
import type { MemberInfo } from "../../transport/protocol.js";
import { RosterCharacter, RowCache, moderatorFor } from "./character.js";
import { DEFAULT_STRIDE, rosterWindow, syncRosterMeasurement } from "./rosterWindow.js";


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

export interface ChannelRosterAttrs {
	session: string;
	conv: Conversation;
}

interface ChannelRosterState {
	members?: string[];
	ops?: string[];
	friends?: MemberInfo[];
	sorted: string[];
	opsSet: Set<string>;
	/** cache memoizes rendered row vnodes per name and hands out stable
	 * placeholder records, so RosterCharacter's render.pure reference check
	 * holds. See RowCache. */
	cache: RowCache<Mithril.Vnode>;
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
		state.cache = new RowCache();
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
			state.cache.clear();
			return m("aside.roster-panel", [
				m("h2.sidebar-title", `Members (${members.length})`),
				m("p.roster-empty.muted", "No members."),
			]);
		}

		const { start, end, virtual } = rosterWindow(
			total,
			state.scrollTop,
			state.stride,
			state.viewportH,
		);

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

/** rowsChanged reports whether any rendered row's presence record or moderator
 * mark changed since the cached list was built. The scan covers only the
 * current window and is pointer compares only; presence records and the
 * placeholder records are stable references. */
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
		const record = characters[name];
		const character = state.cache.presenceOf(name, record);
		if (
			state.cache.isStale(name, character, moderatorFor(record, state.opsSet, name))
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
	const keep = new Set<string>();
	const rows: Mithril.Vnode[] = [];
	for (let i = start; i < end; i++) {
		const name = state.sorted[i];
		if (name === undefined) {
			continue;
		}
		keep.add(name);
		const record = characters[name];
		const character = state.cache.presenceOf(name, record);
		const moderator = moderatorFor(record, state.opsSet, name);
		rows.push(
			state.cache.value(name, character, moderator, () =>
				m(
					"li",
					{ key: name },
					m(RosterCharacter, { character, moderator, row: true }),
				),
			),
		);
	}
	// Drop values and placeholder records for names no longer in the window: the
	// maps are keyed by name and would otherwise grow with every churn of unknown
	// members. `keep` covers exactly the rendered window.
	state.cache.prune(keep);

	const attrs: Mithril.Attributes = {};
	if (virtual) {
		attrs.onscroll = (e: Event) => {
			// Mithril redraws after the handler, so the view picks up the new
			// offset without an explicit request().
			state.scrollTop = (e.target as HTMLElement).scrollTop;
		};
		attrs.oncreate = (vnode: Mithril.VnodeDOM) => {
			if (syncRosterMeasurement(vnode.dom as HTMLElement, state, true)) {
				request();
			}
		};
		attrs.onupdate = (vnode: Mithril.VnodeDOM) => {
			if (syncRosterMeasurement(vnode.dom as HTMLElement, state, false)) {
				request();
			}
		};
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
