import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { closeModal } from "../../store/state.js";
import { useActions, useStore, useView } from "../../context.js";
import { request } from "../../render.js";
import { boundMatches } from "../../lib/list.js";
import { Dialog, DialogTabs } from "../primitives/dialog.js";
import { Button, TextField } from "../primitives/form.js";
import { FilterInput } from "../primitives/FilterInput.js";
import type { ChannelsPayload } from "../../transport/protocol.js";
// JoinChannelDialog: modal for joining an official channel or a private room
// from the core-provided catalog, or creating a new closed private room.
// Official channels and public rooms are the two catalog tabs; clicking a row
// joins it immediately. The third tab creates a room from a title. There is
// deliberately no join-by-name/ID field — every joinable conversation comes
// from the catalog.
//
// Performance: the catalog can be large (the ORS room list especially), so the
// lowercase search index is built once per catalog revision, the query is
// debounced, and only a bounded prefix of matches is rendered. The rendered
// list is memoized and returned verbatim while its inputs (tab, filter,
// catalog revision, clicked row) are unchanged, so unrelated global redraws
// touch no dialog nodes. Typing does not trigger a global redraw: the input is
// uncontrolled for the duration of the debounce and the automatic Mithril
// redraw is suppressed. Ordering by population is applied once in applyChannels.

/** MAX_VISIBLE caps rendered rows so a broad query cannot build thousands of
 * DOM nodes on a slow client. */
const MAX_VISIBLE = 100;

type JoinKind = "official" | "room" | "create";

export const JoinChannelDialog: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as JoinState;
		state.kind = "official";
		state.id = "";
		state.query = "";
		state.filter = "";
		state.title = "";
		state.error = null;
		state.busy = false;
		state.official = [];
		state.rooms = [];
		state.cache = null;
	},
	view: (vnode) => {
		const state = vnode.state as JoinState;
		const view = useView();
		const store = useStore();

		// Rebuild the lowercase search index only when the catalog changes.
		if (state.catalogRef !== store.channels) {
			state.catalogRef = store.channels;
			state.official = store.channels.official.map((c) => ({
				name: c.name,
				label: c.name,
				lower: c.name.toLowerCase(),
				characters: c.characters,
			}));
			state.rooms = store.channels.rooms.map((c) => {
				const label = c.title !== "" ? c.title : c.name;
				return {
					name: c.name,
					label,
					lower: label.toLowerCase(),
					characters: c.characters,
				};
			});
			state.cache = null;
		}

		/** panelFor builds the catalog panel for one tab. Only the active tab's
		 * render is invoked, so only one list is ever built. */
		const panelFor = (kind: JoinKind): Mithril.Children => {
			const entries = kind === "official" ? state.official : state.rooms;
			if (entries.length === 0) {
				return m("p.join-empty.muted", "Channel list isn't available yet.");
			}
			return m("div.join-catalog", [
				m(FilterInput, {
					// Remount on a tab switch so a pending debounce is cancelled and
					// a stale query cannot filter the newly shown catalog.
					key: state.kind,
					class: "join-catalog-search",
					placeholder: "Filter…",
					value: state.query,
					oninput: (value) => {
						state.query = value;
					},
					onfilter: (value) => {
						state.filter = value;
					},
				}),
				ensureList(state, entries, store.channels).list,
			]);
		};

		/** panelCreate builds the Create Room form. It is one-shot: a successful
		 * create closes the dialog, and the new ADH room appears in the sidebar
		 * when the server's self JCH lands. */
		const panelCreate = (): Mithril.Children =>
			m("div.join-create", [
				m(TextField, {
					label: "Room title",
					value: state.title,
					disabled: state.busy,
					oninput: (value: string) => {
						state.title = value;
					},
					onsubmit: () => createRoom(state),
				}),
				m(
					"p.field-note",
					"Creates a private, invite-only room. Its ADH-… id is assigned by the server.",
				),
				m(Button, {
					label: "Create room",
					busy: state.busy,
					disabled: state.title.trim() === "",
					onclick: () => createRoom(state),
				}),
			]);

		return m(
			Dialog,
			{
				title: "Join a conversation",
				onClose: () => {
					closeModal(view);
				},
			},
			[
				m(DialogTabs, {
					active: state.kind,
					fill: true,
					onSelect: (id) => selectKind(state, id as JoinKind),
					tabs: [
						{
							id: "official",
							label: "Official channels",
							render: () => panelFor("official"),
						},
						{
							id: "room",
							label: "Private rooms",
							render: () => panelFor("room"),
						},
						{
							id: "create",
							label: "Create Room",
							render: panelCreate,
						},
					],
				}),
				state.error !== null ? m("p.form-error", state.error) : null,
			],
		);
	},
};

/** selectKind changes the active tab and clears the query, selection, title,
 * and any pending debounce so the catalogs and the form never share state. */
function selectKind(state: JoinState, kind: JoinKind): void {
	if (state.kind === kind) {
		return;
	}
	state.kind = kind;
	state.id = "";
	state.query = "";
	state.filter = "";
	state.title = "";
	state.error = null;
	state.cache = null;
}

/** createRoom creates a closed private room from the Create Room tab. It
 * trims the title, reads the active session at click time, and closes the
 * dialog on a successful ack: the new room's ADH id arrives with the server's
 * self JCH, which the sidebar picks up with no pending-selection machinery. */
function createRoom(state: JoinState): void {
	if (state.busy) {
		return;
	}
	const title = state.title.trim();
	if (title === "") {
		state.error = "Enter a room title.";
		request();
		return;
	}
	const view = useView();
	const session = view.activeSession;
	if (session === null) {
		return;
	}
	const actions = useActions();
	state.busy = true;
	state.error = null;
	void actions.createRoom(session, title).then((err) => {
		state.busy = false;
		if (err !== null) {
			state.error = err;
			request();
			return;
		}
		state.title = "";
		closeModal(view);
		request();
	});
}

/** join sends the join command for a catalog row. The dialog stays open so
 * several conversations can be joined in one pass. It reads the active session
 * at click time so cached rows never capture a stale session. */
function join(state: JoinState, name: string): void {
	if (state.busy) {
		return;
	}
	// Catalog rows only exist on the official/room tabs; the create tab has its
	// own submit path.
	if (state.kind === "create") {
		return;
	}
	const view = useView();
	const session = view.activeSession;
	if (session === null) {
		return;
	}
	const kind = state.kind;
	const actions = useActions();
	state.busy = true;
	state.error = null;
	state.id = name;
	void actions.joinChannel(session, kind, name).then((err) => {
		state.busy = false;
		if (err !== null) {
			state.error = err;
			request();
			return;
		}
		// Do not open the conversation here: it does not exist until the
		// server's JCH creates it (see SessionView's auto-select).
		request();
	});
}

/** ensureList returns the memoized list, rebuilding it only when the tab,
 * filter, catalog revision, or selected row changed. */
function ensureList(
	state: JoinState,
	entries: CatalogEntry[],
	catalog: ChannelsPayload,
): ListCache {
	const cache = state.cache;
	if (
		cache !== null &&
		cache.kind === state.kind &&
		cache.filter === state.filter &&
		cache.catalog === catalog &&
		cache.selected === state.id
	) {
		return cache;
	}
	const next = buildList(state, entries, catalog);
	state.cache = next;
	return next;
}

/** buildList filters the active catalog and builds the bounded row list. */
function buildList(
	state: JoinState,
	entries: CatalogEntry[],
	catalog: ChannelsPayload,
): ListCache {
	const q = state.filter.trim().toLowerCase();
	const matches = q === "" ? entries : entries.filter((e) => e.lower.includes(q));
	const { visible, hidden } = boundMatches(matches, MAX_VISIBLE);
	const rows: Mithril.Vnode[] = visible.map((e) =>
		m(
			"li.join-row",
			{
				key: e.name,
				class: state.id === e.name ? "is-selected" : "",
				onclick: () => join(state, e.name),
			},
			[
				m("span.join-name", e.label),
				m("span.join-count.muted", String(e.characters)),
			],
		),
	);
	if (hidden > 0) {
		// The list is keyed, so this row must be keyed too; "#more" cannot
		// collide with an F-Chat channel name.
		rows.push(
			m(
				"li.join-more.muted",
				{ key: "#more" },
				`${hidden} more — keep typing to narrow`,
			),
		);
	}
	return {
		kind: state.kind,
		filter: state.filter,
		catalog,
		selected: state.id,
		// Keyed so the panel fragment (this list plus the keyed FilterInput) is
		// uniformly keyed; Mithril rejects fragments that mix keyed and unkeyed
		// vnodes. "list" is distinct from the FilterInput's kind key.
		list: m("ul.join-list", { key: "list" }, rows),
	};
}

interface CatalogEntry {
	name: string;
	label: string;
	lower: string;
	characters: number;
}

/** ListCache is the memoized rendered list plus the inputs it was built from. */
interface ListCache {
	kind: JoinState["kind"];
	filter: string;
	catalog: ChannelsPayload;
	selected: string;
	list: Mithril.Vnode;
}

interface JoinState {
	kind: JoinKind;
	/** id is the catalog row that was clicked (highlight / retry context). */
	id: string;
	/** query is the live input value; filter is the debounced value. */
	query: string;
	filter: string;
	/** title is the Create Room form's live input value. */
	title: string;
	error: string | null;
	busy: boolean;
	/** catalogRef is the store.channels identity the index was built from. */
	catalogRef?: ChannelsPayload;
	official: CatalogEntry[];
	rooms: CatalogEntry[];
	/** cache is the memoized list, rebuilt only when its inputs change. */
	cache: ListCache | null;
}