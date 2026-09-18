// HTTP helpers for the core's shared-password endpoints. F-Chat credentials
// never go through HTTP; they travel over the WebSocket. The request/response
// reads (history, ads, presence search) are HTTP too, so large or paginated
// payloads stay off the live socket.
import type {
	Ad,
	ConvRef,
	History,
	LogActivityDetail,
	LogActivityOverview,
	LogActivityScope,
	LogCleanupRequest,
	LogCleanupResult,
	LogConvRef,
	LogCoverage,
	LogSessionConv,
	MemberInfo,
	SearchMapping,
	SearchPayload,
	SearchQuery,
	Warpmark,
} from "./transport/protocol.js";

export interface SessionProbe {
	authRequired: boolean;
	authenticated: boolean;
}

const jsonAccept = { Accept: "application/json" };

/** REQUEST_TIMEOUT_MS bounds one HTTP request. A core that accepts the socket
 * but stalls must not pin a spinner forever; on timeout the request aborts and
 * the helper reports failure like any other transport error. */
const REQUEST_TIMEOUT_MS = 15000;

/** safeFetch performs one request with a timeout and never rejects. A network
 * error, an abort, or a DNS failure all become `{ response: null }`, so every
 * API function can honor its null/[]/false contract instead of throwing into a
 * floating `.then()` and stranding the caller's loading state. */
async function safeFetch(
	url: string,
	init?: RequestInit,
): Promise<{ response: Response | null }> {
	const controller = new AbortController();
	const timer = window.setTimeout(() => controller.abort(), REQUEST_TIMEOUT_MS);
	try {
		const response = await fetch(url, { ...init, signal: controller.signal });
		return { response };
	} catch {
		return { response: null };
	} finally {
		window.clearTimeout(timer);
	}
}

/** getJSON reads a JSON body, returning null for a transport error, a non-2xx
 * status, or a malformed body. */
async function getJSON<T>(url: string, init?: RequestInit): Promise<T | null> {
	const { response } = await safeFetch(url, init);
	if (response === null || !response.ok) {
		return null;
	}
	try {
		return (await response.json()) as T;
	} catch {
		return null;
	}
}

/** errorText reads the short rejection body, falling back to the status. The
 * read itself can fail on a torn connection, so it is guarded too. */
async function errorText(response: Response, fallback: string): Promise<string> {
	let text = "";
	try {
		text = (await response.text()).trim();
	} catch {
		// leave text empty and use the fallback below
	}
	return text !== "" ? text : fallback;
}

/** probeSession reports whether the core requires a password and whether this
 * browser is already authenticated (a persistent HttpOnly cookie). A failed
 * probe assumes open access so the app still loads. */
export async function probeSession(): Promise<SessionProbe> {
	const { response } = await safeFetch("/api/session", { headers: jsonAccept });
	if (response === null || !response.ok) {
		return { authRequired: false, authenticated: true };
	}
	try {
		return (await response.json()) as SessionProbe;
	} catch {
		return { authRequired: false, authenticated: true };
	}
}

/** loginSession submits the shared password; success sets the session cookie. */
export async function loginSession(password: string): Promise<boolean> {
	const { response } = await safeFetch("/api/session", {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ password }),
	});
	return response?.ok ?? false;
}

/** logoutSession clears the session cookie. */
export async function logoutSession(): Promise<void> {
	await safeFetch("/api/session", { method: "DELETE" });
}

/** HistoryQuery selects one history page; omit both cursors for the newest. */
export interface HistoryQuery {
	session: string;
	convKind: ConvRef["kind"];
	convId: string;
	beforeSeq?: number;
	afterSeq?: number;
	limit?: number;
}

/** fetchHistory pages a conversation's timeline over HTTP. */
export async function fetchHistory(q: HistoryQuery): Promise<History | null> {
	const params = new URLSearchParams({
		session: q.session,
		conv_kind: q.convKind,
		conv_id: q.convId,
	});
	if (q.beforeSeq !== undefined) params.set("before_seq", String(q.beforeSeq));
	if (q.afterSeq !== undefined) params.set("after_seq", String(q.afterSeq));
	if (q.limit !== undefined) params.set("limit", String(q.limit));
	return getJSON<History>(`/api/history?${params}`, { headers: jsonAccept });
}

// --- warpmarks (HTTP) ---

/** fetchWarpmarks lists one character's marks, newest first, with snippets. */
export async function fetchWarpmarks(
	session: string,
): Promise<Warpmark[] | null> {
	const params = new URLSearchParams({ session });
	const body = await getJSON<{ warpmarks?: Warpmark[] }>(
		`/api/warpmarks?${params}`,
		{ headers: jsonAccept },
	);
	return body === null ? null : (body.warpmarks ?? []);
}

/** createWarpmark creates or replaces a mark on one entry. */
export async function createWarpmark(
	session: string,
	entryId: string,
	label: string,
): Promise<boolean> {
	const { response } = await safeFetch("/api/warpmarks", {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ session, entryId, label }),
	});
	return response?.ok ?? false;
}

/** deleteWarpmark removes a mark; a missing mark is not an error. */
export async function deleteWarpmark(
	session: string,
	entryId: string,
): Promise<boolean> {
	const params = new URLSearchParams({ session, entryId });
	const { response } = await safeFetch(`/api/warpmarks?${params}`, {
		method: "DELETE",
	});
	return response?.ok ?? false;
}

// --- chatlog browser/export ---

/** fetchLogCharacters lists own characters with persisted history. Returns null
 * on failure so the caller can distinguish an error from an empty log. */
export async function fetchLogCharacters(): Promise<string[] | null> {
	const body = await getJSON<{ characters?: string[] }>("/api/logs/index", {
		headers: jsonAccept,
	});
	return body === null ? null : (body.characters ?? []);
}

/** fetchAllLogConversations lists every conversation across all characters, for
 * the conversation field before a character is chosen. */
export async function fetchAllLogConversations(): Promise<LogConvRef[] | null> {
	const body = await getJSON<{ conversations?: LogConvRef[] }>(
		"/api/logs/index?conversations=1",
		{ headers: jsonAccept },
	);
	return body === null ? null : (body.conversations ?? []);
}

/** fetchLogConversations lists one character's conversations with history. */
export async function fetchLogConversations(
	session: string,
): Promise<LogConvRef[] | null> {
	const params = new URLSearchParams({ session });
	const body = await getJSON<{ conversations?: LogConvRef[] }>(
		`/api/logs/index?${params}`,
		{ headers: jsonAccept },
	);
	return body === null ? null : (body.conversations ?? []);
}

/** fetchLogSessions resolves a conversation back to the own characters with
 * history in it. */
export async function fetchLogSessions(
	kind: LogConvRef["kind"],
	id: string,
): Promise<LogSessionConv[] | null> {
	const params = new URLSearchParams({ conv_kind: kind, conv_id: id });
	const body = await getJSON<{ characters?: LogSessionConv[] }>(
		`/api/logs/index?${params}`,
		{ headers: jsonAccept },
	);
	return body === null ? null : (body.characters ?? []);
}

/** fetchLogCoverage reads one conversation's persisted span. */
export async function fetchLogCoverage(
	session: string,
	kind: LogConvRef["kind"],
	id: string,
): Promise<LogCoverage | null> {
	const params = new URLSearchParams({
		session,
		conv_kind: kind,
		conv_id: id,
	});
	return getJSON<LogCoverage>(`/api/logs/coverage?${params}`, {
		headers: jsonAccept,
	});
}

/** fetchLogActivityOverview reads a conversation's per-local-day message counts.
 * The core resolves the span from coverage and the scope from `scope` ("auto"
 * by default). */
export async function fetchLogActivityOverview(
	session: string,
	kind: LogConvRef["kind"],
	id: string,
	tz: number,
	scope: LogActivityScope | "auto" = "auto",
): Promise<LogActivityOverview | null> {
	const params = new URLSearchParams({
		session,
		conv_kind: kind,
		conv_id: id,
		tz: String(tz),
		scope,
	});
	return getJSON<LogActivityOverview>(`/api/logs/activity?${params}`, {
		headers: jsonAccept,
	});
}

/** fetchLogActivityDetail segments one bounded range into activity sessions. */
export async function fetchLogActivityDetail(
	session: string,
	kind: LogConvRef["kind"],
	id: string,
	tz: number,
	fromMs: number,
	toMs: number,
	scope: LogActivityScope | "auto" = "auto",
): Promise<LogActivityDetail | null> {
	const params = new URLSearchParams({
		session,
		conv_kind: kind,
		conv_id: id,
		tz: String(tz),
		from: String(fromMs),
		to: String(toMs),
		scope,
	});
	return getJSON<LogActivityDetail>(`/api/logs/activity?${params}`, {
		headers: jsonAccept,
	});
}

/** logExportURL builds the self-contained artifact URL. `tz` is the display
 * offset in minutes east of UTC; the caller opens it in a new tab. */export function logExportURL(
	ref: { session: string; kind: LogConvRef["kind"]; id: string },
	fromMs: number,
	toMs: number,
	tz: number,
): string {
	const params = new URLSearchParams({
		session: ref.session,
		conv_kind: ref.kind,
		conv_id: ref.id,
		from: String(fromMs),
		to: String(toMs),
		tz: String(tz),
	});
	return `/api/logs/export?${params}`;
}

/** LogCleanupResponse is a cleanup answer: the impact, or a server message. */
export type LogCleanupResponse =
	| { ok: true; result: LogCleanupResult }
	| { ok: false; error: string };

/** postLogCleanup previews (apply false) or performs (apply true) one cleanup
 * rule. A rejection returns the server's short text, which is surfaced verbatim. */
export async function postLogCleanup(
	req: LogCleanupRequest,
): Promise<LogCleanupResponse> {
	const { response } = await safeFetch("/api/logs/cleanup", {
		method: "POST",
		headers: { "Content-Type": "application/json", Accept: "application/json" },
		body: JSON.stringify(req),
	});
	if (response === null) {
		return { ok: false, error: "Could not reach the core." };
	}
	if (response.ok) {
		try {
			return { ok: true, result: (await response.json()) as LogCleanupResult };
		} catch {
			return { ok: false, error: "The core returned an invalid response." };
		}
	}
	return {
		ok: false,
		error: await errorText(response, `Cleanup failed (${response.status}).`),
	};
}

/** fetchAds returns a session's buffered LRP ads. */
export async function fetchAds(session: string): Promise<Ad[]> {
	const params = new URLSearchParams({ session });
	return (await getJSON<Ad[]>(`/api/ads?${params}`, { headers: jsonAccept })) ?? [];
}

/** PresenceQuery filters the online roster search. */
export interface PresenceQuery {
	query?: string;
	gender?: string;
	status?: string;
	limit?: number;
}

/** fetchPresence searches a session's online roster over HTTP. */
export async function fetchPresence(session: string, q: PresenceQuery = {}): Promise<MemberInfo[]> {
	const params = new URLSearchParams({ session });
	if (q.query) params.set("q", q.query);
	if (q.gender) params.set("gender", q.gender);
	if (q.status) params.set("status", q.status);
	if (q.limit !== undefined) params.set("limit", String(q.limit));
	return (
		(await getJSON<MemberInfo[]>(`/api/presence?${params}`, {
			headers: jsonAccept,
		})) ?? []
	);
}

// --- character search (FKS) ---

/** mappingCache holds the fetched mapping for the process lifetime. The mapping
 * is core-wide and loaded once at core start, so refetching on every dialog
 * open would only add latency. A failed load is not cached, so reopening
 * retries (the endpoint is 503 until the core's load lands). */
let mappingCache: SearchMapping | null = null;

/** fetchMapping returns the core's search field mapping, or null when it is not
 * available yet. */
export async function fetchMapping(): Promise<SearchMapping | null> {
	if (mappingCache !== null) {
		return mappingCache;
	}
	const body = await getJSON<SearchMapping>("/api/mapping", {
		headers: jsonAccept,
	});
	if (body === null) {
		return null;
	}
	mappingCache = body;
	return mappingCache;
}

/** postSearch queues an FKS on a session. The result set is cached on the core
 * session and announced as a `search` notice, so this only reports whether the
 * core accepted the trigger. */
export async function postSearch(
	session: string,
	query: SearchQuery,
): Promise<SaveResult> {
	const { response } = await safeFetch(
		`/api/search?session=${encodeURIComponent(session)}`,
		{
			method: "POST",
			headers: {
				"Content-Type": "application/json",
				Accept: "application/json",
			},
			body: JSON.stringify(query),
		},
	);
	if (response === null) {
		return { ok: false, error: "Could not reach the core." };
	}
	if (response.ok) {
		return { ok: true };
	}
	return {
		ok: false,
		error: await errorText(response, `Search failed (${response.status}).`),
	};
}

/** fetchSearchResults pulls a session's cached FKS result set. It returns null
 * on failure (unknown session, not authenticated), leaving the store's last
 * known results untouched. */
export async function fetchSearchResults(
	session: string,
): Promise<SearchPayload | null> {
	return getJSON<SearchPayload>(
		`/api/search?session=${encodeURIComponent(session)}`,
		{ headers: jsonAccept },
	);
}

// --- settings ---

/** JoinTarget is one auto-join entry: an official channel or a room. */
export interface JoinTarget {
	kind: "official" | "room";
	id: string;
	name?: string;
}

/** GlobalSettings is the account-wide configuration document. */
export interface GlobalSettings {
	password?: string;
}

/** AutoStatus is a status the core applies after login. The message is raw
 * BBCode. */
export interface AutoStatus {
	status: string;
	message?: string;
}

/** CharacterSettings is one character's configuration document. */
export interface CharacterSettings {
	highlights?: string[];
	autoJoin?: JoinTarget[];
	autoStatus?: AutoStatus;
}

/** SettingsResponse is the core's answer to GET /api/settings. The two scopes
 * are disjoint; hasGlobal/hasCharacter distinguish an absent document from one
 * whose fields are all empty. */
export interface SettingsResponse {
	global: GlobalSettings;
	character: CharacterSettings;
	hasGlobal: boolean;
	hasCharacter: boolean;
}

/** fetchSettings reads the global document and, when a session is given, that
 * character's document. Returns null on any transport or HTTP error. */
export async function fetchSettings(session?: string): Promise<SettingsResponse | null> {
	const query =
		session !== undefined && session !== ""
			? `?session=${encodeURIComponent(session)}`
			: "";
	return getJSON<SettingsResponse>(`/api/settings${query}`, {
		headers: jsonAccept,
	});
}

/** SaveResult reports a settings write: ok, or a server-provided message. */
export interface SaveResult {
	ok: boolean;
	error?: string;
}

/** sendSettings writes one settings route. PUT sends a document; DELETE resets
 * the scope. The settings API answers 204 on success and a short text body on
 * rejection, which is surfaced verbatim. */
async function sendSettings(
	method: "PUT" | "DELETE",
	url: string,
	body?: unknown,
): Promise<SaveResult> {
	const init: RequestInit = { method };
	if (body !== undefined) {
		init.headers = {
			"Content-Type": "application/json",
			Accept: "application/json",
		};
		init.body = JSON.stringify(body);
	}
	const { response } = await safeFetch(url, init);
	if (response === null) {
		return { ok: false, error: "Could not reach the core." };
	}
	if (response.ok) {
		return { ok: true };
	}
	return {
		ok: false,
		error: await errorText(response, `Request failed (${response.status}).`),
	};
}

/** putGlobalSettings replaces the account-wide document. */
export function putGlobalSettings(g: GlobalSettings): Promise<SaveResult> {
	return sendSettings("PUT", "/api/settings/global", g);
}

/** resetGlobalSettings deletes the account-wide document. */
export function resetGlobalSettings(): Promise<SaveResult> {
	return sendSettings("DELETE", "/api/settings/global");
}

/** putCharacterSettings replaces one character's document. */
export function putCharacterSettings(
	session: string,
	c: CharacterSettings,
): Promise<SaveResult> {
	return sendSettings(
		"PUT",
		`/api/settings/character?session=${encodeURIComponent(session)}`,
		c,
	);
}

/** resetCharacterSettings deletes one character's document. */
export function resetCharacterSettings(session: string): Promise<SaveResult> {
	return sendSettings(
		"DELETE",
		`/api/settings/character?session=${encodeURIComponent(session)}`,
	);
}