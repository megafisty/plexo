// characters.ts — the character picker shell (Ctrl/Cmd-K). An invisible
// container that owns one palette's data and behavior and renders only the
// shared Palette primitive.
//
// Its contents are a stack of CommandLists, exactly like the main command menu:
// a row carrying `next` holds the list it opens, and choosing it swaps
// `state.current`. The picker has two roots: the active channel/room's member
// roster, and the "seen" characters (every character the client holds a live
// online presence record for), headed by the session's recently closed DM
// partners. A DM-only character (no shared channel, no presence record) is
// reachable from those recent rows. While a channel/room is active the two
// roots are switchable with Ctrl-K, which swaps `current` without clearing the
// typed filter. A DM/warp/none open has no roster, so it can only ever show the
// seen list.
//
// A character row opens a per-character action list. Open DM / Open Profile are
// always present; a channel-member list additionally offers a Moderator Actions
// subcommand when the authority snapshot authorizes a verb (op/deop/kick/ban).
// The moderator list carries the ops interface that runs its verbs.
//
// The Palette materializes each list once and never tracks live changes, so a
// presence or role change while the picker is open is not seen; the server
// remains the moderation authority. The wrapper div catches the toggle chord
// before it can reach the global shortcut listener and before the browser can
// steal focus; it only toggles from a root list in a channel/room.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useActions, useDispatch, useStore, useView } from "../../context.js";
import { loggedInNames, openProfile, seenOnlineNames } from "../../lib/characters.js";
import { activeConv, isMemberConv } from "../../lib/conversations.js";
import {
	memberActionNotice,
	roomOps,
	type MemberCapabilities,
	type RoomOps,
} from "../../lib/moderation.js";
import { rosterRank, sortRosterNames } from "../../lib/order.js";
import { request } from "../../render.js";
import { activateConv } from "../../store/commands.js";
import {
	closeCommand,
	pushToast,
	type Conversation,
	type View,
} from "../../store/state.js";
import { RosterCharacter, moderatorFor } from "../presence/character.js";
import { Palette } from "../primitives/palette.js";
import { type CommandAttrs, type CommandContext, type CommandItem, type CommandList } from "./list.js";

const NO_MEMBERS: string[] = [];
const NO_OPS: string[] = [];

/** CharacterMode names which root list is showing. */
type CharacterMode = "roster" | "seen";

interface CharacterSearchState {
	query: string;
	/** current is the CommandList the palette shows. */
	current: CommandList;
	/** previous is the display-only row above the input: the toggle hint at a
	 * root, or a plain header for the character whose actions are showing. */
	previous?: CommandItem;
	/** mode is the root list the user last chose; ignored outside a channel. */
	mode: CharacterMode;
	/** atRoot is true while a root list shows; the toggle only switches roots,
	 * never an action list. */
	atRoot: boolean;
	/** selected is the character whose action list is showing, for the previous
	 * header only. Undefined at a root. */
	selected?: string;
}

/** recentNames returns a session's recently closed DM partners, newest first.
 * The buffer is stored oldest first (a FIFO), so the picker reverses it to put
 * the most recent close at the top. */
function recentNames(view: View, session: string): readonly string[] {
	const buffer = view.recentDms[session];
	if (buffer === undefined || buffer.length === 0) {
		return NO_MEMBERS;
	}
	return [...buffer].reverse();
}

/** toggleHint builds the fake previous item that states the current list and
 * advertises the Ctrl-K switch. It is display-only: the shell gates the switch
 * on a root list in a channel/room, not on this item's presence. */
function toggleHint(mode: CharacterMode): CommandItem {
	if (mode === "roster") {
		return {
			id: "toggle:seen",
			title: "Channel members",
			description: "Press Ctrl-K again to view every character you've seen.",
			filterable: "",
		};
	}
	return {
		id: "toggle:roster",
		title: "Seen characters",
		description: "Press Ctrl-K to view only this channel's members.",
		filterable: "",
	};
}

/** moderatorItems is the Moderator Actions sub-list: the member verbs the
 * capability snapshot authorized, in menu order. Empty when none is allowed. */
function moderatorItems(
	name: string,
	caps: MemberCapabilities,
): CommandItem[] {
	const items: CommandItem[] = [];
	if (caps.op) {
		items.push({
			id: "op",
			title: "Make moderator",
			description: `Add ${name} as a room moderator.`,
			filterable: "Make moderator",
		});
	}
	if (caps.deop) {
		items.push({
			id: "deop",
			title: "Remove moderator",
			description: `Remove ${name} as a room moderator.`,
			filterable: "Remove moderator",
		});
	}
	if (caps.kick) {
		items.push({
			id: "kick",
			title: "Kick",
			description: `Kick ${name} from this channel.`,
			filterable: "Kick",
		});
	}
	if (caps.ban) {
		items.push({
			id: "ban",
			title: "Ban",
			description: `Ban ${name} from this channel.`,
			filterable: "Ban",
		});
	}
	return items;
}

/** characterActionsList is the per-character action list. Open DM and Open
 * Profile are always present; a channel-member list additionally offers a
 * Moderator Actions subcommand when the authority snapshot authorized at least
 * one verb. The snapshot is taken when the list materializes (on drilling in),
 * so every row is decided before it is shown. */
function characterActionsList(
	name: string,
	conv: Conversation | undefined,
): CommandList {
	return {
		id: `character:${name}`,
		placeholder: "Choose an action",
		emptyText: "No actions",
		list: (context) => {
			const items: CommandItem[] = [
				{
					id: "open-dm",
					title: "Open DM",
					description: `Start or reopen the direct message with ${name}.`,
					filterable: "Open DM",
				},
				{
					id: "open-profile",
					title: "Open Profile",
					description: `Open ${name}'s F-List profile in a new tab.`,
					filterable: "Open Profile",
				},
			];
			if (conv !== undefined) {
				const ops = roomOps(
					context.store,
					context.actions,
					context.session,
					conv,
					(action, target, error) => {
						pushToast(
							context.view,
							error !== null ? error : memberActionNotice(action, target),
						);
					},
				);
				const mods = moderatorItems(
					name,
					ops.capabilities({
						name,
						admin: context.store.characters[name]?.admin === true,
					}),
				);
				if (mods.length > 0) {
					items.push({
						id: "moderator-actions",
						title: "Moderator Actions",
						description: `Moderate ${name} in this channel.`,
						filterable: "Moderator Actions",
						next: moderatorActionList(name, mods, ops),
					});
				}
			}
			return items;
		},
		onSelect: (item, context) => {
			switch (item.id) {
				case "open-dm":
					activateConv(
						context.store,
						context.view,
						context.dispatch,
						context.session,
						`dm:${name}`,
					);
					break;
				case "open-profile":
					openProfile(name);
					break;
				default:
					break;
			}
		},
	};
}

/** moderatorActionList is the Moderator Actions sub-list: the verbs the
 * capability snapshot authorized, carrying the ops interface that runs them. */
function moderatorActionList(
	name: string,
	items: CommandItem[],
	ops: RoomOps,
): CommandList {
	return {
		id: `moderator:${name}`,
		placeholder: "Moderator actions",
		emptyText: "No actions",
		list: () => items,
		onSelect: (item) => {
			switch (item.id) {
				case "op":
					void ops.op(name);
					break;
				case "deop":
					void ops.deop(name);
					break;
				case "kick":
					void ops.kick(name);
					break;
				case "ban":
					void ops.ban(name);
					break;
				default:
					break;
			}
		},
	};
}

/** buildRosterItems maps the active channel/room's ranked member list to rows,
 * each a subcommand opening that character's action list. */
function buildRosterItems(context: CommandContext): CommandItem[] {
	const conv = context.currentConv;
	if (conv === undefined || !isMemberConv(conv)) {
		return [];
	}
	const ops = new Set(conv.ops ?? NO_OPS);
	const friendSet = new Set(context.store.friends.map((f) => f.name));
	const names = sortRosterNames(conv.members ?? NO_MEMBERS, (name) =>
		rosterRank({
			isAdmin: context.store.characters[name]?.admin === true,
			isOp: ops.has(name),
			isFriend: friendSet.has(name),
		}),
	);
	return names.map((name) => {
		const record = context.store.characters[name];
		return {
			id: name,
			title: m(RosterCharacter, {
				character: record ?? { name, online: false },
				moderator: moderatorFor(record, ops, name),
			}),
			filterable: name,
			next: characterActionsList(name, conv),
		};
	});
}

/** buildSeenItems maps the online characters to rows. Recently closed DM
 * partners head the list and are kept out of the alphabetical body so the same
 * character never appears twice; a partner with a live presence record still
 * shows it on their recent row. No moderator mark: the seen list never shows
 * one, so its action lists carry no channel. */
function buildSeenItems(context: CommandContext): CommandItem[] {
	const store = context.store;
	const seen = seenOnlineNames(store.characters, loggedInNames(store.sessions));
	const recent = recentNames(context.view, context.session);
	const body =
		recent.length === 0 ? seen : seen.filter((name) => !recent.includes(name));
	const items: CommandItem[] = [];
	for (const name of recent) {
		items.push({
			id: name,
			title: m(RosterCharacter, {
				character: store.characters[name] ?? { name, online: false },
			}),
			description: "Recently closed DM",
			filterable: name,
			next: characterActionsList(name, undefined),
		});
	}
	for (const name of body) {
		items.push({
			id: name,
			title: m(RosterCharacter, {
				character: store.characters[name] ?? { name, online: false },
			}),
			filterable: name,
			next: characterActionsList(name, undefined),
		});
	}
	return items;
}

/** RosterList is the channel/room member root. */
const RosterList: CommandList = {
	id: "roster",
	placeholder: "Search members",
	emptyText: "This channel's members aren't loaded yet.",
	list: buildRosterItems,
};

/** SeenList is the "seen characters" root. */
const SeenList: CommandList = {
	id: "seen",
	placeholder: "Search characters",
	emptyText: "No characters are online.",
	list: buildSeenItems,
};

/** subcommandPrevious builds the display-only header above the input for a
 * drilled list: from a root it states the chosen character; from the action
 * list it states the moderator sub-list. */
function subcommandPrevious(
	state: CharacterSearchState,
	item: CommandItem,
): CommandItem {
	if (state.atRoot) {
		state.selected = item.id;
		return {
			id: `selected:${item.id}`,
			title: item.id,
			description: "Choose an action.",
			filterable: "",
		};
	}
	const name = state.selected ?? "";
	return {
		id: `moderator:${name}`,
		title: name,
		description: "Moderator actions.",
		filterable: "",
	};
}

export const CharacterSearch: Mithril.Component<CommandAttrs> = {
	oninit: (vnode) => {
		const state = vnode.state as CharacterSearchState;
		state.query = "";
		state.previous = undefined;
		state.selected = undefined;
		state.atRoot = true;
		const store = useStore();
		const view = useView();
		// Open on the channel roster when the active conversation has one.
		state.mode = isMemberConv(activeConv(store, view)) ? "roster" : "seen";
		state.current = state.mode === "roster" ? RosterList : SeenList;
		state.previous = state.mode === "roster" ? toggleHint(state.mode) : undefined;
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const actions = useActions();
		const state = vnode.state as CharacterSearchState;
		const session = view.activeSession;
		if (session === null) {
			return null;
		}
		const currentConv = activeConv(store, view);
		const hasRoster = isMemberConv(currentConv);
		const context: CommandContext = {
			store,
			view,
			dispatch,
			actions,
			session,
			currentConv,
		};

		const onToggleKey = (e: KeyboardEvent): void => {
			const mod = e.ctrlKey || e.metaKey;
			if (!mod || e.altKey || e.shiftKey || (e.key !== "k" && e.key !== "K")) {
				return;
			}
			// Always claim the chord: otherwise the browser may steal focus out of
			// the palette. Only a root list in a channel/room toggles, and the
			// typed filter carries across the swap.
			e.preventDefault();
			e.stopPropagation();
			if (!hasRoster || !state.atRoot) {
				return;
			}
			state.mode = state.mode === "roster" ? "seen" : "roster";
			state.current = state.mode === "roster" ? RosterList : SeenList;
			state.previous = toggleHint(state.mode);
			request();
		};

		return m("div", { onkeydown: onToggleKey }, [
			m(Palette, {
				// A root swap keeps the same key so the filter survives Ctrl-K; any
				// drill changes it so the input and highlight reset. The palette
				// rematerializes itself when the list id changes.
				key: state.atRoot ? "root" : state.current.id,
				list: state.current,
				context,
				query: state.query,
				minInput: 0,
				previousItem: state.previous,
				onQuery: (query: string) => {
					state.query = query;
				},
				onSubcommand: (item: CommandItem) => {
					if (item.next === undefined) {
						return;
					}
					state.previous = subcommandPrevious(state, item);
					state.current = item.next;
					state.atRoot = false;
					state.query = "";
					request();
				},
				onSelect: () => closeCommand(view),
				onClose: () => closeCommand(view),
			}),
		]);
	},
};