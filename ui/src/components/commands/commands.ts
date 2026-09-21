// commands.ts — the main command palette shell. A shell is an invisible
// container: it owns one palette's data and behavior and renders only the
// shared Palette primitive, so the visual component stays reusable across
// unrelated commands.
//
// This shell adds one thing over the conversation-jump shell: its contents are
// a stack of CommandLists. A row carrying `next` holds the list it opens, and
// choosing it swaps `state.current` for that list, so a single modal can drill
// from the command menu into a further palette (statuses, for example) without
// the shell branching on which list is showing.
//
// The shell's view never changes with the list: it builds the shared
// CommandContext, hands the current list and context to Palette, and decides
// what a swap means. The lists hold the rows and the action; MainCommandList is
// the root.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useActions, useDispatch, useStore, useView } from "../../context.js";
import { activeConv, isChannelKind } from "../../lib/conversations.js";
import { unionFriends } from "../../lib/friends.js";
import { request } from "../../render.js";
import { dismissConv, setStatus } from "../../store/commands.js";
import { closeCommand, pushToast } from "../../store/state.js";
import { FeaturedCharacter, RosterCharacter } from "../presence/character.js";
import { STATUS_OPTIONS } from "../presence/status.js";
import { Palette } from "../primitives/palette.js";
import { CharacterActionsList } from "./characterActions.js";
import { type CommandAttrs, type CommandContext, type CommandItem, type CommandList } from "./list.js";

/** StatusCommandList lets the user set their own status from the palette. It
 * deliberately leaves the status message alone (the stub's "quickly set status,
 * doesn't change the message"): the current message is re-sent so the server
 * keeps it, exactly as StatusDialog prefills it. */
const StatusCommandList: CommandList<string> = {
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

/** FriendsCommandList lists the online friends and bookmarks (alphabetically)
 * as FeaturedCharacter rows; each is a subcommand that opens that contact's
 * actions. The name is the filterable text (the title is the rendered row). All
 * rows are subcommands, so the list itself has no onSelect. */
const FriendsCommandList: CommandList = {
	id: "friends",
	placeholder: "Friends & bookmarks",
	emptyText: "No friends or bookmarks online",
	list: (context) => {
		const names = unionFriends(context.store)
			.map((friend) => friend.name)
			.sort((a, b) => a.localeCompare(b));
		const items: CommandItem[] = [];
		for (const name of names) {
			const character = context.store.characters[name];
			if (character?.online !== true) {
				continue;
			}
			items.push({
				id: `friend:${name}`,
				title: m(FeaturedCharacter, { character }),
				filterable: name,
				next: CharacterActionsList,
				value: { name, source: "friends" },
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
const OfficialChannelsCommandList: CommandList<string> = {
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
const RoomsCommandList: CommandList<string> = {
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
			next: OfficialChannelsCommandList,
		},
		{
			id: "join-room",
			title: "Join Room",
			description: "Join a public room.",
			filterable: "Join Room",
			next: RoomsCommandList,
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
		const items: CommandItem[] = [
			{
				id: "set-status",
				title: "Set Status",
				description: "Quickly set status, doesn't change the message.",
				filterable: "Set Status",
				next: StatusCommandList,
			},
			{
				id: "friends",
				title: "Friends & Bookmarks",
				description: "Open a DM or profile for a friend or bookmark.",
				filterable: "Friends & Bookmarks",
				next: FriendsCommandList,
			},
			{
				id: "join",
				title: "Join Channel",
				description: "Join an official channel or a public room.",
				filterable: "Join Channel",
				next: JoinCommandList,
			},
		];
		const conv = context.currentConv;
		if (conv === undefined) {
			return items;
		}
		if (conv.conv.kind === "dm") {
			const name = conv.conv.id;
			items.unshift({
				id: "character-actions",
				title: m(RosterCharacter, {
					character: context.store.characters[name] ?? {
						name,
						online: false,
					},
				}),
				description: "Profile, bookmark, and other character actions.",
				filterable: `${name} profile bookmark character actions`,
				next: CharacterActionsList,
				value: { name, source: "dm" },
			});
			items.push({
				id: "close-dm",
				title: "Close DM",
				description: "Close this conversation.",
				filterable: "Close DM",
			});
		} else if (isChannelKind(conv.conv.kind)) {
			items.push({
				id: "leave-channel",
				title: "Leave Channel",
				description: "Leave this channel.",
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

/** CommandShellState is the shell's local state: the list currently showing, the
 * query the palette reports, and the context item the current list was opened
 * from (forwarded to the palette as `previousItem`, for display and for a
 * subcommand list to read its target). The Palette owns the materialized rows. */
interface CommandShellState {
	current: CommandList;
	query: string;
	previous: CommandItem | undefined;
}

/** CommandShell is the main command palette. Mount it in the modal slot; it
 * renders only the shared Palette and delegates every row to the current
 * CommandList. A leaf selection closes the palette. */
export const CommandShell: Mithril.Component<CommandAttrs> = {
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
		const context: CommandContext = {
			store,
			view,
			dispatch,
			actions,
			session,
			currentConv: activeConv(store, view),
		};
		// The palette goes in a single-element keyed fragment: a key only remounts
		// within a fragment, and remounting on a list swap is what resets the
		// highlight to the first row and clears the typed filter.
		return [
			m(Palette, {
				key: state.current.id,
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
					state.previous = item.next.transformPrevious?.(item) ?? item;
					state.current = item.next;
					state.query = "";
					request();
				},
				onSelect: () => closeCommand(view),
				onClose: () => closeCommand(view),
			}),
		];
	},
};
