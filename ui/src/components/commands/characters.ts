// characters.ts — the character picker shell (Ctrl/Cmd-K). An invisible
// container that owns one palette's data and behavior and renders only the
// shared Palette primitive.
//
// It has two root lists. Opened from a channel/room it shows that
// conversation's actual member roster; opened anywhere else it shows the
// "seen" characters (every character the client holds a live online presence
// record for). While a channel/room is active the two are switchable: a fake
// previous item at the top states the current list and gates a second Ctrl-K
// that swaps to the other. The fake item's presence is the switch's
// availability, so a DM/warp/none open can only ever show the seen list. The
// seen list also heads with the session's recently closed DM partners, which a
// DM-only character (no shared channel, no presence record) would otherwise be
// unreachable from.
//
// A character row drills into a small action list (Open DM / Open Profile),
// identical in both modes. The wrapper div catches the toggle chord before it
// can reach the global shortcut listener and before the browser can steal
// focus; it always claims the chord and only toggles when the fake item marks
// the switch available.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import {
	useDispatch,
	useStore,
	useView,
	type Dispatch,
} from "../../context.js";
import { seenOnlineNames } from "../../lib/characters.js";
import { rosterRank, sortRosterNames } from "../../lib/order.js";
import { request } from "../../render.js";
import { activateConv } from "../../store/commands.js";
import {
	closeModal,
	type Character,
	type Conversation,
	type Store,
	type View,
} from "../../store/state.js";
import {
	RosterCharacter,
	type RosterCharacterState,
} from "../presence/character.js";
import { Palette, type PaletteItem } from "../primitives/palette.js";
import { openProfile } from "./commands.js";

const NO_MEMBERS: string[] = [];
const NO_OPS: string[] = [];

/** CharacterMode names which root list is showing. */
type CharacterMode = "roster" | "seen";

/** CachedItem is one rendered root row plus the inputs it was built from. It is
 * reused while the presence record and moderator mark are unchanged, so a
 * redraw (a keystroke, an unrelated presence change) does not rebuild every
 * row's vnode. */
interface CachedItem {
	character: RosterCharacterState;
	moderator: Moderator;
	item: PaletteItem<string>;
}

type Moderator = "room" | "global" | undefined;

interface CharacterSearchState {
	query: string;
	/** selected is the character whose action list is showing; undefined at the
	 * root (roster or seen) list. */
	selected?: string;
	/** mode is the root list the user last chose; ignored outside a channel. */
	mode: CharacterMode;
	/** rosterNames is the ranked member list, computed once on first use. */
	rosterNames?: string[];
	/** rosterOpsSet is the op set that ranked rosterNames. */
	rosterOpsSet: ReadonlySet<string>;
	/** seenNames is the alphabetical online-character list, computed once. */
	seenNames?: string[];
	/** items caches a root row per name so unchanged presence reuses the vnode. */
	items: Map<string, CachedItem>;
	/** placeholders keeps a stable record for a member without presence yet. */
	placeholders: Map<string, RosterCharacterState>;
}

/** isMemberConv reports whether a conversation kind has a live member roster. */
function isMemberConv(conv: Conversation | undefined): boolean {
	const kind = conv?.conv.kind;
	return kind === "official" || kind === "room";
}

/** moderatorFor resolves the row's moderator mark; only the roster list passes
 * an op set, so the seen list never shows a room badge. */
function moderatorFor(
	character: Character | undefined,
	ops: ReadonlySet<string>,
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

/** placeholder returns a stable record for a name the presence registry has not
 * reached yet, so RosterCharacter's reference check still holds. */
function placeholder(
	state: CharacterSearchState,
	name: string,
): RosterCharacterState {
	let record = state.placeholders.get(name);
	if (record === undefined) {
		record = { name, online: false };
		state.placeholders.set(name, record);
	}
	return record;
}

/** selfNames collects every logged-in character name, so the seen list never
 * offers the user their own alt. */
function selfNames(store: Store): Set<string> {
	const names = new Set<string>();
	for (const session of Object.keys(store.sessions)) {
		const snapshot = store.sessions[session];
		if (snapshot !== undefined) {
			names.add(snapshot.character);
		}
	}
	return names;
}

/** buildItems maps the active name list to root rows, reusing a cached row when
 * its presence record and moderator mark are unchanged. */
function buildItems(
	state: CharacterSearchState,
	names: readonly string[],
	mode: CharacterMode,
	ops: ReadonlySet<string>,
	store: Store,
): PaletteItem<string>[] {
	const items: PaletteItem<string>[] = [];
	for (const name of names) {
		const record = store.characters[name];
		const character = record ?? placeholder(state, name);
		const moderator =
			mode === "roster" ? moderatorFor(record, ops, name) : undefined;
		const cached = state.items.get(name);
		if (
			cached !== undefined &&
			cached.character === character &&
			cached.moderator === moderator
		) {
			items.push(cached.item);
			continue;
		}
		const item: PaletteItem<string> = {
			id: name,
			title: m(RosterCharacter, { character, moderator }),
			filterable: name,
			subcommand: true,
			value: name,
		};
		state.items.set(name, { character, moderator, item });
		items.push(item);
	}
	return items;
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

/** recentItems builds the "Recently closed DM" rows that head the seen list.
 * A row reuses the live presence record when the registry has one, so a partner
 * who is online still shows that; a DM-only partner falls back to a placeholder
 * (offline). No moderator mark: the seen list never shows one. */
function recentItems(
	state: CharacterSearchState,
	names: readonly string[],
	store: Store,
): PaletteItem<string>[] {
	const items: PaletteItem<string>[] = [];
	for (const name of names) {
		const character = store.characters[name] ?? placeholder(state, name);
		items.push({
			id: name,
			title: m(RosterCharacter, { character }),
			description: "Recently closed DM",
			filterable: name,
			subcommand: true,
			value: name,
		});
	}
	return items;
}

/** toggleHint builds the fake previous item that states the current list and
 * advertises the Ctrl-K switch. Its presence is the switch's availability. */
function toggleHint(mode: CharacterMode): PaletteItem<string> {
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

/** actionItems is the per-character action list, identical in both modes. */
function actionItems(name: string): PaletteItem<string>[] {
	return [
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
}

/** runAction performs a chosen action row and closes the picker. */
function runAction(
	action: string,
	name: string,
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
): void {
	if (action === "open-dm") {
		activateConv(store, view, dispatch, session, `dm:${name}`);
	} else if (action === "open-profile") {
		openProfile(name);
	}
	closeModal(view);
}

export const CharacterSearch: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as CharacterSearchState;
		state.query = "";
		state.selected = undefined;
		state.mode = "seen";
		state.rosterNames = undefined;
		state.rosterOpsSet = new Set();
		state.seenNames = undefined;
		state.items = new Map();
		state.placeholders = new Map();
		// Open on the channel roster when the active conversation has one.
		const store = useStore();
		const view = useView();
		const session = view.activeSession;
		const key = session === null ? undefined : view.activeConv[session];
		const conv =
			session === null || key === undefined
				? undefined
				: store.conversations[session]?.[key];
		if (isMemberConv(conv)) {
			state.mode = "roster";
		}
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const state = vnode.state as CharacterSearchState;
		const session = view.activeSession;
		if (session === null) {
			return null;
		}
		const key = view.activeConv[session];
		const conv =
			key === undefined ? undefined : store.conversations[session]?.[key];
		const hasRoster = isMemberConv(conv);
		// Only a channel/room can show the roster; everywhere else is seen.
		const mode: CharacterMode = hasRoster ? state.mode : "seen";

		// Each root list is built once on first use, then frozen for the life of
		// this ephemeral picker, so rows never reorder or pop in under the cursor.
		// Live presence still reaches an individual row through buildItems' cache.
		if (mode === "roster" && state.rosterNames === undefined && conv !== undefined) {
			const members = conv.members ?? NO_MEMBERS;
			const ops = conv.ops ?? NO_OPS;
			state.rosterOpsSet = new Set(ops);
			const friendSet = new Set(store.friends.map((f) => f.name));
			state.rosterNames = sortRosterNames(members, (name) =>
				rosterRank({
					isAdmin: store.characters[name]?.admin === true,
					isOp: state.rosterOpsSet.has(name),
					isFriend: friendSet.has(name),
				}),
			);
		}
		if (mode === "seen" && state.seenNames === undefined) {
			state.seenNames = seenOnlineNames(store.characters, selfNames(store));
		}

		// Recently closed DM partners head the seen list. They are also kept out
		// of the alphabetical body so the same character never appears twice; a
		// partner with a presence record still gets it on their recent row.
		const recent = mode === "seen" ? recentNames(view, session) : NO_MEMBERS;
		const rootNames =
			(mode === "roster" ? state.rosterNames : state.seenNames) ?? NO_MEMBERS;
		const bodyNames =
			recent.length === 0
				? rootNames
				: rootNames.filter((name) => !recent.includes(name));

		let items: PaletteItem<string>[];
		let previousItem: PaletteItem<string> | undefined;
		// toggleItem is non-undefined only at a root list in a channel/room.
		let toggleItem: PaletteItem<string> | undefined;
		if (state.selected !== undefined) {
			items = actionItems(state.selected);
			previousItem = {
				id: `selected:${state.selected}`,
				title: state.selected,
				description: "Choose an action.",
				filterable: "",
			};
		} else {
			items = buildItems(state, bodyNames, mode, state.rosterOpsSet, store);
			if (recent.length > 0) {
				items = [...recentItems(state, recent, store), ...items];
			}
			toggleItem = hasRoster ? toggleHint(mode) : undefined;
			previousItem = toggleItem;
		}

		const listEmpty =
			mode === "roster"
				? rootNames.length === 0
				: rootNames.length === 0 && recent.length === 0;
		const emptyText = listEmpty
			? mode === "roster"
				? "This channel's members aren't loaded yet."
				: "No characters are online."
			: "No matches";

		const onToggleKey = (e: KeyboardEvent): void => {
			const mod = e.ctrlKey || e.metaKey;
			if (!mod || e.altKey || e.shiftKey || (e.key !== "k" && e.key !== "K")) {
				return;
			}
			// Always claim the chord: otherwise the browser may steal focus out of
			// the palette. Toggle only when the fake item marks it available.
			e.preventDefault();
			e.stopPropagation();
			if (toggleItem !== undefined) {
				state.mode = state.mode === "roster" ? "seen" : "roster";
				request();
			}
		};

		return m("div", { onkeydown: onToggleKey }, [
			m(Palette, {
				// Remount when entering or leaving the action list so the input and
				// highlight reset, but not when toggling lists, so the filter stays.
				key: state.selected ?? "root",
				items,
				query: state.query,
				minInput: 0,
				placeholder:
					state.selected !== undefined
						? "Choose an action"
						: mode === "roster"
							? "Search members"
							: "Search characters",
				emptyText,
				previousItem,
				onQuery: (query: string) => {
					state.query = query;
				},
				onSelect: (item: PaletteItem<string>) => {
					if (state.selected !== undefined) {
						runAction(
							item.id,
							state.selected,
							store,
							view,
							dispatch,
							session,
						);
						return;
					}
					state.selected = item.value ?? item.id;
					state.query = "";
					request();
				},
				onClose: () => closeModal(view),
			}),
		]);
	},
};
