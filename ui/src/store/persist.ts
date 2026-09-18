// persist.ts — device-local persistence: browser preferences and composer
// drafts, both in localStorage and neither synced to the core. Absorbs
// device.ts and drafts.ts.


// ==========================================================================
// device.ts
// ==========================================================================
// Device-local preferences, persisted as one JSON document in localStorage.
// These are per-browser and never synced to the core; the "This Device" settings
// card edits them. The blob is versionless: unknown keys are preserved on write,
// and a missing or corrupt document falls back to the defaults below.
const KEY = "plexo:device";

export interface DevicePrefs {
	/** soundEnabled plays the attention sound for elevated messages. */
	soundEnabled: boolean;
	/** composerEnterNewline makes Enter insert a newline (Ctrl/Cmd+Enter sends)
	 * instead of sending. */
	composerEnterNewline: boolean;
}

const DEFAULTS: DevicePrefs = {
	soundEnabled: true,
	composerEnterNewline: false,
};

let cached: DevicePrefs | null = null;

/** devicePrefs returns the cached preferences, reading localStorage once. */
export function devicePrefs(): DevicePrefs {
	if (cached !== null) {
		return cached;
	}
	cached = { ...DEFAULTS };
	try {
		const raw = window.localStorage.getItem(KEY);
		if (raw !== null) {
			// Parse loosely: only known keys with the right type are adopted, so
			// a partial or hand-edited document still yields a valid shape.
			const parsed = JSON.parse(raw) as Partial<DevicePrefs>;
			if (typeof parsed.soundEnabled === "boolean") {
				cached.soundEnabled = parsed.soundEnabled;
			}
			if (typeof parsed.composerEnterNewline === "boolean") {
				cached.composerEnterNewline = parsed.composerEnterNewline;
			}
		}
	} catch {
		/* absent or corrupt: keep defaults */
	}
	return cached;
}

/** saveDevicePrefs merges a change into the document and writes it back. */
export function saveDevicePrefs(next: Partial<DevicePrefs>): void {
	const prefs = devicePrefs();
	Object.assign(prefs, next);
	try {
		window.localStorage.setItem(KEY, JSON.stringify(prefs));
	} catch {
		/* storage unavailable: the change applies for this session only */
	}
}

// ==========================================================================
// drafts.ts
// ==========================================================================
// Draft persistence: device-local durability for composer text, so a reload or
// crash never loses a long post. This mirrors the composer's View.drafts entry
// into localStorage under a per-conversation key; it is message content and
// never leaves the browser.
//
// Writes are throttled to once per INTERVAL_MS while typing (with a trailing
// flush), and flushed on blur/unmount, so a long burst costs at most one write
// per interval instead of one per keystroke. localStorage writes are synchronous
// (and can touch disk), so the interval is kept long enough that a netbook does
// not pay it on every half second of typing. Durability is best-effort: when
// storage is unavailable or full, the in-memory View copy still works.
const PREFIX = "plexo:draft:";
const INTERVAL_MS = 1000;

/** pending is the newest text not yet written; last is the time of the last
 * write; timer is the pending trailing flush. All three are per draft key. */
const pending: Record<string, string> = {};
const last: Record<string, number> = {};
const timers: Record<string, number> = {};

function storage(): Storage | null {
	try {
		return window.localStorage;
	} catch {
		return null;
	}
}

/** readDraft returns the persisted draft for a key, or undefined when none. */
export function readDraft(key: string): string | undefined {
	const s = storage();
	if (s === null) {
		return undefined;
	}
	try {
		const v = s.getItem(PREFIX + key);
		return v === null ? undefined : v;
	} catch {
		return undefined;
	}
}

/** writeDraft records the latest text and schedules a throttled write. */
export function writeDraft(key: string, text: string): void {
	pending[key] = text;
	const since = Date.now() - (last[key] ?? 0);
	if (since >= INTERVAL_MS) {
		flushDraft(key);
		return;
	}
	if (timers[key] === undefined) {
		timers[key] = window.setTimeout(() => flushDraft(key), INTERVAL_MS - since);
	}
}

/** flushDraft writes any pending text for a key immediately. Called on blur and
 * unmount so the trailing edge is never lost. */
export function flushDraft(key: string): void {
	if (timers[key] !== undefined) {
		window.clearTimeout(timers[key]);
		delete timers[key];
	}
	const text = pending[key];
	if (text === undefined) {
		return;
	}
	delete pending[key];
	last[key] = Date.now();
	const s = storage();
	if (s === null) {
		return;
	}
	try {
		if (text === "") {
			s.removeItem(PREFIX + key);
		} else {
			s.setItem(PREFIX + key, text);
		}
	} catch {
		/* quota/unavailable: durability is best-effort */
	}
}

/** deleteDraft drops a conversation's persisted draft (e.g. after sending). */
export function deleteDraft(key: string): void {
	if (timers[key] !== undefined) {
		window.clearTimeout(timers[key]);
		delete timers[key];
	}
	delete pending[key];
	const s = storage();
	if (s === null) {
		return;
	}
	try {
		s.removeItem(PREFIX + key);
	} catch {
		/* best-effort */
	}
}

/** flushAll writes every pending draft; wired to beforeunload so a reload in
 * the throttle window does not drop the trailing keystrokes. */
export function flushAll(): void {
	for (const key of Object.keys(pending)) {
		flushDraft(key);
	}
}

window.addEventListener("beforeunload", flushAll);
