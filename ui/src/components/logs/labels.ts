// labels.ts — the log browser's pure kind labels, shared by the picker, the
// export panel, and the cleanup cards.

import type { LogConvKind } from "../../transport/protocol.js";

/** logKindLabel is a conversation kind's short display label. `channel` is the
 * reverse-lookup pseudo-kind; the concrete kinds come back separately. */
export function logKindLabel(kind: LogConvKind | "channel"): string {
	return LABELS[kind];
}

const LABELS: Record<LogConvKind | "channel", string> = {
	official: "Channel",
	room: "Room",
	dm: "DM",
	channel: "Channel/Room",
};