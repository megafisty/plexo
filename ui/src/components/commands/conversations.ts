// conversations.ts — the command palette shells. A shell is an invisible container:
// it owns one palette's data and behavior and renders only the shared Palette
// primitive, so the visual component stays reusable across unrelated commands.
// A shell may act on a selection however it likes — close, or replace its item
// set (subcommands) — because the palette only reports the selection.
//
// New shells are added to COMMANDS and named by CommandId; shell.ts mounts the
// one the open modal names.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useActions, useDispatch, useStore, useView } from "../../context.js";
import { convTitle, orderedConversations } from "../../lib/order.js";
import { activateConv } from "../../store/commands.js";
import { closeModal, type CommandId, type Conversation } from "../../store/state.js";
import { Palette } from "../primitives/palette.js";
import { CharacterSearch } from "./characters.js";
import { CommandShell } from "./commands.js";
import { type CommandContext, type CommandItem, type CommandList } from "./list.js";

// ==========================================================================
// conversation jump
// ==========================================================================
// Jump to a conversation in the active session by name. It reuses the
// sidebar's ordering and the normal activateConv path, so the jump behaves
// exactly like clicking the row. Like the other two shells it drives the
// Palette from a CommandList; this one is a single leaf list, so it never
// drills.

interface ConversationJumpState {
	query: string;
}

/** convKindLabel names a conversation's kind for the row's description line. */
function convKindLabel(conv: Conversation): string {
	switch (conv.conv.kind) {
		case "official":
			return "Channel";
		case "room":
			return "Room";
		case "dm":
			return "Direct message";
		case "warp":
			return "Warpmark";
		case "broadcast":
			return "Broadcast";
	}
}

/** convItem maps one conversation to a palette row. The displayed title is the
 * filterable text (and falls back to the id, so an untitled DM is searchable by
 * name); the precomputed `value` hands the selection a plain conversation key. */
function convItem(conv: Conversation): CommandItem<string> {
	return {
		id: conv.key,
		title: convTitle(conv),
		description: convKindLabel(conv),
		filterable: convTitle(conv),
		value: conv.key,
	};
}

/** resultKey resolves the conversation key from a selected row: the
 * precomputed value, or the item's id when no value was set. */
function resultKey(item: CommandItem<string>): string {
	return item.value ?? item.id;
}

/** ConversationJumpList is the jump shell's only list: the active session's
 * conversations in sidebar order. Every row is a leaf that activates the
 * conversation. */
const ConversationJumpList: CommandList<string> = {
	id: "conversations",
	placeholder: "Jump to conversation",
	emptyText: "No conversations yet",
	list: (context) =>
		orderedConversations(context.store.conversations[context.session]).map(
			convItem,
		),
	onSelect: (item, context) => {
		activateConv(
			context.store,
			context.view,
			context.dispatch,
			context.session,
			resultKey(item),
		);
	},
};

const ConversationJump: Mithril.Component = {
	oninit: (vnode) => {
		(vnode.state as unknown as ConversationJumpState).query = "";
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const actions = useActions();
		const state = vnode.state as unknown as ConversationJumpState;
		const session = view.activeSession;
		if (session === null) {
			// No live session means nothing to jump to. Like the other shells,
			// render empty rather than reaching for a redraw.
			return null;
		}
		const context: CommandContext = { store, view, dispatch, actions, session };
		return m(Palette, {
			list: ConversationJumpList,
			context,
			query: state.query,
			minInput: 0,
			onQuery: (query: string) => {
				state.query = query;
			},
			onSelect: () => closeModal(view),
			onClose: () => closeModal(view),
		});
	},
};

/** COMMANDS maps a CommandId to the shell component that drives that palette. */
export const COMMANDS: Record<CommandId, Mithril.Component> = {
	"conversation-jump": ConversationJump,
	"character-search": CharacterSearch,
	main: CommandShell,
};