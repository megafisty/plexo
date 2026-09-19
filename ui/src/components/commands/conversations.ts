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
import { useDispatch, useStore, useView } from "../../context.js";
import { convTitle, orderedConversations } from "../../lib/order.js";
import { activateConv } from "../../store/commands.js";
import { closeModal, type CommandId, type Conversation } from "../../store/state.js";
import { Palette, type PaletteItem } from "../primitives/palette.js";

// ==========================================================================
// conversation jump
// ==========================================================================
// The first shell: jump to a conversation in the active session by name. It
// reuses the sidebar's ordering and the normal activateConv path, so the jump
// behaves exactly like clicking the row.

interface ConversationJumpState {
	query: string;
}

/** filterConversations returns the conversations whose title or id contains the
 * query, case-insensitively; an empty query keeps the whole list. Pure, so the
 * match rule is unit-testable apart from the palette. */
export function filterConversations(
	list: ReadonlyArray<Conversation>,
	query: string,
): Conversation[] {
	const q = query.trim().toLowerCase();
	if (q === "") {
		return list.slice();
	}
	return list.filter(
		(conv) =>
			convTitle(conv).toLowerCase().includes(q)
	);
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

/** convItem maps one conversation to a palette row. The precomputed `value`
 * hands the selection a plain conversation key. */
function convItem(conv: Conversation): PaletteItem<string> {
	return {
		id: conv.key,
		title: convTitle(conv),
		description: convKindLabel(conv),
		value: conv.key,
	};
}

/** resultKey resolves the conversation key from a selected row: the
 * precomputed value, or the item's id when no value was set. */
function resultKey(item: PaletteItem<string>): string {
	return item.value ?? item.id;
}

const ConversationJump: Mithril.Component = {
	oninit: (vnode) => {
		(vnode.state as unknown as ConversationJumpState).query = "";
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const state = vnode.state as unknown as ConversationJumpState;
		const session = view.activeSession;
		const list = orderedConversations(
			session === null ? undefined : store.conversations[session],
		);
		return m(Palette, {
			items: filterConversations(list, state.query).map(convItem),
			query: state.query,
			minInput: 0,
			placeholder: "Jump to conversation",
			emptyText: "No conversations match",
			onQuery: (query: string) => {
				state.query = query;
			},
			onSelect: (item: PaletteItem<string>) => {
				if (session !== null) {
					activateConv(store, view, dispatch, session, resultKey(item));
				}
				closeModal(view);
			},
			onClose: () => closeModal(view),
		});
	},
};

/** COMMANDS maps a CommandId to the shell component that drives that palette. */
export const COMMANDS: Record<CommandId, Mithril.Component> = {
	"conversation-jump": ConversationJump,
};
