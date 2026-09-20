// menus.ts — the character context menu and the top-bar friends popout.
// Absorbs CharacterMenu.ts and FriendsMenu.ts.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useActions, useDispatch, useStore, useView } from "../../context.js";
import { memo, memoInit, type Memo } from "../../render.js";
import { genderClass, profileURL } from "../../lib/characters.js";
import { useEscape } from "../primitives/dialog.js";
import { PopoutMenu } from "../primitives/popout.js";
import { activateConv, closeCharacterMenu, setIgnore } from "../../store/commands.js";
import { closePopout, pushToast, togglePopout } from "../../store/state.js";
import { memberActionNotice, roomOps } from "../../lib/moderation.js";
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
		const actions = useActions();

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
		// Room moderation for the active conversation. The shared interface gates
		// each action; a DM, a non-member target, or a character lacking authority
		// yields no items. Feedback is a toast, since the menu closes on action.
		const activeKey = view.activeConv[session];
		const roomConv =
			activeKey !== undefined
				? store.conversations[session]?.[activeKey]
				: undefined;
		const moderation =
			roomConv !== undefined
				? roomOps(
						store,
						actions,
						session,
						roomConv,
						(action, target, error) => {
							pushToast(
								view,
								error !== null ? error : memberActionNotice(action, target),
							);
						},
					)
				: null;
		const roomItems: Mithril.Children[] = [];
		if (moderation !== null) {
			const caps = moderation.capabilities({
				name,
				admin: character?.admin === true,
			});
			const add = (label: string, run: () => void, danger = false): void => {
				roomItems.push(
					m(MenuAction, {
						label,
						danger,
						onAction: () => {
							run();
							close();
						},
					}),
				);
			};
			if (caps.op) {
				add("Make moderator", () => {
					void moderation.op(name);
				});
			}
			if (caps.deop) {
				add("Remove moderator", () => {
					void moderation.deop(name);
				});
			}
			if (caps.kick) {
				add(
					"Kick",
					() => {
						void moderation.kick(name);
					},
					true,
				);
			}
			if (caps.ban) {
				add(
					"Ban",
					() => {
						void moderation.ban(name);
					},
					true,
				);
			}
		}
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
						m(MenuAction, {
							label: "View profile",
							href: profileURL(name),
							onAction: close,
						}),
						m(MenuAction, {
							label: "Open DM",
							onAction: () => {
								activateConv(store, view, dispatch, session, `dm:${name}`);
								close();
							},
						}),
						m(MenuAction, {
							label: ignored ? "Unblock" : "Block",
							onAction: () => {
								setIgnore(store, view, dispatch, session, name, !ignored);
							},
						}),
						roomItems.length > 0
							? m("div.character-menu-divider")
							: null,
						...roomItems,
					]),
				],
			),
		];
	},
};

/** MenuAction is one item in the character menu's action list: an external link
 * when `href` is set, otherwise a button. `danger` marks a destructive item; the
 * caller closes the menu in `onAction`. */
interface MenuActionAttrs {
	label: string;
	onAction: () => void;
	href?: string;
	danger?: boolean;
}

const MenuAction: Mithril.Component<MenuActionAttrs> = {
	view: ({ attrs }) => {
		const cls =
			attrs.danger === true
				? "character-menu-item is-danger"
				: "character-menu-item";
		if (attrs.href !== undefined) {
			return m(
				"a",
				{
					class: cls,
					role: "menuitem",
					href: attrs.href,
					target: "_blank",
					rel: "noopener noreferrer",
					onclick: attrs.onAction,
				},
				attrs.label,
			);
		}
		return m(
			"button",
			{
				class: cls,
				type: "button",
				role: "menuitem",
				onclick: attrs.onAction,
			},
			attrs.label,
		);
	},
};

// ==========================================================================
// FriendsMenu.ts
// ==========================================================================
// FriendsMenu: the top-bar friends/bookmarks button and the slot for its
// popout. It drives the shared `PopoutMenu` shell; the popout is a separate
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
		return m(
			PopoutMenu,
			{
				label: "Friends",
				title: "Online friends & bookmarks",
				buttonClass: "friends-button",
				open,
				onToggle: () => togglePopout(view, "friends"),
				onClose: () => closePopout(view),
			},
			// `key: session` remounts the popover when the active tab changes.
			open ? [m(FriendsPopout, { key: session, friends: store.friends })] : null,
		);
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
	/** sorted memoizes the name order against the friends array reference. */
	sorted: Memo<string[]>;
}

const FriendsPopout: Mithril.Component<FriendsPopoutAttrs> = {
	oninit: (vnode) => {
		(vnode.state as FriendsPopoutState).sorted = memoInit<string[]>();
	},
	view: (vnode) => {
		const store = useStore();
		const state = vnode.state as FriendsPopoutState;
		const { friends } = vnode.attrs;

		const sorted = memo(state.sorted, [friends], () =>
			friends.map((f) => f.name).sort((a, b) => a.localeCompare(b)),
		);
		// Read online status live; presence is coalesced upstream, so this does
		// not run per presence event.
		const contacts: string[] = [];
		for (const name of sorted) {
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
