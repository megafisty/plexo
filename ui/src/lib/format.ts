// format.ts — display formatting: clock/date conversion, composer byte
// counting, and conversation labels. Absorbs time.ts, text.ts, labels.ts.

import type { JoinTarget } from "../api.js";

// ==========================================================================
// time.ts
// ==========================================================================
// Time formatting shared by the timeline, the ad rows, and the log exporter.
// Pure and dependency-free.

/** pad2 zero-pads a number to two digits. */
export function pad2(n: number): string {
	return String(n).padStart(2, "0");
}

/** formatClock renders a timestamp as local "HH:MM". It accepts whatever
 * `new Date()` does (epoch ms or a parseable string), matching the two callers
 * (timeline entries carry epoch ms, ads carry an RFC3339 string). */
export function formatClock(value: number | string): string {
	const d = new Date(value);
	return `${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

/** toLocalInput formats an epoch instant as a datetime-local value in the
 * browser's timezone, the same clock the export renders with by default. */
export function toLocalInput(ms: number): string {
	const d = new Date(ms);
	return `${d.getFullYear()}-${pad2(d.getMonth() + 1)}-${pad2(d.getDate())}T${pad2(d.getHours())}:${pad2(d.getMinutes())}`;
}

/** fromLocalInput parses a datetime-local value as a local instant (epoch ms),
 * or null when empty/invalid. */
export function fromLocalInput(value: string): number | null {
	if (value === "") {
		return null;
	}
	const ms = Date.parse(value);
	return Number.isNaN(ms) ? null : ms;
}

/** formatOffset renders a minutes-east offset as "UTC+HH:MM". */
export function formatOffset(minutesEast: number): string {
	const sign = minutesEast < 0 ? "-" : "+";
	const abs = Math.abs(minutesEast);
	return `UTC${sign}${pad2(Math.floor(abs / 60))}:${pad2(abs % 60)}`;
}

// ==========================================================================
// text.ts
// ==========================================================================
// Text measurement helpers for the composer's byte counter. Pure and
// dependency-free.

/** utf8Bytes counts a string as Go's len() would: bytes, not UTF-16 units. The
 * server limit is bytes, and TextEncoder is not guaranteed on QtWebKit. */
export function utf8Bytes(s: string): number {
	let n = 0;
	for (let i = 0; i < s.length; i++) {
		const c = s.charCodeAt(i);
		if (c < 0x80) {
			n += 1;
		} else if (c < 0x800) {
			n += 2;
		} else if (c >= 0xd800 && c <= 0xdbff) {
			n += 4;
			i++; // surrogate pair: count once
		} else {
			n += 3;
		}
	}
	return n;
}

/** countLabel formats the byte counter, omitting the limit until the server has
 * reported one. */
export function countLabel(bytes: number, limit: number): string {
	if (limit <= 0) {
		return bytes === 0 ? "" : `${bytes}`;
	}
	return `${bytes} / ${limit}`;
}

/** overLimit reports whether the trimmed draft is over a known byte limit. */
export function overLimit(value: string, limit: number): boolean {
	return limit > 0 && utf8Bytes(value.trim()) > limit;
}

// ==========================================================================
// labels.ts
// ==========================================================================
// Conversation label helpers. Pure; shared by the settings editor and the
// conversation lists.

/** convLabel is a conversation's human label: its display name when known,
 * otherwise its raw id. */
export function convLabel(name: string | undefined, id: string): string {
	return name !== undefined && name !== "" ? name : id;
}

/** joinLabel is an auto-join entry's human label: its display name when known,
 * otherwise its raw id. */
export function joinLabel(entry: JoinTarget): string {
	return convLabel(entry.name, entry.id);
}
