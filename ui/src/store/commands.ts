import type { AppActions, Dispatch } from "../context.js";
import { createWarpmark, deleteWarpmark, fetchHistory, fetchSettings, fetchWarpmarks, putCharacterSettings, type AutoStatus, type CharacterSettings } from "../api.js";
import { profileURL } from "../lib/characters.js";
import { convLabel } from "../lib/format.js";
import { orderedConversations } from "../lib/order.js";
import { request } from "../render.js";
import { OPS, convKey, parseConvKey, type Interest, type MemberInfo, type Warpmark } from "../transport/protocol.js";
import type { Conversation, EntryWindow, Store } from "./state.js";
import { isConvFocused } from "./unread.js";
import { forget } from "./typing.js";
import { closeModal, draftKey, openModal, pushToast, removeTab, sessionAlive, type View } from "./state.js";
import { ensureWindow, mergeHistory, refreshBounds, toEntry } from "./window.js";
// Command helpers: the one place that combines store reads, view mutations,
// and dispatches for a user intent. Containers call these; presentational
// components never do.

/** closeSession logs a character out and tears down its local state. */
export async function closeSession(
	store: Store,
	view: View,
	actions: AppActions,
	session: string,
): Promise<void> {
	const err = await actions.logoutCharacter(session);
	if (err !== null) {
		pushToast(view, `Logout failed: ${err}`);
		return;
	}
	delete store.sessions[session];
	delete store.conversations[session];
	delete store.entries[session];
	delete store.characters[session];
	delete store.warpmarks[session];
	store.warpmarksRev++;
	forget(session);
	delete view.pendingConv[session];
	delete view.windowLru[session];
	// The search dialog is bound to the active session; if that session is the
	// one going away, drop its builder selections and close the dialog so it
	// does not reappear over the next tab.
	delete view.searchSelection[session];
	if (view.modal?.kind === "search" && view.activeSession === session) {
		closeModal(view);
	}
	store.conversationsRev++;
	const tab = view.tabs.find((t) => t.session === session);
	if (tab !== undefined) {
		removeTab(view, tab.id);
	}
}

// MAX_RETAINED_WINDOWS bounds how many released timelines a session parks for
// a delta re-entry. Six covers ordinary A/B switching and recent history
// without letting a heavily-visited session hoard windows.
const MAX_RETAINED_WINDOWS = 6;

/** retainWindow marks a conversation's retained window most-recently-used and
 * evicts the least-recently-used beyond the cap. The evicted conversation
 * simply re-materializes in full next time. */
function retainWindow(
	store: Store,
	view: View,
	session: string,
	key: string,
): void {
	const list = (view.windowLru[session] ?? []).filter((k) => k !== key);
	list.push(key);
	while (list.length > MAX_RETAINED_WINDOWS) {
		const evicted = list.shift();
		if (evicted === undefined) {
			break;
		}
		delete store.entries[session]?.[evicted];
	}
	view.windowLru[session] = list;
}

/** windowSince returns the highest conv_seq a retained window can resume from,
 * so the core can send a delta instead of a full window. A window that dropped
 * newer entries while scrolled up reports its newest retained seq, which makes
 * the delta refill the dropped span too. */
function windowSince(win: EntryWindow | undefined): number | undefined {
	if (win === undefined) {
		return undefined;
	}
	return win.newestSeq ?? win.liveSeq;
}

/** activateConv selects a conversation, releases full interest in the
 * previous one, and clears unread. */
export function activateConv(
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
	key: string,
): void {
	const previous = view.activeConv[session];
	const conv = parseConvKey(key);
	if (previous !== undefined && previous !== key) {
		releaseConv(store, view, dispatch, session, previous);
	}
	// A warp pane is a read-only alias: no interest, no tracking, no join. The
	// window is seeded over HTTP and ends at the mark.
	if (conv.kind === "warp") {
		view.activeConv[session] = key;
		void seedWarpWindow(store, session, key);
		return;
	}
	// Opening a DM tracks it in the core, so it stays visible (and is synced to
	// other clients) even before its first message and across a UI reload.
	if (conv.kind === "dm") {
		dispatch({ op: OPS.setTracked, session, conv, tracked: true });
	}
	view.activeConv[session] = key;
	markConvRead(store, view, session, key);
	// A retained window lets the core resume with a delta rather than a full
	// re-materialization; a fresh conversation asks for the full window.
	const win = store.entries[session]?.[key];
	const since = windowSince(win);
	dispatch({
		op: OPS.setInterest,
		session,
		conv,
		level: "full" satisfies Interest,
		...(since !== undefined ? { since } : {}),
	});
	const record = store.conversations[session]?.[key];
	if (record !== undefined) {
		record.interestAsked = true;
	}
	if (win !== undefined) {
		retainWindow(store, view, session, key);
	}
}

/** cycleConversation moves the active session's conversation selection up
 * (delta -1) or down (+1) the sidebar order — channels then DMs, each sorted
 * by title — wrapping at the ends. No-op without an active session or list.
 * It does not redraw; the caller requests. */
export function cycleConversation(
	store: Store,
	view: View,
	dispatch: Dispatch,
	delta: number,
): void {
	const session = view.activeSession;
	if (session === null) {
		return;
	}
	const list = orderedConversations(store.conversations[session]);
	if (list.length === 0) {
		return;
	}
	const current = view.activeConv[session];
	const index = list.findIndex((c) => c.key === current);
	const next =
		index === -1
			? delta > 0
				? 0
				: list.length - 1
			: (index + delta + list.length) % list.length;
	const target = list[next];
	if (target !== undefined && target.key !== current) {
		activateConv(store, view, dispatch, session, target.key);
	}
}

/** markConvRead clears a conversation's unread and highlight flags, but only
 * when it is actually being viewed: the active conversation in the active
 * session with the tab focused. Called on activation and when focus returns. */
export function markConvRead(
	store: Store,
	view: View,
	session: string,
	key: string,
): void {
	if (!isConvFocused(view, session, key)) {
		return;
	}
	const conv = store.conversations[session]?.[key];
	if (conv === undefined) {
		return;
	}
	if (conv.unread || conv.highlight) {
		conv.unread = false;
		conv.highlight = false;
		store.unreadRev++;
	}
}

function releaseConv(
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
	key: string,
): void {
	// A warp pane dispatched no interest and its window is HTTP-seeded; keep it
	// so returning to the pane reuses the window instead of refetching.
	if (parseConvKey(key).kind === "warp") {
		return;
	}
	dispatch({
		op: OPS.setInterest,
		session,
		conv: parseConvKey(key),
		level: "summary" satisfies Interest,
	});
	// Keep the timeline and downgrade only interest: re-entry can then ask the
	// core for a delta over the messages that arrived while away. The retained
	// window is bounded by retainWindow.
	const win = store.entries[session]?.[key];
	const conv = store.conversations[session]?.[key];
	if (conv !== undefined) {
		conv.materialized = win !== undefined;
		conv.interestAsked = false;
		// Typing is gated to full interest, so no off event arrives while the
		// conversation is released; drop the state rather than show it stale on
		// return. A still-typing partner re-signals on their next transition.
		conv.typing = {};
	}
	if (win !== undefined) {
		retainWindow(store, view, session, key);
	}
}

/** sendDraft sends the composer draft for the active conversation and creates
 * the optimistic entry. Returns false when there was nothing to send. */
export function sendDraft(
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
	key: string,
): boolean {
	const text = (view.drafts[draftKey(session, parseConvKey(key))] ?? "").trim();
	if (text === "" || store.sessions[session]?.state !== "live") {
		return false;
	}
	const conv = parseConvKey(key);
	const cid = dispatch({ op: OPS.sendMessage, session, conv, body: text });
	delete view.drafts[draftKey(session, conv)];

	// Optimistic entry: the ack replaces it (pending -> sent/failed, same id)
	// and the self echo retires it when the canonical copy arrives.
	const entryId = `pending-${cid}`;
	const win = ensureWindow(store, session, key);
	win.items.push({
		id: entryId,
		convSeq: Number.MAX_SAFE_INTEGER,
		kind: conv.kind === "dm" ? "dm" : "msg",
		speaker: session,
		html: previewHTML(text),
		time: Date.now(),
		self: true,
		send: "pending",
	});
	win.rev++;
	refreshBounds(win);
	store.pending[cid] = { session, conv, entryId };
	return true;
}

/** previewHTML renders a just-sent body for the optimistic entry. It mirrors
 * the core's message prefix (a leading "/me" action is dropped, anything else
 * gains ": ") so the pending row matches the canonical row that replaces it. */
function previewHTML(text: string): string {
	return escapeHTML(isEmote(text) ? text.slice(3) : `: ${text}`);
}

function isEmote(text: string): boolean {
	if (!text.startsWith("/me")) {
		return false;
	}
	const next = text[3];
	return next === undefined || /\s/.test(next);
}

function escapeHTML(text: string): string {
	const div = document.createElement("div");
	div.textContent = text;
	return div.innerHTML;
}

/** PAGE is how many entries one history request returns. */
const PAGE = 100;

/** loadOlder pages one older history page over HTTP and merges it into the
 * window. Resolves when done; the caller owns scroll restoration. */
export async function loadOlder(
	store: Store,
	session: string,
	key: string,
): Promise<void> {
	const conv = parseConvKey(key);
	const win: EntryWindow | undefined = store.entries[session]?.[key];
	if (win === undefined) {
		return;
	}
	const beforeSeq =
		win.oldestSeq !== undefined && win.oldestSeq < Number.MAX_SAFE_INTEGER
			? win.oldestSeq
			: undefined;
	if (beforeSeq === undefined && win.items.length > 0) {
		// Window exists but has no lower bound yet; nothing to page from.
		return;
	}
	const history = await fetchHistory({
		session,
		convKind: conv.kind,
		convId: conv.id,
		beforeSeq,
		limit: PAGE,
	});
	if (history === null) {
		return;
	}
	const current = store.entries[session]?.[key];
	if (current === undefined) {
		return; // evicted while paging
	}
	// Reading older history: keep the oldest end if the window overflows.
	mergeHistory(current, history.entries.map((e) => toEntry(e)), false);
	if (history.entries.length < PAGE) {
		current.hasOlder = false;
	}
}

/** loadNewer pages entries newer than the retained slice. Used to re-fill
 * silently when the view returns to the bottom after missing live messages. */
export async function loadNewer(
	store: Store,
	session: string,
	key: string,
): Promise<void> {
	const conv = parseConvKey(key);
	const win: EntryWindow | undefined = store.entries[session]?.[key];
	if (win === undefined || win.newestSeq === undefined || !win.hasNewer) {
		return;
	}
	const history = await fetchHistory({
		session,
		convKind: conv.kind,
		convId: conv.id,
		afterSeq: win.newestSeq,
		limit: PAGE,
	});
	if (history === null) {
		return;
	}
	const current = store.entries[session]?.[key];
	if (current === undefined) {
		return; // evicted while paging
	}
	// At the bottom: keep the newest end if the window overflows. If the page
	// added nothing (empty or fully duplicate), we have reached the edge.
	const added = mergeHistory(
		current,
		history.entries.map((e) => toEntry(e)),
		true,
	);
	if (!added) {
		current.hasNewer = false;
	}
}

/** dismissConv closes the active conversation's pane. A channel or room is left
 * upstream and dropped from the sidebar immediately (the core's later `left`
 * event, once F-Chat echoes the LCH, is an idempotent re-delete); a DM is only
 * hidden locally, because the core retains it and new activity reopens it. */
export function dismissConv(
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
	key: string,
): void {
	const conv = store.conversations[session]?.[key];
	if (conv === undefined) {
		return;
	}
	// A warp pane is client-local and read-only: nothing to leave upstream.
	if (conv.conv.kind === "warp") {
		delete store.conversations[session]?.[key];
		delete store.entries[session]?.[key];
		store.conversationsRev++;
		if (view.activeConv[session] === key) {
			delete view.activeConv[session];
		}
		return;
	}
	if (conv.conv.kind === "dm") {
		closeDm(store, view, dispatch, session, key, conv);
		return;
	}
	if (conv.conv.kind !== "official" && conv.conv.kind !== "room") {
		return;
	}
	dispatch({ op: OPS.leave, session, conv: conv.conv });
	// Drop the conversation and its entries locally now, symmetric with the
	// focus clear below. Waiting for the core's `left` event would leave the row
	// in the sidebar for a full client -> F-Chat round trip after the pane has
	// already moved on; the late event re-deletes harmlessly.
	delete store.conversations[session]?.[key];
	delete store.entries[session]?.[key];
	store.conversationsRev++;
	if (view.activeConv[session] === key) {
		delete view.activeConv[session];
	}
}

/** closeDm hides a DM from the sidebar and untracks it in the core. The
 * conversation stays in the core (with its history); interest is downgraded to
 * summary so bodies stop streaming, and a later message reopens it through
 * recordEntry. */
function closeDm(
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
	key: string,
	conv: Conversation,
): void {
	dispatch({ op: OPS.setTracked, session, conv: conv.conv, tracked: false });
	delete store.conversations[session]?.[key];
	delete store.entries[session]?.[key];
	store.conversationsRev++;
	if (view.activeConv[session] === key) {
		delete view.activeConv[session];
	}
	dispatch({
		op: OPS.setInterest,
		session,
		conv: conv.conv,
		level: "summary" satisfies Interest,
	});
}

// ==========================================================================
// warpmarks
// ==========================================================================
// A warpmark points at one stored entry. Opening it activates a virtual,
// read-only conversation "warp:<entryId>" whose window ends at the marked
// message, seeded over HTTP (no interest, no join). The mark list is HTTP-only
// and refetched on open or mutation. See docs/warpmarks.md.

/** Mithril lets a handler suppress its automatic redraw with `redraw = false`. */
type MithrilEvent = Event & { redraw?: boolean };

// warpPaneKey names the virtual conversation for one marked entry.
function warpPaneKey(entryId: string): string {
	return `warp:${entryId}`;
}

/** seedWarpWindow fetches the tail page ending at the mark once, if the pane
 * has no window yet. Older paging then reuses loadOlder unchanged. */
async function seedWarpWindow(
	store: Store,
	session: string,
	key: string,
): Promise<void> {
	const existing = store.entries[session]?.[key];
	if (existing !== undefined && existing.items.length > 0) {
		return;
	}
	const conv = parseConvKey(key);
	const history = await fetchHistory({
		session,
		convKind: conv.kind,
		convId: conv.id,
		limit: PAGE,
	});
	if (history === null) {
		return;
	}
	if (!sessionAlive(store, session)) {
		return; // session closed while the seed was in flight
	}
	const win = ensureWindow(store, session, key);
	mergeHistory(win, history.entries.map((e) => toEntry(e)), true);
	// The seed ends at the mark: older pages remain, newer does not exist.
	win.hasOlder = history.entries.length >= PAGE;
	request();
}

/** loadWarpmarks refetches one session's mark list into the store. */
export async function loadWarpmarks(
	store: Store,
	session: string,
): Promise<void> {
	const marks = await fetchWarpmarks(session);
	if (marks === null) {
		return;
	}
	if (!sessionAlive(store, session)) {
		return; // session closed while the list was in flight
	}
	store.warpmarks[session] = marks;
	store.warpmarksRev++;
	request();
}

/** warpmarkClick handles a click that targets a message's timestamp. The
 * timestamp element carries its own `data-entry`, so the clicked element must
 * be the timestamp itself: there is no ancestor lookup, and a click on the
 * nested send-state mark is not treated as a timestamp. It returns true when
 * the event was claimed — either opening the label prompt or deliberately
 * ignoring a non-markable target. A miss leaves the event for the character
 * handler and suppresses redraw so an unrelated click does not repaint the
 * memoized rows. */
export function warpmarkClick(
	store: Store,
	view: View,
	e: MouseEvent,
): boolean {
	const target = e.target;
	if (!(target instanceof Element) || !target.classList.contains("msg-time")) {
		return false;
	}
	const id = target.getAttribute("data-entry");
	const session = view.activeSession;
	const key = session === null ? undefined : view.activeConv[session];
	const entry =
		id === null || session === null || key === undefined
			? undefined
			: store.entries[session]?.[key]?.items.find((x) => x.id === id);
	// Only durable entries are markable: an optimistic send has no DB id.
	if (
		id === null ||
		session === null ||
		entry === undefined ||
		entry.send !== undefined
	) {
		(e as MithrilEvent).redraw = false;
		return true;
	}
	const existing = store.warpmarks[session]?.find((mk) => mk.entryId === id);
	openModal(view, {
		kind: "warpmark",
		session,
		entryId: id,
		speaker: entry.speaker,
		existing: existing !== undefined,
		label: existing?.label ?? "",
	});
	return true;
}

/** saveWarpmark creates or replaces the open dialog's mark and closes it. */
export async function saveWarpmark(
	store: Store,
	view: View,
	label: string,
): Promise<void> {
	const dialog = view.modal?.kind === "warpmark" ? view.modal : null;
	if (dialog === null) {
		return;
	}
	const ok = await createWarpmark(dialog.session, dialog.entryId, label);
	if (!ok) {
		pushToast(view, "Could not save the warpmark");
		request();
		return;
	}
	closeModal(view);
	await loadWarpmarks(store, dialog.session);
	pushToast(view, "Warpmark saved");
	request();
}

/** removeWarpmark deletes a mark by entry id and closes any pane for it. */
export async function removeWarpmark(
	store: Store,
	view: View,
	session: string,
	entryId: string,
): Promise<void> {
	const ok = await deleteWarpmark(session, entryId);
	if (!ok) {
		pushToast(view, "Could not delete the warpmark");
		request();
		return;
	}
	closeModal(view);
	closeWarpPane(store, view, session, entryId);
	await loadWarpmarks(store, session);
	pushToast(view, "Warpmark removed");
	request();
}

/** closeWarpPane drops a session's virtual pane for one entry, if it exists. */
function closeWarpPane(
	store: Store,
	view: View,
	session: string,
	entryId: string,
): void {
	const key = warpPaneKey(entryId);
	if (store.conversations[session]?.[key] === undefined) {
		return;
	}
	delete store.conversations[session]?.[key];
	delete store.entries[session]?.[key];
	store.conversationsRev++;
	if (view.activeConv[session] === key) {
		delete view.activeConv[session];
	}
}

/** openWarpmark opens a mark as a read-only pane. It creates the ephemeral
 * conversation on first open, then defers to activateConv's warp branch. */
export function openWarpmark(
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
	mark: Warpmark,
): void {
	if (mark.missing === true) {
		pushToast(view, "That message is no longer stored");
		request();
		return;
	}
	const key = warpPaneKey(mark.entryId);
	const title =
		mark.label !== ""
			? mark.label
			: `${mark.speaker} — ${convLabel(mark.convName, mark.conv.id)}`;
	const per = (store.conversations[session] ??= {});
	const existing = per[key];
	if (existing === undefined) {
		per[key] = {
			key,
			conv: { kind: "warp", id: mark.entryId },
			session,
			title,
			unread: false,
			highlight: false,
			lastActivity: Date.parse(mark.createdAt) || 0,
			readOnly: true,
			typing: {},
			materialized: true,
		};
		store.conversationsRev++;
	} else if (existing.title !== title) {
		// A label edited since the pane was created re-titles it.
		existing.title = title;
		store.conversationsRev++;
	}
	activateConv(store, view, dispatch, session, key);
}

/** openRealWarpConv leaves the read-only pane for the real conversation: a DM
 * is tracked, a joined channel/room is focused, and an unjoined one is joined
 * first. This is the only warp path that touches the live machinery. */
export function openRealWarpConv(
	store: Store,
	view: View,
	dispatch: Dispatch,
	actions: AppActions,
	session: string,
	entryId: string,
): void {
	const mark = store.warpmarks[session]?.find((mk) => mk.entryId === entryId);
	if (mark === undefined || mark.missing === true) {
		return;
	}
	const key = convKey(mark.conv);
	// A DM, a broadcast, or an already-known conversation needs no join.
	if (
		mark.conv.kind === "dm" ||
		mark.conv.kind === "broadcast" ||
		store.conversations[session]?.[key] !== undefined
	) {
		activateConv(store, view, dispatch, session, key);
		return;
	}
	void actions
		.joinChannel(
			session,
			mark.conv.kind === "room" ? "room" : "official",
			mark.conv.id,
		)
		.then((err) => {
			if (err !== null) {
				pushToast(view, `Join failed: ${err}`);
			} else {
				activateConv(store, view, dispatch, session, key);
			}
			request();
		});
}

const MENU_WIDTH = 240;
const MENU_HEIGHT = 200;
/** DESKTOP is resolved once, at module load. A hovering fine pointer means the
 * device has a real secondary button, so left click can open the profile and
 * right click the menu. Engines that predate the Level-4 media features
 * (QtWebKit) report false and fall back to tap-style behavior: every
 * activation opens the menu. */
const DESKTOP =
	typeof window !== "undefined" &&
	typeof window.matchMedia === "function" &&
	window.matchMedia("(hover: hover) and (pointer: fine)").matches;

/** rosterCharacterFromEvent resolves the clicked roster row's character name
 * from the delegated `data-character` attribute. */
function rosterCharacterFromEvent(e: MouseEvent): string | null {
	const target = e.target;
	if (!(target instanceof Element)) {
		return null;
	}
	const name = target.closest("[data-character]")?.getAttribute("data-character");
	return name !== undefined && name !== null && name !== "" ? name : null;
}

/** openCharacterMenuAt opens the roster context menu for `name` at the cursor,
 * clamped to the viewport. The presence snapshot lets the menu render a
 * transient search result the client registry does not hold. */
function openCharacterMenuAt(
	view: View,
	session: string,
	name: string,
	x: number,
	y: number,
	character?: MemberInfo,
): void {
	view.characterMenu = {
		session,
		name,
		x: Math.max(0, Math.min(x, window.innerWidth - MENU_WIDTH)),
		y: Math.max(0, Math.min(y, window.innerHeight - MENU_HEIGHT)),
		character,
	};
}

/** openProfile opens a character's F-List page in a new tab. window.open keeps
 * the clickable element a <button>; a null opener prevents the new tab from
 * reaching back into the app. */
function openProfile(name: string): void {
	const w = window.open(profileURL(name), "_blank");
	if (w !== null) {
		w.opener = null;
	}
}

/** spoilerClick toggles a BBCode [spoiler]. Only the spoiler element itself is
 * a target: the body wrapper and everything revealed inside keep their own
 * behavior (links navigate, nested spoilers toggle themselves), so a click
 * there never collapses the outer spoiler. Toggling the class is a direct DOM
 * change, so the handler suppresses Mithril's redraw. */
function spoilerClick(e: MouseEvent): boolean {
	const target = e.target;
	if (!(target instanceof Element) || !target.classList.contains("bc-spoiler")) {
		return false;
	}
	target.classList.toggle("is-revealed");
	(e as MithrilEvent).redraw = false;
	return true;
}

/** findConvKey resolves an existing conversation key without regard to case.
 * The client keys by the server's casing (a JCH/ConvView may spell an ADH id
 * differently from the posted tag), so a raw key compare would fork a second
 * sidebar entry. */
function findConvKey(
	store: Store,
	session: string,
	want: string,
): string | undefined {
	const convs = store.conversations[session];
	if (convs === undefined) {
		return undefined;
	}
	const lower = want.toLowerCase();
	return Object.keys(convs).find((k) => k.toLowerCase() === lower);
}

/** sessionClick opens the conversation behind a BBCode [session] link. The tag
 * renders a span carrying the target kind and id (rooms today; official
 * channels planned). An already-joined conversation is focused immediately;
 * otherwise the request is recorded in `view.pendingConv` and a join is sent,
 * and `activatePendingConv` opens it once the core's JCH has created it. This
 * is deliberately not optimistic: the conversation only exists after the
 * server confirms the join, so the pane never points at a conv key that does
 * not exist yet. preventDefault stops an enclosing [url] from navigating as
 * the click bubbles past this handler. */
function sessionClick(
	store: Store,
	view: View,
	dispatch: Dispatch,
	actions: AppActions,
	e: MouseEvent,
): boolean {
	const target = e.target;
	if (!(target instanceof Element)) {
		return false;
	}
	const el = target.closest(".bc-session");
	if (el === null) {
		return false;
	}
	e.preventDefault();
	const id = el.getAttribute("data-conv-id");
	const session = view.activeSession;
	const kind =
		el.getAttribute("data-conv-kind") === "official" ? "official" : "room";
	if (id === null || session === null) {
		return true;
	}
	const want = convKey({ kind, id });
	// Already joined (server-confirmed): focus it now and drop any pending
	// request left over from an earlier click.
	const existing = findConvKey(store, session, want);
	if (existing !== undefined) {
		delete view.pendingConv[session];
		activateConv(store, view, dispatch, session, existing);
		return true;
	}
	// A join for this same target is already in flight; don't send a second.
	if (view.pendingConv[session] === want) {
		return true;
	}
	view.pendingConv[session] = want;
	void actions.joinChannel(session, kind, id).then((err) => {
		if (err !== null) {
			// A different click may have replaced the request; only clear ours.
			if (view.pendingConv[session] === want) {
				delete view.pendingConv[session];
			}
			pushToast(view, `Could not open ${id}: ${err}`);
		}
	});
	return true;
}

/** activatePendingConv opens a conversation requested by a [session] link once
 * the server's JCH has created it. The session view calls this during render
 * (which already activates for auto-select), so it is a no-op until the
 * conversation actually exists; a request that never materializes is dropped
 * by the session's error event or on logout. */
export function activatePendingConv(
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
): void {
	const want = view.pendingConv[session];
	if (want === undefined) {
		return;
	}
	const key = findConvKey(store, session, want);
	if (key === undefined) {
		return;
	}
	delete view.pendingConv[session];
	activateConv(store, view, dispatch, session, key);
}

/** characterSnapshot resolves the freshest record for a character by name: the
 * live registry when present, else the session's latest search result, which
 * carries presence the registry may not hold. */
function characterSnapshot(
	store: Store,
	session: string,
	name: string,
): MemberInfo | undefined {
	return (
		store.characters[name] ??
		store.search[session]?.find((r) => r.name === name)
	);
}

/** openCharacterMenu opens the context menu for a clicked character row at the
 * cursor, passing the best-available presence snapshot. */
function openCharacterMenu(
	store: Store,
	view: View,
	session: string,
	name: string,
	e: MouseEvent,
): void {
	openCharacterMenuAt(
		view,
		session,
		name,
		e.clientX,
		e.clientY,
		characterSnapshot(store, session, name),
	);
}

/** menuFromEvent opens the character context menu when the event hit a
 * character row. A miss leaves the event alone, so a right click on empty
 * container space keeps the browser's own menu. */
function menuFromEvent(store: Store, view: View, e: MouseEvent): void {
	const session = view.activeSession;
	const name = rosterCharacterFromEvent(e);
	if (session === null || name === null) {
		return;
	}
	e.preventDefault();
	openCharacterMenu(store, view, session, name, e);
}

/** activateCharacter runs the pointer-dependent activation for a character
 * row. On a fine pointer a left click opens the profile, while a keyboard
 * activation (click with detail 0 and no pointer) opens the menu; a touch tap
 * always opens the menu. */
function activateCharacter(store: Store, view: View, e: MouseEvent): void {
	const session = view.activeSession;
	const name = rosterCharacterFromEvent(e);
	if (session === null || name === null) {
		return;
	}
	if (DESKTOP && e.detail > 0) {
		openProfile(name);
		return;
	}
	openCharacterMenu(store, view, session, name, e);
}

/** clickHandlers is the chatspace's single delegated pointer handler. Attach
 * both keys to the chatspace root: every otherwise-unbound pointer event
 * inside bubbles here and is routed by type.
 *
 *   - click: a [spoiler] toggles; a [session] link opens its conversation; a
 *     message timestamp opens the warpmark prompt; anything else falls through
 *     to the character rows;
 *   - contextmenu: a character row opens its context menu.
 *
 * The spoiler and timestamp checks are exact (the clicked element itself), so
 * revealed content and nested marks are never mistaken for their container.
 * The session check uses `closest` because the tag's span wraps its label.
 * Character rows keep one `closest` lookup because the clickable button wraps
 * the real target (name, avatar); no other click type reaches across elements. */
export function clickHandlers(
	store: Store,
	view: View,
	dispatch: Dispatch,
	actions: AppActions,
): {
	onclick: (e: MouseEvent) => void;
	oncontextmenu: (e: MouseEvent) => void;
} {
	const onEvent = (e: MouseEvent): void => {
		if (e.type === "contextmenu") {
			menuFromEvent(store, view, e);
			return;
		}
		if (spoilerClick(e)) {
			return;
		}
		if (sessionClick(store, view, dispatch, actions, e)) {
			return;
		}
		if (warpmarkClick(store, view, e)) {
			return;
		}
		activateCharacter(store, view, e);
	};
	return { onclick: onEvent, oncontextmenu: onEvent };
}

/** closeCharacterMenu dismisses the roster context menu. */
export function closeCharacterMenu(view: View): void {
	view.characterMenu = null;
}

/** setStatus sends the character's own status and status message. The core
 * emits a rendered self presence event; the raw text is remembered locally so
 * the editor prefills before the next snapshot. */
export function setStatus(
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
	status: string,
	statusMsg: string,
): void {
	const sess = store.sessions[session];
	if (sess === undefined) {
		return;
	}
	dispatch({ op: OPS.setStatus, session, status, statusMsg });
	sess.selfStatusText = statusMsg;
	closeModal(view);
}

/** loadAutoStatus reads one character's saved automatic status. It resolves to
 * `undefined` on a failed read and to `null` when no status is saved. */
export async function loadAutoStatus(
	session: string,
): Promise<AutoStatus | null | undefined> {
	const v = await fetchSettings(session);
	if (v === null) {
		return undefined;
	}
	return v.character.autoStatus ?? null;
}

/** saveAutoStatus replaces a character's automatic status, or clears it when
 * `auto` is null. The settings API is whole-document, so the current document
 * is read back and only this field is replaced. It resolves to an error
 * message, or null on success. */
export async function saveAutoStatus(
	session: string,
	auto: AutoStatus | null,
): Promise<string | null> {
	const v = await fetchSettings(session);
	if (v === null) {
		return "Could not load the settings document.";
	}
	const next: CharacterSettings = {
		highlights: v.character.highlights ?? [],
		autoJoin: v.character.autoJoin ?? [],
	};
	if (auto !== null) {
		next.autoStatus = auto;
	}
	const r = await putCharacterSettings(session, next);
	return r.ok ? null : (r.error ?? "Request failed.");
}

/** setIgnore blocks (on) or unblocks a character on the account ignore list. */
export function setIgnore(
	store: Store,
	view: View,
	dispatch: Dispatch,
	session: string,
	name: string,
	on: boolean,
): void {
	if (store.sessions[session] === undefined) {
		return;
	}
	dispatch({
		op: OPS.setIgnore,
		session,
		character: name,
		action: on ? "add" : "delete",
	});
	closeCharacterMenu(view);
}
