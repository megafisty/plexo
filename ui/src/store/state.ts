// state.ts — the canonical client state: the core-mirrored Store and the
// device-local View, their shapes, and their constructors. Only apply.ts and
// commands.ts mutate them. Absorbs store.ts and view.ts.

import type { AccountState, AdsStatus, ChannelsPayload, ConvRef, MemberInfo, PresencePayload, RoomRole, SessionSnapshot, Warpmark } from "../transport/protocol.js";
import { convKey } from "../transport/protocol.js";
import { devicePrefs, saveDevicePrefs } from "./persist.js";

/** INVITES_KEY is the client-only sentinel key for a session's pending room
 * invitations. It is not a core conversation: nothing is joined, streamed, or
 * persisted for it, and its content is derived from the session's
 * SessionSnapshot.invites list. The section is hidden while a session has no
 * pending invitations or the user has closed it. */
export const INVITES_KEY = "invites";


// ==========================================================================
// store.ts
// ==========================================================================
// The canonical client state. Only apply.ts and commands.ts (the pending-send
// path) mutate it; components only read.

export interface CoreState {
	connection: ConnectionState;
	authRequired: boolean;
	authenticated: boolean;
}

export type ConnectionState = "connecting" | "open" | "closed";

/** Character is a partial presence record; unknown fields resolve to
 * placeholders, never to errors. */
export interface Character {
	name: string;
	gender?: string;
	status?: string;
	/** statusMsg is rendered HTML from the core, never raw BBCode. */
	statusMsg?: string;
	/** admin marks a global F-Chat moderator (from the server's ADL). It is
	 * authoritative on the wire (always present, resolved by the core). */
	admin: boolean;
	online: boolean;
	presenceKnown: boolean;
}

/** TypingState is one character's live typing signal. `at` is the receipt
 * time of the last "on" event; `paused` means text is waiting to be sent but
 * the typist is not currently pressing keys. */
export interface TypingState {
	at: number;
	paused: boolean;
}

export interface Conversation {
	key: string;
	conv: ConvRef;
	session: string;
	title?: string;
	/** description is rendered HTML from the core, never raw BBCode. */
	description?: string;
	mode?: string;
	/** role is the reporting session's room-scoped authority, set-to by the core
	 * for channels/rooms and absent for DMs/broadcasts. It drives whether the
	 * header offers management affordances; global-moderator status is separate
	 * (presence admin). */
	role?: RoomRole;
	/** unread is client-owned: set when a message arrives while the conversation
	 * is not the focused pane in a focused tab. Never sourced from the core. */
	unread: boolean;
	/** highlight is a real core signal: an incoming channel message matched a
	 * configured highlight string. Elevated alongside unread DMs. */
	highlight: boolean;
	lastActivity: number; // epoch ms
	members?: string[];
	ops?: string[];
	/** typing maps character -> last "on" signal; stale entries are filtered
	 * at render time, never swept. `paused` distinguishes text waiting to be
	 * sent from an active typist. */
	typing: Record<string, TypingState>;
	/** materialized is true once a conv_view has arrived for this conv. */
	materialized: boolean;
	/** interestAsked is true once full interest has been dispatched for this
	 * conversation on the current subscription and its window has not yet
	 * materialized. The session view checks it before re-requesting, so a
	 * conversation whose conv_view is in flight is not re-asked on every redraw. */
	interestAsked?: boolean;
	/** readOnly marks a virtual conversation (a warp pane) that renders history
	 * but never sends, joins, or streams. Chrome and commands check this once
	 * rather than branching on the kind. */
	readOnly?: boolean;
}

/** Entry is one timeline item. It is immutable: any change produces a new
 * record (views memoize on the reference) and bumps the owning window's `rev`.
 * Never assign to a field in place -- an in-place write is invisible to every
 * reference-equality check downstream. */
export interface Entry {
	readonly id: string;
	readonly convSeq: number;
	readonly kind: string;
	readonly speaker: string;
	readonly html: string;
	readonly time: number; // epoch ms
	readonly self?: boolean;
	readonly send?: "pending" | "sent" | "failed";
	readonly error?: string;
}

/** EntryWindow holds one conversation's bounded timeline. undefined in
 * entries[session][key] means "not loaded", not "empty".
 *
 * The window is a revisioned container: `rev` must bump on every change to its
 * contents (append, trim, or an entry replacement). Views memoize against `rev`
 * and against Entry references, so a mutation that forgets to bump `rev` is a
 * stale-UI bug. Use the helpers in window.ts rather than touching items. */
export interface EntryWindow {
	items: Entry[];
	oldestSeq?: number;
	newestSeq?: number;
	hasOlder: boolean;
	/** hasNewer is true when the retained slice is behind the channel's live
	 * edge: bodies were dropped while the view was scrolled up. */
	hasNewer: boolean;
	/** liveSeq is the highest conv_seq seen from live events, retained or not.
	 * loadNewer resumes from newestSeq and this bounds hasNewer. */
	liveSeq?: number;
	/** rev bumps on every in-place mutation so views can memoize against it. */
	rev: number;
}

export interface PendingSend {
	session: string;
	conv: ConvRef;
	entryId: string;
}

export interface Store {
	core: CoreState;
	account: AccountState;
	sessions: Record<string, SessionSnapshot>;
	/** friends and bookmarks are account-wide sets, delivered once (the snapshot
	 * carries them at the root, and later changes stream as an account/friends
	 * record) and shared by every session. The core keeps them split; views that
	 * want the deduplicated union use unionFriends() from lib/friends. */
	friends: MemberInfo[];
	bookmarks: MemberInfo[];
	ignores: string[];
	characters: Record<string, Character>;
	/** charactersRev bumps when any presence record changes. Views that render a
	 * derived character list (the Ctrl/Cmd-K picker) memoize against it, so an
	 * unrelated redraw reuses the built rows while a presence change rebuilds. */
	charactersRev: number;
	conversations: Record<string, Record<string, Conversation>>;
	entries: Record<string, Record<string, EntryWindow | undefined>>;
	/** pending maps command cid -> optimistic entry, so an ack can update it
	 * in place (never remove/re-add; Mithril would rebuild the DOM node). */
	pending: Record<string, PendingSend>;
	/** channels is the core-wide official channel and public room catalog;
	 * empty lists until the first CHA/ORS round trip completes. */
	channels: ChannelsPayload;
	/** search is the latest FKS result set per session, keyed by session. Rows
	 * are self-contained (name + presence, enriched by the core) and are never
	 * merged into `characters`. It is pulled from the core's session cache on
	 * `search` notices, dialog open, or the recall button, so a client that did
	 * not witness the search still sees the latest set. */
	search: Record<string, MemberInfo[]>;
	/** searchRevision is the newest FKS revision per session. A fetch older than
	 * this is discarded, so a slow response cannot regress a newer result set. */
	searchRevision: Record<string, number>;
	/** ads is the latest advertisement scheduler status per session, streamed
	 * under the `ads/<character>` state key. The Ads Post dialog derives each
	 * channel's next-eligible time and skipped reason from it; the campaign
	 * definition itself is HTTP-only and never stored here. It is set-to and
	 * clears when the session is lost. */
	ads: Record<string, AdsStatus>;
	/** warpmarks maps session -> that character's marks, newest first. Marks are
	 * HTTP-only (no live event); the list is refetched when the popout mounts or
	 * a mutation lands. */
	warpmarks: Record<string, Warpmark[]>;
	/** warpmarksRev bumps when a session's mark list changes. It is kept separate
	 * from conversationsRev and unreadRev so a future memo can key on it without
	 * coupling the list to sidebar/unread invalidation. */
	warpmarksRev: number;
	/** conversationsRev bumps when the conversation set or a title changes;
	 * views memoize built vnodes against it. */
	conversationsRev: number;
	/** unreadRev bumps when any conversation's unread/highlight flag changes. */
	unreadRev: number;
}

/** WINDOW caps a conversation's retained timeline: one contiguous slice around
 * the viewport. Larger wastes netbook memory; smaller pages more often. */
export const WINDOW = 120;

export function createStore(): Store {
	return {
		core: { connection: "connecting", authRequired: false, authenticated: false },
		account: { status: "missing" },
		sessions: {},
		friends: [],
		bookmarks: [],
		ignores: [],
		characters: {},
		charactersRev: 0,
		conversations: {},
		entries: {},
		pending: {},
		channels: { official: [], rooms: [] },
		search: {},
		searchRevision: {},
		ads: {},
		warpmarks: {},
		warpmarksRev: 0,
		conversationsRev: 0,
		unreadRev: 0,
	};
}

export function applyPresence(
	store: Store,
	p: PresencePayload,
	known: boolean,
): void {
	// Presence arrives repeatedly (a resync, a friend presence refresh). When
	// nothing changed, keep the existing record so reference-equality views
	// (roster rows) do not rebuild for an identical payload.
	const existing = store.characters[p.character];
	const admin = p.admin === true;
	if (
		existing !== undefined &&
		existing.gender === p.gender &&
		existing.status === p.status &&
		existing.statusMsg === p.statusMsg &&
		existing.admin === admin &&
		existing.online === p.online &&
		existing.presenceKnown === known
	) {
		return;
	}
	store.characters[p.character] = {
		name: p.character,
		gender: p.gender,
		status: p.status,
		statusMsg: p.statusMsg,
		// admin is authoritative on the wire (always present, resolved by the
		// core), so a removed moderator clears instead of staying crowned.
		admin,
		online: p.online,
		presenceKnown: known,
	};
	store.charactersRev++;
}

// ==========================================================================
// view.ts
// ==========================================================================
// Device-local view state. Never synced to the core and never mutated by a
// presentational component.

export type Phase = "boot" | "core-auth" | "credentials" | "chatspace";

export interface Toast {
	id: number;
	message: string;
}

/** MAX_TABS caps the total number of session tabs, connected or unconnected. */
export const MAX_TABS = 5;

/** CharacterMenuState anchors the roster context menu to a cursor position. */
export interface CharacterMenuState {
	session: string;
	name: string;
	x: number;
	y: number;
	/** character is a presence snapshot captured when the menu was opened.
	 * Search results pass their core-enriched row; the menu uses it when the
	 * live registry has no record, so transient results never enter
	 * store.characters. */
	character?: MemberInfo;
}

/** WarpmarkDialogState is the payload of the open mark label prompt for one
 * entry. existing is true when the entry is already marked, so the dialog
 * offers delete. */
export interface WarpmarkDialogState {
	session: string;
	entryId: string;
	speaker: string;
	existing: boolean;
	label: string;
}

/** CommandId names one command palette shell (components/commands). The palette
 * modal itself carries no payload; the named shell owns its data and action. */
export type CommandId =
	| "conversation-jump"
	| "main"
	| "character-search"
	| "format-marks"
	| "format-advanced";

/** FormatApply wraps the active composer's selection in a BBCode tag. The
 * composer creates it when it opens its format palette and it rides on the
 * command modal, so the palette can apply a chosen tag without holding the
 * textarea itself. `value` fills a parameterized tag's parameter; `content`
 * replaces the selected text as the tag body. */
export type FormatApply = (
	tag: string,
	param: boolean,
	value?: string,
	content?: string,
) => void;

/** CommandPalette is the single command-palette overlay, separate from the
 * modal slot so a composer format palette can layer above a dialog. `onFormat`
 * carries the composer's apply closure for the format shells; `selection` is
 * the text it had selected when the chord fired; `start` is the advanced
 * sub-list to open on. Other shells ignore all three. */
export interface CommandPalette {
	command: CommandId;
	onFormat?: FormatApply;
	selection?: string;
	start?: string;
}

/** Modal is the single modal dialog on screen. The top-bar dialogs carry no
 * payload; the warpmark prompt carries the entry it edits. A modal owns the
 * whole screen (backdrop), so there is never more than one. */
export type Modal =
	| { kind: "join" }
	| { kind: "status" }
	| { kind: "search" }
	| { kind: "ads" }
	| { kind: "logs" }
	| ({ kind: "warpmark" } & WarpmarkDialogState)
	| { kind: "roomAdmin"; session: string; conv: ConvRef };

/** ModalKind names the payload-free top-bar dialogs, the ones `toggleModal`
 * can open or close. The command palette is opened by name, not toggled. */
export type ModalKind = Exclude<Modal["kind"], "warpmark" | "roomAdmin">;

/** Popout is the single top-bar popout on screen (friends/bookmarks or
 * warpmarks). Both are button + overlay + popover triples, so only one can be
 * showing. */
export type Popout = "friends" | "warpmarks";

/** Tab is a client-owned session tab. Its id is stable for the tab's lifetime;
 * session is the bound character name once login succeeds, or null while the
 * tab still shows the character picker. */
export interface Tab {
	id: string;
	session: string | null;
}

export interface View {
	phase: Phase;
	/** focused is true when the browser tab itself has focus. Unread is only
	 * cleared while focused, so a blurred tab never silently marks read. */
	focused: boolean;
	/** soundEnabled plays the attention sound for elevated messages. A device
	 * preference (This Device), seeded from localStorage; default on. */
	soundEnabled: boolean;
	/** tabs lists every open session tab, connected or not, left to right. */
	tabs: Tab[];
	/** activeTab is the focused tab id, or null when no tab exists. */
	activeTab: string | null;
	/** activeSession is the active tab's bound character, or null while that
	 * tab is unconnected. Derived from tabs; never assigned. */
	readonly activeSession: string | null;
	tabCounter: number;
	/** activeConv maps session -> composite conv key. */
	activeConv: Record<string, string>;
	/** invitesClosed marks a session whose virtual invites conversation the user
	 * closed while invitations were still pending. A newly arrived invitation,
	 * or an empty list, clears it; it is never persisted. */
	invitesClosed: Record<string, boolean>;
	/** windowLru maps session -> retained conversation keys, most recently used
	 * last. A released conversation keeps its timeline so re-entry can request a
	 * core-side delta instead of a full re-materialization; the cap bounds how
	 * many windows each session may park. */
	windowLru: Record<string, string[]>;
	/** recentDms maps session -> partners of recently closed DMs, oldest first.
	 * The core retains the conversation and its history; this only lets the
	 * character picker offer the partner again after the client drops the row
	 * (see closeDm). Capped by RECENT_DM_CAP and never persisted. */
	recentDms: Record<string, string[]>;
	/** pendingConv maps session -> composite conv key requested by a [session]
	 * link but not yet confirmed. The session view opens it once the core's JCH
	 * has created the conversation; a rejected join is cleared by the session's
	 * error event, and logout drops it. */
	pendingConv: Record<string, string>;
	/** msgPinned tracks whether each timeline is scrolled to the bottom, keyed
	 * "session/convKey". apply.ts reads it to decide whether a live entry is
	 * retained or trimmed; absent means pinned. */
	msgPinned: Record<string, boolean>;
	/** drafts are keyed "session/convKey". */
	drafts: Record<string, string>;
	/** modal is the single modal dialog on screen, or null. All the session and
	 * top-bar dialogs share this one slot, so opening one replaces whatever was
	 * there instead of needing a hand-kept close list at each call site. */
	modal: Modal | null;
	/** palette is the single command-palette overlay, or null. It renders above
	 * the modal slot: a composer format palette may mount while a dialog is
	 * active, so its apply closure keeps a live textarea. The global palettes are
	 * gated behind `dialogOpen`. */
	palette: CommandPalette | null;
	/** searchSelection maps session -> FKS field -> selected ids, so the search
	 * dialog's query builder survives close/reopen. Results live in the store,
	 * pulled from the core's session cache on `search` notices and on open. */
	searchSelection: Record<string, Record<string, Array<string | number>>>;
	/** popout is the single top-bar popout on screen, or null. */
	popout: Popout | null;
	/** settingsOpen swaps the chat workspace for the configuration editor. */
	settingsOpen: boolean;
	/** composerEnterNewline is a device preference: when true, Enter inserts a
	 * newline and Ctrl/Cmd+Enter sends, for long-form posts. */
	composerEnterNewline: boolean;
	/** limitMessageWidth is a device preference: when true, the timeline renders
	 * in a bounded, centered column for readability on wide screens. */
	limitMessageWidth: boolean;
	/** characterMenu is the open roster context menu, or null. */
	characterMenu: CharacterMenuState | null;
	toasts: Toast[];
	coreAuthError: string | null;
	credentialsError: string | null;
	submitting: boolean;
}

let toastCounter = 0;

export function createView(): View {
	const view: View = {
		phase: "boot",
		focused: false,
		soundEnabled: devicePrefs().soundEnabled,
		tabs: [],
		activeTab: null,
		get activeSession(): string | null {
			const tab = view.tabs.find((t) => t.id === view.activeTab);
			return tab?.session ?? null;
		},
		tabCounter: 0,
		activeConv: {},
		invitesClosed: {},
		windowLru: {},
		recentDms: {},
		pendingConv: {},
		msgPinned: {},
		drafts: {},
		modal: null,
		palette: null,
		searchSelection: {},
		popout: null,
		settingsOpen: false,
		composerEnterNewline: devicePrefs().composerEnterNewline,
		limitMessageWidth: devicePrefs().limitMessageWidth,
		characterMenu: null,
		toasts: [],
		coreAuthError: null,
		credentialsError: null,
		submitting: false,
	};
	return view;
}

/** addTab opens a new unconnected tab showing the picker, if the tab cap
 * allows. Returns the new tab id, or null when the cap is reached. */
export function addTab(view: View): string | null {
	if (view.tabs.length >= MAX_TABS) {
		return null;
	}
	view.tabCounter += 1;
	const id = `tab-${view.tabCounter}`;
	view.tabs = [...view.tabs, { id, session: null }];
	view.activeTab = id;
	return id;
}

/** bindTab attaches a character to a tab once its login is accepted. */
export function bindTab(view: View, id: string, session: string): void {
	const tab = view.tabs.find((t) => t.id === id);
	if (tab !== undefined) {
		tab.session = session;
	}
}

/** hydrateTab appends a tab bound to a core session, bypassing the tab cap.
 * Used only for the initial snapshot: the core's sessions exist regardless of
 * the UI's own add-tab cap. */
export function hydrateTab(view: View, session: string): void {
	view.tabCounter += 1;
	view.tabs = [...view.tabs, { id: `tab-${view.tabCounter}`, session }];
}

/** removeTab closes one tab and moves focus to the last remaining tab. */
export function removeTab(view: View, id: string): void {
	view.tabs = view.tabs.filter((t) => t.id !== id);
	if (view.activeTab === id) {
		view.activeTab = view.tabs[view.tabs.length - 1]?.id ?? null;
	}
}

/** reconcileTabs unbinds any tab whose bound session is no longer in the
 * store, so a session_lost or a reconnect snapshot leaves the picker in that
 * tab rather than a permanently "connecting" skeleton. The session's active
 * and pending conversation selections are dropped; drafts are kept so a
 * re-login does not lose a post. */
export function reconcileTabs(view: View, store: Store): void {
	for (const tab of view.tabs) {
		const session = tab.session;
		if (session === null || store.sessions[session] !== undefined) {
			continue;
		}
		tab.session = null;
		delete view.activeConv[session];
		delete view.pendingConv[session];
	}
}

/** sessionAlive reports whether a session is still present. Async loaders check
 * it after every await before writing into the store: a session can be closed
 * (logged out) while an HTTP read is in flight, and a late write would
 * resurrect entries, marks, or search results for a session that is gone. */
export function sessionAlive(store: Store, session: string): boolean {
	return store.sessions[session] !== undefined;
}

/** cycleTab moves focus one session tab left (delta -1) or right (+1),
 * wrapping. Like a tab click it leaves the Config view, so switching lands on
 * the workspace. It does not redraw; the caller requests. */
export function cycleTab(view: View, delta: number): void {
	if (view.tabs.length === 0) {
		return;
	}
	const index = view.tabs.findIndex((t) => t.id === view.activeTab);
	const next =
		index === -1
			? delta > 0
				? 0
				: view.tabs.length - 1
			: (index + delta + view.tabs.length) % view.tabs.length;
	view.activeTab = view.tabs[next]?.id ?? view.activeTab;
	view.settingsOpen = false;
}

export function pushToast(view: View, message: string): void {
	toastCounter += 1;
	view.toasts = [...view.toasts, { id: toastCounter, message }];
}

export function dismissToast(view: View, id: number): void {
	view.toasts = view.toasts.filter((t) => t.id !== id);
}

export function convScopeKey(session: string, key: string): string {
	return `${session}/${key}`;
}

/** draftKey names one conversation's draft in View.drafts. */
export function draftKey(session: string, conv: ConvRef): string {
	return convScopeKey(session, convKey(conv));
}

/** isMsgPinned reports whether a timeline is at the bottom. Unknown defaults to
 * pinned, so a background conversation keeps the newest slice. */
export function isMsgPinned(view: View, session: string, key: string): boolean {
	return view.msgPinned[convScopeKey(session, key)] ?? true;
}

/** openModal installs the single modal dialog. The popout, context menu, and
 * command palette cannot stay meaningfully visible behind a modal's backdrop,
 * so they close. */
export function openModal(view: View, modal: Modal): void {
	view.modal = modal;
	view.palette = null;
	view.popout = null;
	view.characterMenu = null;
}

/** closeModal clears the modal slot, if anything is open. It also drops any
 * palette layered over it: the palette's apply closure needs the dialog's
 * composer to stay mounted, so they close together. */
export function closeModal(view: View): void {
	view.modal = null;
	view.palette = null;
}

/** toggleModal opens the given top-bar dialog, or closes it when it is already
 * the open one. Opening any top-bar dialog leaves the Config view, the way the
 * top bar's other destinations do. */
export function toggleModal(view: View, kind: ModalKind): void {
	if (view.modal?.kind === kind) {
		view.modal = null;
		return;
	}
	openModal(view, { kind });
	view.settingsOpen = false;
}

/** openCommand opens one command palette shell in the palette slot, above any
 * modal. A format palette (one carrying `onFormat`) may mount while a dialog is
 * active, because it is contextual to a composer inside that dialog and must
 * keep the dialog mounted; any other palette is refused there (the global
 * chords are already gated behind `dialogOpen`). Like a top-bar dialog it
 * leaves the Config view. `selection` is the text the composer had selected
 * when the chord fired; `start` is the advanced sub-list to open on. Non-format
 * shells ignore all three. */
export function openCommand(
	view: View,
	command: CommandId,
	onFormat?: FormatApply,
	selection?: string,
	start?: string,
): void {
	if (view.modal !== null && onFormat === undefined) {
		return;
	}
	view.palette = { command, onFormat, selection, start };
	if (view.modal === null) {
		view.popout = null;
		view.characterMenu = null;
	}
	view.settingsOpen = false;
}

/** closeCommand clears the command-palette slot, if anything is open. */
export function closeCommand(view: View): void {
	view.palette = null;
}

/** togglePopout opens the given top-bar popout, or closes it when it is already
 * the open one. Like a top-bar dialog it replaces any other overlay. */
export function togglePopout(view: View, id: Popout): void {
	if (view.popout === id) {
		view.popout = null;
		return;
	}
	view.popout = id;
	view.modal = null;
	view.palette = null;
	view.characterMenu = null;
	view.settingsOpen = false;
}

/** closePopout clears the popout slot, if anything is open. */
export function closePopout(view: View): void {
	view.popout = null;
}

/** dialogOpen reports whether an overlay is capturing input, so global keys do
 * not move or scroll the workspace behind it. A popout is not modal and leaves
 * keyboard navigation live. */
export function dialogOpen(view: View): boolean {
	return (
		view.modal !== null ||
		view.palette !== null ||
		view.characterMenu !== null
	);
}

/** setEnterNewline flips the composer's send-key preference and persists it in
 * the device document. */
export function setEnterNewline(view: View, on: boolean): void {
	view.composerEnterNewline = on;
	saveDevicePrefs({ composerEnterNewline: on });
}

/** setSoundEnabled flips the notification-sound preference and persists it in
 * the device document. */
export function setSoundEnabled(view: View, on: boolean): void {
	view.soundEnabled = on;
	saveDevicePrefs({ soundEnabled: on });
}

/** setLimitMessageWidth flips the bounded-timeline preference and persists it
 * in the device document. */
export function setLimitMessageWidth(view: View, on: boolean): void {
	view.limitMessageWidth = on;
	saveDevicePrefs({ limitMessageWidth: on });
}
