// list.ts — the command shell's view of the palette's list interface. The
// generic `PaletteList`/`PaletteItem` live in the palette primitive (which
// never sees app state); here they are specialized to the command context the
// three shells hand in. A shell keeps a `current` CommandList and swaps it
// when a row carrying `next` is chosen, so one modal can drill into a further
// palette without the shell branching on which list is showing.

import type { AppActions, Dispatch } from "../../context.js";
import type { Conversation, Store, View } from "../../store/state.js";
import type { PaletteItem, PaletteList } from "../primitives/palette.js";

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

/** CommandList is a PaletteList bound to the command context. `R` is the value
 * type a row carries, when a list wants one. */
export type CommandList<R = unknown> = PaletteList<R, CommandContext>;

/** CommandItem is a PaletteItem bound to the command context. */
export type CommandItem<R = unknown> = PaletteItem<R, CommandContext>;