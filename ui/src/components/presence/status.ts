// status.ts — F-Chat status display metadata. A leaf shared by the roster, the
// sidebar, the presence menus, the command palettes, and the status dialog, so
// it must not import a component: the dialog itself lives in statusDialog.ts.

// ==========================================================================
// status.ts
// ==========================================================================
// F-Chat status display metadata, shared by the roster, the self status
// control, and the status dialog. STATUS_OPTIONS is the set a character may
// choose; `crown` is a secret admin-granted status that renders but is never
// selectable.
export interface StatusOption {
	value: string;
	label: string;
	mark: string;
}

/** STATUS_OPTIONS lists the selectable statuses, in menu order. */
export const STATUS_OPTIONS: readonly StatusOption[] = [
	{ value: "online", label: "Online", mark: "🟢" },
	{ value: "looking", label: "Looking", mark: "👀" },
	{ value: "away", label: "Away", mark: "🌙" },
	{ value: "busy", label: "Busy", mark: "🟠" },
	{ value: "dnd", label: "Do not disturb", mark: "⛔" },
	{ value: "idle", label: "Idle", mark: "💤" },
];

/** OFFLINE_MARK is shown for a character known to be offline regardless of the
 * status they held when they left. */
export const OFFLINE_MARK = "⚪";

const STATUS_MARK: Record<string, string> = {
	online: "🟢",
	looking: "👀",
	away: "🌙",
	busy: "🟠",
	dnd: "⛔",
	idle: "💤",
	crown: "🍰",
};

const STATUS_LABEL: Record<string, string> = {
	online: "Online",
	looking: "Looking",
	away: "Away",
	busy: "Busy",
	dnd: "Do not disturb",
	idle: "Idle",
	crown: "Rewarded",
};

// Status strings are a tiny, repeated set; normalize once and reuse the result
// so long lists do not allocate per render.
const marks = new Map<string, string>();
const labels = new Map<string, string>();

export function statusMark(status?: string): string {
	const key = (status ?? "").toLowerCase();
	let mark = marks.get(key);
	if (mark === undefined) {
		mark = STATUS_MARK[key] ?? "";
		marks.set(key, mark);
	}
	return mark;
}

export function statusLabel(status?: string): string {
	const key = (status ?? "").toLowerCase();
	let label = labels.get(key);
	if (label === undefined) {
		label = STATUS_LABEL[key] ?? status ?? "";
		labels.set(key, label);
	}
	return label;
}
