// commands.ts — the main command palette shell. A shell is an invisible
// container: it owns one palette's data and behavior and renders only the
// shared Palette primitive, so the visual component stays reusable across
// unrelated commands.
//
// This shell adds one thing over the conversation-jump shell: its contents are
// a stack of CommandLists. A row marked `subcommand` carries the list it opens
// in its `value`, and choosing it swaps `state.current` for that list, so a
// single modal can drill from the command menu into a further palette (statuses,
// for example) without the shell branching on which list is showing.
//
// The shell's view never changes with the list: it builds the shared
// CommandContext, asks the current list for rows, and hands them to Palette.
// The lists hold the rows and the action; MainCommandList is the root.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useActions, useDispatch, useStore, useView, type AppActions, type Dispatch } from "../../context.js";
import { profileURL } from "../../lib/characters.js";
import { request } from "../../render.js";
import { activateConv, dismissConv, setStatus } from "../../store/commands.js";
import {
	closeModal,
	pushToast,
	type Conversation,
	type Store,
	type View,
} from "../../store/state.js";
import { FeaturedCharacter } from "../presence/character.js";
import { STATUS_OPTIONS } from "../presence/status.js";
import { Palette, type PaletteItem } from "../primitives/palette.js";

/** CommandContext is the read/act surface handed to a command list's `list`
 * and `onSelect`: the live store and view, the dispatcher, and pointers into
 * the active session captured when the palette renders. A list reads the store
 * through it rather than through its own imports, so the same list can be
 * exercised in isolation. */
export interface CommandContext {
	store: Store;
	view: View;
	dispatch: Dispatch;
	/** actions is the composition root's action surface (joins, logins). */
	actions: AppActions;
	/** session is the active character's session. */
	session: string;
	/** currentConv is the active conversation, when the session has one. */
	currentConv?: Conversation;
}

/** CommandList is one palette's worth of commands. `list` produces the rows for
 * the given context; `onSelect` runs the action for a chosen leaf row. A row
 * marked `subcommand` is handled by the shell (it swaps the list), so a
 * subcommand list's `onSelect` is never reached. The metadata fields label the
 * palette while that list is showing. */
export interface CommandList {
	/** id is the list's stable identity. The shell keys the palette on it, so a
	 * swap resets the input and highlight instead of carrying a filter over. */
	id: string;
	/** placeholder is the palette input prompt for this list. */
	placeholder: string;
	/** emptyText is shown when this list produces no rows for the context. */
	emptyText: string;
	/** list returns the rows to show, already filtered for the context. */
	list: (context: CommandContext) => PaletteItem[];
	/** onSelect runs this list's action for a chosen leaf row. Omitted when every
	 * row is a subcommand, so the shell never needs to call it. */
	onSelect?: (item: PaletteItem, context: CommandContext) => void;
}

/** StatusCommandList lets the user set their own status from the palette. It
 * deliberately leaves the status message alone (the stub's "quickly set status,
 * doesn't change the message"): the current message is re-sent so the server
 * keeps it, exactly as StatusDialog prefills it. */
const StatusCommandList: CommandList = {
	id: "status",
	placeholder: "Set status",
	emptyText: "No statuses",
	list: () =>
		STATUS_OPTIONS.map((option) => ({
			id: `status:${option.value}`,
			title: `${option.mark} ${option.label}`.trim(),
			// The label is what is shown; the value lets "dnd" match
			// "Do not disturb".
			filterable: `${option.label} ${option.value}`,
			value: option.value,
		})),
	onSelect: (item, context) => {
		const status = typeof item.value === "string" ? item.value : "";
		if (status === "") {
			return;
		}
		const message = context.store.sessions[context.session]?.selfStatusText ?? "";
		setStatus(
			context.store,
			context.view,
			context.dispatch,
			context.session,
			status,
			message,
		);
	},
};

/** openProfile opens a character's F-List page in a new tab, mirroring the
 * roster menu. A null opener keeps the new tab from reaching back into the
 * app. */
function openProfile(name: string): void {
	const w = window.open(profileURL(name), "_blank");
	if (w !== null) {
		w.opener = null;
	}
}

/** friendActions builds the subcommand list for one friend/bookmark. It is made
 * per contact rather than shared because its actions close over the name; the
 * row that opens it carries it in `value`. */
function friendActions(name: string): CommandList {
	return {
		id: `friend-actions:${name}`,
		placeholder: name,
		emptyText: "No actions",
		list: () => [
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
		],
		onSelect: (item, context) => {
			if (item.id === "open-dm") {
				activateConv(
					context.store,
					context.view,
					context.dispatch,
					context.session,
					`dm:${name}`,
				);
			} else if (item.id === "open-profile") {
				openProfile(name);
			}
		},
	};
}

/** FriendsCommandList lists the online friends and bookmarks (alphabetically)
 * as FeaturedCharacter rows; each is a subcommand that opens that contact's
 * actions. The name is the filterable text (the title is the rendered row). All
 * rows are subcommands, so the list itself has no onSelect. */
const FriendsCommandList: CommandList = {
	id: "friends",
	placeholder: "Friends & bookmarks",
	emptyText: "No friends or bookmarks online",
	list: (context) => {
		const names = context.store.friends
			.map((friend) => friend.name)
			.sort((a, b) => a.localeCompare(b));
		const items: PaletteItem[] = [];
		for (const name of names) {
			const character = context.store.characters[name];
			if (character?.online !== true) {
				continue;
			}
			items.push({
				id: `friend:${name}`,
				title: m(FeaturedCharacter, { character }),
				filterable: name,
				subcommand: true,
				value: friendActions(name),
			});
		}
		return items;
	},
};

/** joinCatalogEntry sends a join for one catalog row and reports a failure as
 * a toast. The conversation itself is created by the server's JCH a round trip
 * later; like the join dialog, the palette does not open it. */
function joinCatalogEntry(
	context: CommandContext,
	kind: "official" | "room",
	name: string,
): void {
	void context.actions.joinChannel(context.session, kind, name).then((err) => {
		if (err !== null) {
			pushToast(context.view, `Could not join ${name}: ${err}`);
		}
		request();
	});
}

/** OfficialChannelsCommandList lists the catalog's official channels. Choosing
 * one joins it; the server's JCH creates the conversation, which the sidebar
 * then shows. It is a leaf list, so selecting sends the join and closes. */
const OfficialChannelsCommandList: CommandList = {
	id: "join-official",
	placeholder: "Join channel",
	emptyText: "Channel list isn't available yet",
	list: (context) =>
		context.store.channels.official.map((channel) => ({
			id: `official:${channel.name}`,
			title: channel.name,
			description: `${channel.characters} online`,
			filterable: channel.name,
			value: channel.name,
		})),
	onSelect: (item, context) => {
		if (typeof item.value === "string") {
			joinCatalogEntry(context, "official", item.value);
		}
	},
};

/** RoomsCommandList lists the catalog's public rooms, labeled by their title
 * but joined by name. It mirrors the join dialog's room tab. */
const RoomsCommandList: CommandList = {
	id: "join-room",
	placeholder: "Join room",
	emptyText: "Room list isn't available yet",
	list: (context) =>
		context.store.channels.rooms.map((room) => {
			const label = room.title !== "" ? room.title : room.name;
			return {
				id: `room:${room.name}`,
				title: label,
				description: `${room.characters} online`,
				filterable: label === room.name ? room.name : `${label} ${room.name}`,
				value: room.name,
			};
		}),
	onSelect: (item, context) => {
		if (typeof item.value === "string") {
			joinCatalogEntry(context, "room", item.value);
		}
	},
};

/** JoinCommandList splits the catalog into its two kinds before the picker, so
 * the user chooses channel or room first. Both rows are subcommands, so the
 * list itself has no onSelect. */
const JoinCommandList: CommandList = {
	id: "join",
	placeholder: "Join",
	emptyText: "No join targets",
	list: () => [
		{
			id: "join-official",
			title: "Join Channel",
			description: "Join an official F-Chat channel.",
			filterable: "Join Channel",
			subcommand: true,
			value: OfficialChannelsCommandList,
		},
		{
			id: "join-room",
			title: "Join Room",
			description: "Join a public room.",
			filterable: "Join Room",
			subcommand: true,
			value: RoomsCommandList,
		},
	],
};

/** MainCommandList is the root menu: the actions that always make sense, plus
 * one conversation action chosen from the active conversation's kind. */
const MainCommandList: CommandList = {
	id: "main",
	placeholder: "Commands",
	emptyText: "No commands",
	list: (context) => {
		const items: PaletteItem[] = [
			{
				id: "set-status",
				title: "Set Status",
				description: "Quickly set status, doesn't change the message.",
				filterable: "Set Status",
				subcommand: true,
				value: StatusCommandList,
			},
			{
				id: "friends",
				title: "Friends & Bookmarks",
				description: "Open a DM or profile for a friend or bookmark.",
				filterable: "Friends & Bookmarks",
				subcommand: true,
				value: FriendsCommandList,
			},
			{
				id: "join",
				title: "Join Channel",
				description: "Join an official channel or a public room.",
				filterable: "Join Channel",
				subcommand: true,
				value: JoinCommandList,
			},
		];
		const conv = context.currentConv;
		if (conv === undefined) {
			return items;
		}
		if (conv.conv.kind === "dm") {
			items.push({
				id: "close-dm",
				title: "Close DM",
				description: "Hide this direct message.",
				filterable: "Close DM",
			});
		} else if (conv.conv.kind === "official" || conv.conv.kind === "room") {
			items.push({
				id: "leave-channel",
				title: "Leave Channel",
				description: "Leave this conversation.",
				filterable: "Leave Channel",
			});
		}
		return items;
	},
	onSelect: (item, context) => {
		const conv = context.currentConv;
		if (conv === undefined) {
			return;
		}
		if (item.id === "close-dm" || item.id === "leave-channel") {
			dismissConv(
				context.store,
				context.view,
				context.dispatch,
				context.session,
				conv.key,
			);
		}
	},
};

/** isCommandList narrows a row's `value` to the subcommand list it carries. */
function isCommandList(value: unknown): value is CommandList {
	return (
		typeof value === "object" &&
		value !== null &&
		"list" in value &&
		typeof (value as { list: unknown }).list === "function"
	);
}

/** CommandShellState is the shell's local state: the list currently showing, the
 * query the palette reports, and the row that list was opened from (shown by
 * the palette as previous context). */
interface CommandShellState {
	current: CommandList;
	query: string;
	previous: PaletteItem | undefined;
}

/** CommandShell is the main command palette. Mount it in the modal slot; it
 * renders only the shared Palette and delegates every row to the current
 * CommandList. A leaf selection closes the palette. */
export const CommandShell: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as CommandShellState;
		state.current = MainCommandList;
		state.query = "";
		state.previous = undefined;
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const actions = useActions();
		const state = vnode.state as CommandShellState;
		const session = view.activeSession;
		if (session === null) {
			// No live session means nothing to command. The shell only closes on
			// selection, so it renders empty rather than reaching for a redraw.
			return null;
		}
		const activeKey = view.activeConv[session];
		const context: CommandContext = {
			store,
			view,
			dispatch,
			actions,
			session,
			currentConv:
				activeKey === undefined
					? undefined
					: store.conversations[session]?.[activeKey],
		};
		// The palette goes in a single-element keyed fragment: a key only remounts
		// within a fragment, and remounting on a list swap is what resets the
		// highlight to the first row and clears the typed filter.
		return [
			m(Palette, {
				key: state.current.id,
				items: state.current.list(context),
				query: state.query,
				minInput: 0,
				placeholder: state.current.placeholder,
				emptyText: state.current.emptyText,
				previousItem: state.previous,
				onQuery: (query: string) => {
					state.query = query;
				},
				onSelect: (item: PaletteItem) => {
					if (item.subcommand === true && isCommandList(item.value)) {
						state.previous = item;
						state.current = item.value;
						state.query = "";
						return;
					}
					state.current.onSelect?.(item, context);
					closeModal(view);
				},
				onClose: () => closeModal(view),
			}),
		];
	},
};