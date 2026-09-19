// The downstream core<->client protocol. The boundary payloads and the enum /
// operation unions are generated from the Go definitions (cmd/tsgen) and
// re-exported here; field-level semantics live in the Go source, which is the
// source of truth. Everything declared in this file is a hand-written
// client-side type or helper.

export * from "./types.gen.js";
export * from "./enums.js";

import type * as Gen from "./types.gen.js";
import type { AccountStatus, EnvelopeType, Interest, Op } from "./enums.js";
import type { ConvRef, RoomAdminRequest } from "./types.gen.js";

/** Presence is the client's name for a delivered presence record. */
export type Presence = Gen.PresencePayload;

/** Envelope types. Server -> client, plus the client's "cmd". Request/response
 * reads (history, ads, presence search) are HTTP endpoints, not envelopes. */
export interface Envelope {
	t: EnvelopeType;
	cid?: string;
	d?: unknown;
}

export interface AccountState {
	status: AccountStatus;
	characters?: string[];
	reason?: string;
	/** persisted is true when a credential document is stored on the core and
	 * will be restored after a restart. The values are never sent. */
	persisted?: boolean;
}

/** convKey is the composite key "kind:id"; channel Kira and DM Kira never
 * collide. */
export function convKey(conv: ConvRef): string {
	return `${conv.kind}:${conv.id}`;
}

/** parseConvKey splits a composite key back into its ref. */
export function parseConvKey(key: string): ConvRef {
	const i = key.indexOf(":");
	if (i < 0) {
		return { kind: "official", id: key };
	}
	return { kind: key.slice(0, i) as ConvRef["kind"], id: key.slice(i + 1) };
}

/** A command sent to the core. Only fields relevant to the op are set.
 *
 * Command is deliberately not generated: `password`/`remember` are parsed off
 * the envelope by the web layer and never reach model.Command, so the Go struct
 * is the smaller internal shape, not the wire command. */
export interface Command {
	cid: string;
	op: Op;
	session?: string;
	character?: string;
	account?: string;
	password?: string;
	/** remember is set by set_credentials: persist the validated pair so the
	 * core can restore it after a restart. */
	remember?: boolean;
	conv?: ConvRef;
	body?: string;
	status?: string;
	statusMsg?: string;
	action?: string;
	level?: Interest;
	/** since is set by set_interest: the highest conv_seq the client already
	 * holds, so the core can resume full interest with a delta catch-up instead
	 * of a full re-materialization. */
	since?: number;
	/** tracked is set by set_tracked: show a DM in the conversation list. */
	tracked?: boolean;
	/** room is set by room_admin: the action and its parameters. */
	room?: RoomAdminRequest;
}

/** A command without a CID; the dispatcher assigns one. */
export type CommandInput = Omit<Command, "cid">;

let counter = 0;

/** nextCID returns a fresh command id. */
export function nextCID(): string {
	counter += 1;
	return `u-${counter}`;
}

/** isEnvelope reports whether a decoded value looks like an envelope. */
export function isEnvelope(value: unknown): value is Envelope {
	return (
		typeof value === "object" &&
		value !== null &&
		typeof (value as { t?: unknown }).t === "string"
	);
}