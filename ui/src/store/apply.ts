import type { AccountState, Batch, ConvStatePayload, ConvView, ChannelsPayload, Envelope, Event, FriendsPayload, IgnoresPayload, MemberInfo, MessagePayload, PresencePayload, SearchNotice, SessionSnapshot, Snapshot, StatePayload, SummaryPayload, TypingPayload } from "../transport/protocol.js";
import { convKey, parseConvKey, type ConvRef } from "../transport/protocol.js";
import { type Conversation, type Entry, type EntryWindow, type Store, applyPresence } from "./state.js";
import { isConvFocused } from "./unread.js";
import { playAttention } from "../sound.js";
import { request } from "../render.js";
import { noteSearchRevision, recallSearch } from "./search.js";
import { addTab, hydrateTab, isMsgPinned, pushToast, reconcileTabs, type View } from "./state.js";
import { ensureWindow, insertLive, toEntry, trimWindow } from "./window.js";
// The single write path into the domain store. Every event batch, snapshot,
// account update, and send-result passes through here.

export function applyEnvelope(store: Store, view: View, env: Envelope): void {
	switch (env.t) {
		case "snapshot":
			applySnapshot(store, view, env.d as Snapshot);
			break;
		case "account_state":
			applyAccount(store, view, env.d as AccountState);
			break;
		case "batch":
			applyBatch(store, view, env.d as Batch);
			break;
	}
}

/** applySendResult retires an optimistic entry: pending -> sent, or pending ->
 * failed with the core's error message. The record is replaced wholesale and
 * the window's rev bumped -- an in-place write here would be invisible to the
 * views that memoize on Entry references and win.rev (a failed send would keep
 * showing its pending mark).
 *
 * A successful ack deliberately keeps the `pending` mapping: the self echo
 * that follows carries the command cid and uses it to retire the exact
 * optimistic row (see applyMessage). A failure has no echo coming, so the
 * mapping is dropped here. The mapping is also dropped and the row later
 * removed when the window is replaced without the echo (prunePending). */
export function applySendResult(
	store: Store,
	cid: string,
	accepted: boolean,
	errorMsg?: string,
): void {
	const p = store.pending[cid];
	if (p === undefined) {
		return;
	}
	const win = store.entries[p.session]?.[convKey(p.conv)];
	if (win === undefined) {
		delete store.pending[cid];
		return;
	}
	const idx = win.items.findIndex((e) => e.id === p.entryId);
	const entry = idx < 0 ? undefined : win.items[idx];
	if (entry === undefined) {
		delete store.pending[cid];
		return;
	}
	if (!accepted) {
		delete store.pending[cid];
	}
	win.items[idx] = {
		...entry,
		send: accepted ? "sent" : "failed",
		error: errorMsg,
	};
	win.rev++;
}

// --- snapshot ---

function applySnapshot(store: Store, view: View, snap: Snapshot): void {
	const next: Record<string, SessionSnapshot> = {};
	for (const s of snap.sessions ?? []) {
		next[s.character] = s;
	}
	store.sessions = next;

	// Rebuild conversation summaries; loaded windows survive if the conv did.
	const convs: Record<string, Record<string, Conversation>> = {};
	for (const s of snap.sessions ?? []) {
		const per: Record<string, Conversation> = {};
		for (const c of s.conversations ?? []) {
			const existing = store.conversations[s.character]?.[convKey(c.conv)];
			per[convKey(c.conv)] = {
				key: convKey(c.conv),
				conv: c.conv,
				session: s.character,
				title: c.title ?? existing?.title,
				description: existing?.description,
				mode: existing?.mode,
				unread: existing?.unread ?? false,
				highlight: existing?.highlight ?? false,
				lastActivity: Date.parse(c.lastActivity) || 0,
				members: existing?.members,
				ops: existing?.ops,
				typing: existing?.typing ?? {},
				materialized: existing?.materialized ?? false,
				interestAsked: existing?.interestAsked ?? false,
			};
		}
		convs[s.character] = per;
	}
	store.conversations = convs;
	store.conversationsRev++;

	// Drop windows for conversations that no longer exist.
	for (const [session, per] of Object.entries(store.entries)) {
		const live = convs[session];
		if (live === undefined) {
			delete store.entries[session];
			continue;
		}
		for (const key of Object.keys(per)) {
			if (live[key] === undefined) {
				delete per[key];
			}
		}
	}

	// Seed each session's own presence (status, status message, admin). The
	// account-wide friends/bookmarks and ignores lists are carried once at the
	// snapshot root, so they are applied once here (applyFriends/applyIgnores fan
	// out to every session themselves).
	for (const s of snap.sessions ?? []) {
		if (s.self.character !== "") {
			applyPresence(store, s.self, true);
		}
	}
	if (snap.friends !== undefined) {
		applyFriends(store, snap.friends);
	}
	if (snap.ignores !== undefined) {
		applyIgnores(store, snap.ignores);
	}
	if (snap.catalog !== undefined) {
		applyChannels(store, snap.catalog);
	}
	// Hydrate tabs from the snapshot on first load only. Tabs are client-owned:
	// a later reconnect snapshot must not reorder or drop them.
	if (view.tabs.length === 0) {
		for (const character of Object.keys(next)) {
			hydrateTab(view, character);
		}
		if (view.tabs.length === 0) {
			addTab(view);
		} else {
			view.activeTab = view.tabs[0]?.id ?? null;
		}
	}
	// A full snapshot can drop a session the client still has a tab bound to;
	// unbind it before choosing the active pane.
	reconcileTabs(view, store);
	pickActive(view, convs);
}

/** applySessionLost removes a session whose removal tombstone arrived and
 * unbinds any tab that pointed at it. */
function applySessionLost(store: Store, view: View, character: string): void {
	delete store.sessions[character];
	delete store.conversations[character];
	delete store.entries[character];
	delete view.windowLru[character];
	store.conversationsRev++;
	reconcileTabs(view, store);
}

/** applyChannels replaces the core-wide channel catalog, ordered by
 * population (user count) descending so the join dialog lists the busiest
 * channels first. Core-wide, never per-session; safe to call from any event. */
function applyChannels(store: Store, p: ChannelsPayload): void {
	store.channels = {
		official: [...(p.official ?? [])].sort(
			(a, b) => b.characters - a.characters || a.name.localeCompare(b.name),
		),
		rooms: [...(p.rooms ?? [])].sort(
			(a, b) =>
				b.characters - a.characters ||
				(a.title !== "" ? a.title : a.name).localeCompare(
					b.title !== "" ? b.title : b.name,
				),
		),
	};
}

function pickActive(
	view: View,
	convs: Record<string, Record<string, Conversation>>,
): void {
	if (
		view.activeTab === null ||
		!view.tabs.some((t) => t.id === view.activeTab)
	) {
		view.activeTab = view.tabs[view.tabs.length - 1]?.id ?? null;
	}
	const session = view.activeSession;
	if (session !== null) {
		const current = view.activeConv[session];
		if (current === undefined || convs[session]?.[current] === undefined) {
			const next = newestConvKey(convs[session]);
			if (next === undefined) {
				delete view.activeConv[session];
			} else {
				view.activeConv[session] = next;
			}
		}
	}
}

function newestConvKey(
	per: Record<string, Conversation> | undefined,
): string | undefined {
	if (per === undefined) {
		return undefined;
	}
	let best: string | undefined;
	let bestAt = -1;
	for (const c of Object.values(per)) {
		if (c.lastActivity >= bestAt) {
			bestAt = c.lastActivity;
			best = c.key;
		}
	}
	return best;
}

function applyAccount(store: Store, view: View, state: AccountState): void {
	store.account = state;
	// applyAccount is the sole driver of the credentials gate: the client stays
	// on the boot spinner while restoring, and only the core's verdict reveals
	// the gate (or the chatspace). "checking" leaves the current screen up.
	switch (state.status) {
		case "ok":
			view.credentialsError = null;
			view.phase = "chatspace";
			break;
		case "missing":
			view.credentialsError = null;
			view.phase = "credentials";
			break;
		case "invalid":
		case "unreachable":
			view.credentialsError = state.reason ?? "Could not sign in to F-List.";
			view.phase = "credentials";
			break;
		case "checking":
			break;
	}
}

// --- batch events ---

function applyBatch(store: Store, view: View, batch: Batch): void {
	for (const ev of batch.events ?? []) {
		switch (ev.kind) {
			case "message":
				applyMessage(store, view, ev);
				break;
			case "state":
				applyState(store, view, ev.payload);
				break;
			case "conv_view":
				applyConvView(store, ev.payload);
				break;
			case "error": {
				// A pending [session] open is dropped on any session error: a rejected
				// join posts ERR and will never create the conversation.
				const session = ev.payload.session;
				if (session !== undefined) {
					delete view.pendingConv[session];
				}
				if (ev.payload.message !== undefined && ev.payload.message !== "") {
					pushToast(view, ev.payload.message);
				}
				break;
			}
		}
	}
}

/** applyState dispatches one set-to record by its key namespace. Every former
 * per-kind event is one of these: account sets, session state, conversation
 * metadata and removal, activity, typing, presence, and search notices. The
 * event no longer carries a session; the key's first path segment past the
 * namespace is it (session/character is the namespace's whole rest). */
function applyState(store: Store, view: View, sp: StatePayload | undefined): void {
	if (sp === undefined || typeof sp.key !== "string") {
		return;
	}
	const slash = sp.key.indexOf("/");
	const ns = slash < 0 ? sp.key : sp.key.slice(0, slash);
	const rest = slash < 0 ? "" : sp.key.slice(slash + 1);
	// conv/summary/typing/search embed the session as the first path segment;
	// account/character/session are the namespaces where it is absent or whole.
	const sep = rest.indexOf("/");
	const session = sep < 0 ? rest : rest.slice(0, sep);
	switch (ns) {
		case "account":
			applyAccountState(store, rest, sp);
			break;
		case "session":
			applySessionKey(store, view, rest, sp);
			break;
		case "conv":
			applyConvKey(store, session, sep < 0 ? "" : rest.slice(sep + 1), sp);
			break;
		case "summary":
			applySummary(store, view, sp.value as SummaryPayload | undefined, session);
			break;
		case "typing": {
			const v = sp.value as TypingPayload | undefined;
			if (v !== undefined) {
				applyTyping(store, v, session);
			}
			break;
		}
		case "character": {
			const v = sp.value as PresencePayload | undefined;
			if (v !== undefined) {
				applyPresence(store, v, true);
			}
			break;
		}
		case "search":
			applySearchState(store, view, session, sp);
			break;
	}
}

function applyAccountState(
	store: Store,
	rest: string,
	sp: StatePayload,
): void {
	switch (rest) {
		case "friends": {
			const fp = sp.value as FriendsPayload | undefined;
			if (fp !== undefined) {
				applyFriends(store, fp.friends);
			}
			break;
		}
		case "ignores": {
			const ip = sp.value as IgnoresPayload | undefined;
			if (ip !== undefined) {
				applyIgnores(store, ip.ignores);
			}
			break;
		}
		case "catalog": {
			const cp = sp.value as ChannelsPayload | undefined;
			if (cp !== undefined) {
				applyChannels(store, cp);
			}
			break;
		}
	}
}

function applySessionKey(
	store: Store,
	view: View,
	character: string,
	sp: StatePayload,
): void {
	if (sp.removed === true) {
		applySessionLost(store, view, character);
		return;
	}
	applySessionStateValue(store, character, sp.value as Parameters<typeof applySessionStateValue>[2]);
}

function applyConvKey(
	store: Store,
	session: string,
	convPart: string,
	sp: StatePayload,
): void {
	if (convPart === "") {
		return;
	}
	const conv = parseConvKey(convPart);
	if (sp.removed === true) {
		removeConv(store, session, convKey(conv));
		return;
	}
	const v = sp.value as ConvStatePayload | undefined;
	if (v !== undefined) {
		applyConvValue(store, session, v);
	}
}

function applySearchState(
	store: Store,
	view: View,
	session: string,
	sp: StatePayload,
): void {
	const notice = sp.value as SearchNotice | undefined;
	// The record is a notice, not the rows: record the revision and pull the
	// cached set only when a dialog is actually showing it.
	noteSearchRevision(store, session, notice?.revision ?? 0);
	if (view.modal?.kind === "search" && view.activeSession === session) {
		void recallSearch(store, session).then(request);
	}
}

function ensureConv(store: Store, session: string, conv: ConvRef): Conversation {
	const per = (store.conversations[session] ??= {});
	const key = convKey(conv);
	const existing = per[key];
	if (existing !== undefined) {
		return existing;
	}
	store.conversationsRev++;
	return (per[key] = {
		key,
		conv,
		session,
		unread: false,
		highlight: false,
		lastActivity: 0,
		typing: {},
		materialized: false,
	});
}

function applyMessage(store: Store, view: View, ev: Extract<Event, { kind: "message" }>): void {
	const p = ev.payload;
	if (p === undefined) {
		return;
	}
	// The entry carries its session; the event wrapper does not.
	const session = p.entry.session;
	const key = convKey(p.conv);
	const conv = ensureConv(store, session, p.conv);
	const time = p.entry.createdAtMs;
	conv.lastActivity = Math.max(conv.lastActivity, time);

	// Play the attention sound for elevated traffic. At full interest this stream
	// entry is the only message signal -- the core suppresses the summary -- so
	// the chime lives here as well as in applySummary.
	if (
		view.soundEnabled &&
		!p.self &&
		(p.highlight === true || p.conv.kind === "dm")
	) {
		playAttention();
	}

	// Unread is client-owned: flag it only when this conversation is not the
	// focused pane in a focused tab. Highlight is the core's per-message signal.
	if (!isConvFocused(view, session, key)) {
		let dirty = false;
		if (!conv.unread) {
			conv.unread = true;
			dirty = true;
		}
		if (p.highlight === true && !conv.highlight) {
			conv.highlight = true;
			dirty = true;
		}
		if (dirty) {
			store.unreadRev++;
		}
	}

	const win = ensureWindow(store, session, key);

	// Self echo: retire the optimistic copy so the canonical entry replaces it.
	// The command cid is the only correlation: a self message this client sent
	// always carries one (the core copies the command cid onto the payload), and
	// only pending sends have one, so a rapid or retried pair retires the exact
	// row. A cid-less self message is a message our character produced elsewhere
	// (another client on the same account); there is no optimistic row to retire,
	// so it is inserted normally -- never matched against a pending row by
	// speaker, which would retire an unrelated in-flight send.
	if (p.self && p.cid !== undefined && p.cid !== "") {
		const pending = store.pending[p.cid];
		if (pending !== undefined) {
			delete store.pending[p.cid];
			const idx = win.items.findIndex((e) => e.id === pending.entryId);
			if (idx >= 0) {
				win.items.splice(idx, 1);
				win.rev++;
			}
		}
	}

	insertLive(
		win,
		toEntry(p.entry, p.self),
		isMsgPinned(view, session, key) || p.self,
	);
}

function applyConvView(store: Store, v: ConvView): void {
	const key = convKey(v.conv);
	const conv = ensureConv(store, v.session, v.conv);
	if (v.title !== undefined && v.title !== "" && conv.title !== v.title) {
		conv.title = v.title;
		store.conversationsRev++;
	}
	if (v.description !== undefined && v.description !== "") {
		conv.description = v.description;
	}
	if (v.mode !== undefined && v.mode !== "") {
		conv.mode = v.mode;
	}
	if (v.members !== undefined) {
		const names = v.members.map((m) => m.name);
		if (!sameStrings(conv.members, names)) {
			conv.members = names;
		}
		for (const m of v.members) {
			applyPresence(
				store,
				{
					character: m.name,
					gender: m.gender,
					status: m.status,
					statusMsg: m.statusMsg,
					admin: m.admin,
					online: m.online,
				},
				true,
			);
		}
	}

	const per = (store.entries[v.session] ??= {});
	const prior = per[key];

	// A delta view carries only the entries the client missed (it asked for this
	// by supplying `since`). Merge it into the retained window, keeping older
	// history, instead of replacing the window.
	if (v.delta === true) {
		const base = prior !== undefined && prior.items.length > 0 ? prior.items : [];
		const merged = [
			...base,
			...v.window.map((e) => toEntry(e, e.speaker === v.session)),
		];
		merged.sort((a, b) => a.convSeq - b.convSeq || a.id.localeCompare(b.id));
		const deduped = dedupById(merged);

		// liveSeq is the highest seq ever seen: the retained high-water mark plus
		// the delta cursor and any live entries that raced the build.
		let live = prior?.liveSeq ?? 0;
		if (v.cursor.asOfSeq > live) {
			live = v.cursor.asOfSeq;
		}
		for (const e of deduped) {
			if (e.send === undefined && e.convSeq > live) {
				live = e.convSeq;
			}
		}
		const win: EntryWindow = {
			items: deduped,
			// A delta never invalidates older history the client already held; a
			// window-less fallback conservatively claims older entries remain.
			hasOlder: base.length > 0 ? prior!.hasOlder : true,
			hasNewer: false,
			liveSeq: live,
			rev: 0,
		};
		trimWindow(win, true);
		prunePending(store, v.session, win);
		per[key] = win;
		conv.materialized = true;
		return;
	}

	// A full materialization is a fresh newest window: keep only live entries
	// that are newer than the cursor (they arrived while it was in flight).
	const kept =
		prior === undefined
			? []
			: prior.items.filter((e) => e.convSeq > v.cursor.asOfSeq);
	const items = [...v.window.map((e) => toEntry(e, e.speaker === v.session)), ...kept];
	items.sort((a, b) => a.convSeq - b.convSeq || a.id.localeCompare(b.id));
	const deduped = dedupById(items);

	// liveSeq is the highest seq ever seen: the cursor plus any live entries
	// that arrived while materialization was in flight.
	let live = v.cursor.asOfSeq;
	for (const e of deduped) {
		if (e.send === undefined && e.convSeq > live) {
			live = e.convSeq;
		}
	}
	const win: EntryWindow = {
		items: deduped,
		hasOlder: v.cursor.hasOlder,
		hasNewer: false,
		liveSeq: live,
		rev: 0,
	};
	trimWindow(win, true);
	prunePending(store, v.session, win);
	per[key] = win;
	conv.materialized = true;
}

/** dedupById keeps the first entry per id, preserving order. Kept live entries
 * and a delta window may overlap at a boundary. */
function dedupById(items: Entry[]): Entry[] {
	const seen = new Set<string>();
	const out: Entry[] = [];
	for (const e of items) {
		if (seen.has(e.id)) {
			continue;
		}
		seen.add(e.id);
		out.push(e);
	}
	return out;
}

/** prunePending drops optimistic-send mappings for a session whose row is no
 * longer present in a replaced window. A self echo that never arrived (a
 * dropped batch, or a broker resync) leaves a mapping pointing at an id the
 * new window does not contain; without this it could later match a reused id. */
function prunePending(store: Store, session: string, win: EntryWindow): void {
	const ids = new Set(win.items.map((e) => e.id));
	for (const [cid, p] of Object.entries(store.pending)) {
		if (p.session === session && !ids.has(p.entryId)) {
			delete store.pending[cid];
		}
	}
}

/** sameStrings reports whether an optional stored array already equals the
 * incoming one. Set-to member/op lists arrive repeatedly (a metadata refresh or
 * a resync); skipping the replacing write keeps the roster's reference check
 * from re-sorting an unchanged membership. */
function sameStrings(a: readonly string[] | undefined, b: readonly string[]): boolean {
	if (a === undefined || a.length !== b.length) {
		return false;
	}
	for (let i = 0; i < a.length; i++) {
		if (a[i] !== b[i]) {
			return false;
		}
	}
	return true;
}

// applyConvValue upserts a conversation's set-to metadata. The core emits
// metadata only for a conversation the character is in, so creating on an
// unknown conversation is correct (there is no out-of-order resurrection to
// guard against).
function applyConvValue(store: Store, session: string, p: ConvStatePayload): void {
	const conv = ensureConv(store, session, p.conv);
	if (p.title !== undefined && p.title !== "" && conv.title !== p.title) {
		conv.title = p.title;
		store.conversationsRev++;
	}
	if (p.description !== undefined && p.description !== "") {
		conv.description = p.description;
	}
	if (p.mode !== undefined && p.mode !== "") {
		conv.mode = p.mode;
	}
	if (p.members != null && !sameStrings(conv.members, p.members)) {
		conv.members = [...p.members];
	}
	// ops is always present and set-to; an empty list clears the op marks.
	if (p.ops != null && !sameStrings(conv.ops, p.ops)) {
		conv.ops = [...p.ops];
	}
}

// removeConv drops a conversation and its loaded window (left/gone).
function removeConv(store: Store, session: string, key: string): void {
	delete store.conversations[session]?.[key];
	delete store.entries[session]?.[key];
	store.conversationsRev++;
}

function applyTyping(store: Store, p: TypingPayload | undefined, session: string): void {
	if (p === undefined) {
		return;
	}
	// Typing is a signal about a conversation, not a reason to create one.
	const conv = store.conversations[session]?.[convKey(p.conv)];
	if (conv === undefined) {
		return;
	}
	if (p.on) {
		conv.typing[p.character] = { at: Date.now(), paused: p.paused === true };
	} else {
		delete conv.typing[p.character];
	}
}

function applySummary(
	store: Store,
	view: View,
	p: SummaryPayload | undefined,
	session: string,
): void {
	if (p === undefined) {
		return;
	}
	// A summary is a signal about a conversation the client already knows (every
	// live conversation is in the snapshot); it never creates one.
	const conv = store.conversations[session]?.[convKey(p.conv)];
	if (conv === undefined) {
		return;
	}
	// Play the attention sound for elevated traffic. A summary is the only
	// activity signal a background conversation sees, and a full-interest
	// subscriber also gets the message event, so the chime keys off summary
	// alone and fires exactly once per entry. `self` excludes the user's own
	// sent copy; DMs are elevated by kind, highlights by the core's flag.
	if (
		view.soundEnabled &&
		p.self !== true &&
		(p.highlight === true || p.conv.kind === "dm")
	) {
		playAttention();
	}
	// Derive unread from focus; highlight is the core's per-message signal. A
	// summary is the only activity signal for a conversation at summary interest
	// (no bodies).
	if (!isConvFocused(view, session, conv.key)) {
		let dirty = false;
		if (!conv.unread) {
			conv.unread = true;
			dirty = true;
		}
		if (p.highlight === true && !conv.highlight) {
			conv.highlight = true;
			dirty = true;
		}
		if (dirty) {
			store.unreadRev++;
		}
	}
	if (p.title !== undefined && p.title !== "" && conv.title !== p.title) {
		conv.title = p.title;
		store.conversationsRev++;
	}
	conv.lastActivity = Math.max(conv.lastActivity, Date.parse(p.lastActivity) || 0);
}

function applyFriends(store: Store, friends: MemberInfo[]): void {
	// Friends/bookmarks are account-wide: the broker de-duplicates them to one
	// event, so the store holds a single set rather than one per session.
	const list = [...friends].sort((a, b) => a.name.localeCompare(b.name));
	store.friends = list;
	for (const f of friends) {
		applyPresence(
			store,
			{
				character: f.name,
				gender: f.gender,
				status: f.status,
				statusMsg: f.statusMsg,
				admin: f.admin,
				online: f.online,
			},
			true,
		);
	}
}

// applyIgnores replaces the account-wide ignore list (IGN init/list and
// add/delete deltas both arrive as full set-to lists). Like friends, it is
// applied to every session.
function applyIgnores(store: Store, ignores: string[]): void {
	store.ignores = [...ignores];
}

function applySessionStateValue(
	store: Store,
	character: string,
	p:
		| {
				state: SessionSnapshot["state"];
				reason?: string;
				severity?: SessionSnapshot["severity"];
				autoRetry?: boolean;
		  }
		| undefined,
): void {
	if (p === undefined) {
		return;
	}
	const existing = store.sessions[character];
	if (existing !== undefined) {
		existing.state = p.state;
		existing.reason = p.reason;
		existing.severity = p.severity;
		existing.autoRetry = p.autoRetry;
		return;
	}
	store.sessions[character] = {
		character,
		state: p.state,
		reason: p.reason,
		severity: p.severity,
		autoRetry: p.autoRetry,
		self: { character, online: false, admin: false },
		adCount: 0,
		conversations: [],
		invites: [],
	};
}
