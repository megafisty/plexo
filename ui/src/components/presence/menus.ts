// menus.ts — the character context menu and the top-bar friends popout.
// Absorbs CharacterMenu.ts and FriendsMenu.ts.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useDispatch, useStore, useView } from "../../context.js";
import { genderClass, profileURL } from "../../lib/characters.js";
import { useEscape } from "../primitives/dialog.js";
import { activateConv, closeCharacterMenu, setIgnore } from "../../store/commands.js";
import { closePopout, togglePopout } from "../../store/state.js";
import { Avatar } from "../primitives/Avatar.js";
import { statusLabel } from "./status.js";
import type { MemberInfo } from "../../transport/protocol.js";
import { RosterCharacter } from "./character.js";


// ==========================================================================
// CharacterMenu.ts
// ==========================================================================
// CharacterMenu: the character context menu. Mounted once by the Chatspace
// shell while view.characterMenu is set, anchored at the captured cursor
// position. Roster rows and character links open it through the shared
// delegated click handler (clickHandlers).
// Bookmark and friend management are deliberately absent: both are handled out
// of band on the F-List profile pages and reach the client via RTB friends
// events.

export const CharacterMenu: Mithril.Component = {
	...useEscape(() => () => {
		closeCharacterMenu(useView());
	}),
	view: () => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();

		const menu = view.characterMenu;
		// A menu opened from another tab (or a closed session) is stale.
		if (
			menu === null ||
			menu.session !== view.activeSession ||
			store.sessions[menu.session] === undefined
		) {
			return null;
		}
		const { session, name, x, y, character: snapshot } = menu;
		// Prefer the live registry record when present (fresher), else the
		// snapshot a search result captured.
		const character = store.characters[name] ?? snapshot;
		const ignored = store.ignores.includes(name);
		// A human-readable status under the name. The registry keeps the last
		// status after FLN, so offline wins over a stale one; a known online
		// character with no status reads as Online.
		const stateLabel =
			character === undefined
				? ""
				: !character.online
					? "Offline"
					: statusLabel(character.status) || "Online";
		const close = (): void => {
			closeCharacterMenu(view);
		};

		return [
			m("div.character-menu-overlay", { onclick: close }),
			m(
				"div.character-menu",
				{ style: { left: `${x}px`, top: `${y}px` } },
				[
					m("div.character-menu-header", [
						m(Avatar, { name, size: 48 }),
						m("div.character-menu-identity", [
							m(
								"span.character-menu-name",
								{ class: genderClass(character?.gender) },
								name,
							),
							stateLabel !== ""
								? m("span.character-menu-state", stateLabel)
								: null,
						]),
					]),
					character?.statusMsg !== undefined && character.statusMsg !== ""
						? m(
								"div.character-menu-status",
								m.trust(character.statusMsg),
							)
						: null,
					m("div.character-menu-actions", { role: "menu" }, [
						m(
							"a.button.button-secondary.character-menu-action",
							{
								role: "menuitem",
								href: profileURL(name),
								target: "_blank",
								rel: "noopener noreferrer",
								onclick: close,
							},
							"View profile",
						),
						m(
							"button.button.character-menu-action",
							{
								type: "button",
								role: "menuitem",
								onclick: () => {
									activateConv(store, view, dispatch, session, `dm:${name}`);
									close();
								},
							},
							"Open DM",
						),
						m(
							"button.button.button-secondary.character-menu-action",
							{
								type: "button",
								role: "menuitem",
								onclick: () => {
									setIgnore(store, view, dispatch, session, name, !ignored);
								},
							},
							ignored ? "Unblock" : "Block",
						),
					]),
				],
			),
		];
	},
};

// ==========================================================================
// FriendsMenu.ts
// ==========================================================================
// FriendsMenu: the top-bar friends/bookmarks button and the slot for its
// popout. `.friends-menu` is the mount point; the popout is a separate
// component mounted only while the friends popout slot is open. The popout is
// keyed by session, so switching tabs remounts it.
//
// Activating a contact uses the shared character behavior (desktop left click
// opens the profile, right click opens the CharacterMenu; touch opens the
// menu). It no longer opens a DM directly.


export const FriendsMenu: Mithril.Component = {
	view: () => {
		const store = useStore();
		const view = useView();

		const session = view.activeSession;
		if (session === null || store.sessions[session] === undefined) {
			return null;
		}

		const open = view.popout === "friends";
		const close = (): void => {
			closePopout(view);
		};

		return m("div.friends-menu", [
			m(
				"button.friends-button",
				{
					type: "button",
					class: open ? "is-open" : "",
					title: "Online friends & bookmarks",
					onclick: () => {
						togglePopout(view, "friends");
					},
				},
				"Friends",
			),
			open ? m("div.friends-overlay", { onclick: close }) : null,
			// The slot: mounted only while open. `key: session` forces a remount
			// when the active tab changes. The keyed vnode lives in its own
			// single-element fragment: a fragment's children must be either all
			// keyed or all unkeyed, and the button and overlay siblings above are
			// unkeyed.
			open
				? [
						m(FriendsPopout, {
							key: session,
							friends: store.friends,
						}),
					]
				: null,
		]);
	},
};

// FriendsPopout is the popover itself. The contact set is derived live: it
// reads presence from the store on every redraw, so a friend who comes online
// while the popout is open (including presence that lands after mount during
// hydration) appears without reopening. Only the name order is cached, keyed
// on the friends array reference.
interface FriendsPopoutAttrs {
	/** Raw FRL union for the active session. */
	friends: MemberInfo[];
}

interface FriendsPopoutState {
	/** source is the friends array the cached order was built from. */
	source?: MemberInfo[];
	/** sorted names of `source`, cached against the array reference. */
	sorted: string[];
}

const FriendsPopout: Mithril.Component<FriendsPopoutAttrs> = {
	oninit: (vnode) => {
		(vnode.state as FriendsPopoutState).sorted = [];
	},
	view: (vnode) => {
		const store = useStore();
		const state = vnode.state as FriendsPopoutState;
		const { friends } = vnode.attrs;

		if (state.source !== friends) {
			state.source = friends;
			state.sorted = friends
				.map((f) => f.name)
				.sort((a, b) => a.localeCompare(b));
		}
		// Read online status live; presence is coalesced upstream, so this does
		// not run per presence event.
		const contacts: string[] = [];
		for (const name of state.sorted) {
			if (store.characters[name]?.online === true) {
				contacts.push(name);
			}
		}

		// One delegated handler for the whole list: a row activates the shared
		// character behavior. The popover is dismissed by its own overlay or the
		// Friends button, not by selecting a contact.
		return m(
			"div.friends-popover",
			[
				m("h3.friends-popover-title", "Online friends & bookmarks"),
				contacts.length === 0
					? m("p.friends-empty.muted", "No one online.")
					: m(
							"ul.friends-list",
							contacts.map((name) =>
								m(
									"li",
									{ key: name },
									m(
										"button.friends-item",
										{ type: "button", "data-character": name },
										m(RosterCharacter, {
											character: store.characters[name] ?? {
												name,
												online: false,
											},
										}),
									),
								),
							),
						),
			],
		);
	},
};
