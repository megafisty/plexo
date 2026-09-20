// moderation.ts — the single authority model for room member moderation.
//
// One place decides who may op, deop, kick, ban, unban, or time out whom, and
// one factory (`roomOps`) turns those decisions into commands. The character
// context menu and the room management dialog both build a RoomOps for their
// (session, conversation), so the two surfaces cannot disagree about
// affordances. The F-Chat server stays the final authority; these rules only
// decide what the client offers.
//
// Authority, from docs/fchat.md: `add_mod`/`remove_mod` require the owner (or a
// global moderator); `kick`/`ban`/`unban`/`timeout` accept any room op. A plain
// room moderator may not act on the room owner or a global moderator; a global
// moderator may act on anyone. The owner name is not streamed, so callers that
// have fetched RoomInfo may pass it; `op` is already correct without it because
// the streamed `ops` set includes the owner.

import type { AppActions } from "../context.js";
import { isChannelKind } from "./conversations.js";
import type { Conversation, Store } from "../store/state.js";
import type {
	ConvKind,
	RoomAdminRequest,
	RoomRole,
} from "../transport/protocol.js";

/** MemberAction is one member-targeted moderation verb. */
export type MemberAction = "op" | "deop" | "kick" | "ban" | "unban" | "timeout";

/** RoomContextSource is the streamed conversation shape the authority model
 * reads. `Conversation` and the test builders satisfy it structurally. */
export interface RoomContextSource {
	kind: ConvKind;
	readOnly?: boolean;
	role?: RoomRole;
	members?: readonly string[];
	ops?: readonly string[];
}

/** RoomContext is the authority snapshot for one room/channel: the streamed
 * source plus the reporting session's identity, global-moderator flag, and (if
 * known) the owner name. */
export interface RoomContext {
	kind: ConvKind;
	readOnly?: boolean;
	role?: RoomRole;
	/** self is the reporting session's character name. */
	self: string;
	/** selfAdmin is true when the reporting character is a global moderator. */
	selfAdmin: boolean;
	members: readonly string[];
	/** ops is the room-op set; the core includes the owner. */
	ops: readonly string[];
	/** owner is the room owner, when a caller has fetched RoomInfo. */
	owner?: string;
}

/** RoomMember is the target of a member action. */
export interface RoomMember {
	name: string;
	/** admin is the target's global-moderator status (presence). */
	admin: boolean;
}

/** MemberCapabilities is the set of actions the context may perform on one
 * target. */
export interface MemberCapabilities {
	op: boolean;
	deop: boolean;
	kick: boolean;
	ban: boolean;
	unban: boolean;
	timeout: boolean;
}

/** RoomOpsResult is one trigger's outcome; `error` is the core's message. */
export interface RoomOpsResult {
	ok: boolean;
	error: string | null;
}

/** RoomOpsReporter observes each trigger so a caller can show a toast (menu) or
 * inline status (dialog) without the shared layer knowing either. */
export type RoomOpsReporter = (
	action: MemberAction,
	name: string,
	error: string | null,
) => void;

/** RoomOps is the shared interface: capability queries plus the triggers. The
 * triggers never mutate local state; the roster and role update from the core's
 * streamed events. */
export interface RoomOps {
	context: RoomContext;
	capabilities(target: RoomMember): MemberCapabilities;
	op(name: string): Promise<RoomOpsResult>;
	deop(name: string): Promise<RoomOpsResult>;
	kick(name: string): Promise<RoomOpsResult>;
	ban(name: string): Promise<RoomOpsResult>;
	unban(name: string): Promise<RoomOpsResult>;
	timeout(name: string, minutes: number): Promise<RoomOpsResult>;
}

const NO_CAPABILITIES: MemberCapabilities = {
	op: false,
	deop: false,
	kick: false,
	ban: false,
	unban: false,
	timeout: false,
};

/** fold normalizes a character name for comparison: F-Chat names are
 * case-insensitive. */
function fold(name: string): string {
	return name.toLowerCase();
}

/** includesName reports whether `names` contains `want`, case-insensitively. */
function includesName(names: readonly string[], want: string): boolean {
	const wantKey = fold(want);
	return names.some((name) => fold(name) === wantKey);
}

/** sourceOf lifts the streamed moderation fields out of a live Conversation. */
function sourceOf(conv: Conversation): RoomContextSource {
	return {
		kind: conv.conv.kind,
		readOnly: conv.readOnly,
		role: conv.role,
		members: conv.members,
		ops: conv.ops,
	};
}

/** roomContext builds the authority snapshot from a streamed conversation
 * source and the reporting session's identity. */
export function roomContext(
	source: RoomContextSource,
	self: string,
	selfAdmin: boolean,
	owner?: string,
): RoomContext {
	return {
		kind: source.kind,
		readOnly: source.readOnly,
		role: source.role,
		self,
		selfAdmin,
		members: source.members ?? [],
		ops: source.ops ?? [],
		owner,
	};
}

/** conversationContext builds a RoomContext for a live conversation, reading
 * the reporting session's identity and global-moderator flag from the store. */
export function conversationContext(
	store: Store,
	conv: Conversation,
): RoomContext {
	const snap = store.sessions[conv.session];
	return roomContext(sourceOf(conv), snap?.character ?? "", snap?.self.admin === true);
}

/** isChannel reports whether the conversation is a room or official channel,
 * the only kinds that carry room moderation. */
function isChannel(ctx: RoomContext): boolean {
	return isChannelKind(ctx.kind);
}

/** canModerate reports whether the context may perform member actions at all
 * (kick/ban/timeout/unban): a room op, the owner, or a global moderator. */
export function canModerate(ctx: RoomContext): boolean {
	return (
		isChannel(ctx) &&
		ctx.readOnly !== true &&
		(ctx.role === "mod" || ctx.role === "owner" || ctx.selfAdmin)
	);
}

/** canManageRoom reports whether the header should offer the room management
 * surface. It reflects true authority: a global moderator may manage any room,
 * with or without a room role. Official channels are excluded because the
 * management dialog is room-shaped (visibility, ownership). */
export function canManageRoom(ctx: RoomContext): boolean {
	return (
		ctx.kind === "room" &&
		ctx.readOnly !== true &&
		(ctx.role === "mod" || ctx.role === "owner" || ctx.selfAdmin)
	);
}

/** memberCapabilities resolves the actions the context may perform on one
 * target. Non-members can still be unbanned (the ban list is not membership). */
export function memberCapabilities(
	ctx: RoomContext,
	target: RoomMember,
): MemberCapabilities {
	if (!canModerate(ctx)) {
		return NO_CAPABILITIES;
	}
	const isSelf = fold(target.name) === fold(ctx.self);
	const isMember = includesName(ctx.members, target.name);
	const isOwner = ctx.owner !== undefined && fold(target.name) === fold(ctx.owner);
	const isOp = includesName(ctx.ops, target.name);
	// add_mod/remove_mod are owner-only verbs (or a global moderator).
	const ownerOrAdmin = ctx.role === "owner" || ctx.selfAdmin;
	// A plain room moderator may not act on the owner or a global moderator.
	const protectedTarget = isOwner || target.admin;
	const canAct = ctx.selfAdmin || !protectedTarget;
	return {
		op: ownerOrAdmin && !isSelf && isMember && !isOp,
		deop: ownerOrAdmin && !isSelf && isMember && isOp,
		kick: !isSelf && isMember && canAct,
		ban: !isSelf && isMember && canAct,
		unban: true,
		timeout: !isSelf && isMember && canAct,
	};
}

/** requestFor maps a capability to its wire command. `minutes` is required for
 * timeout and ignored otherwise. */
export function requestFor(
	action: MemberAction,
	character: string,
	minutes?: number,
): RoomAdminRequest {
	switch (action) {
		case "op":
			return { action: "add_mod", character };
		case "deop":
			return { action: "remove_mod", character };
		case "kick":
			return { action: "kick", character };
		case "ban":
			return { action: "ban", character };
		case "unban":
			return { action: "unban", character };
		case "timeout":
			return { action: "timeout", character, length: minutes };
	}
}

/** memberActionNotice is the confirmation toast for a successful member
 * action, shared by the character context menu and the character picker so the
 * two surfaces phrase an outcome identically. */
export function memberActionNotice(action: MemberAction, name: string): string {
	switch (action) {
		case "op":
			return `Added ${name} as a moderator.`;
		case "deop":
			return `Removed ${name} as a moderator.`;
		case "kick":
			return `Kicked ${name}.`;
		case "ban":
			return `Banned ${name}.`;
		case "unban":
			return `Unbanned ${name}.`;
		case "timeout":
			return `Timed out ${name}.`;
	}
}

/** roomOps builds the shared moderation interface for one (session,
 * conversation). `owner` should be RoomInfo.owner when the caller has it, so a
 * `deop` on the owner can be offered (see the file header). `onResult` is
 * invoked for every trigger, including a locally rejected timeout. */
export function roomOps(
	store: Store,
	actions: AppActions,
	session: string,
	conv: Conversation,
	onResult?: RoomOpsReporter,
	owner?: string,
): RoomOps {
	const snap = store.sessions[session];
	const context = roomContext(
		sourceOf(conv),
		snap?.character ?? "",
		snap?.self.admin === true,
		owner,
	);

	const apply = async (
		action: MemberAction,
		name: string,
		minutes?: number,
	): Promise<RoomOpsResult> => {
		const error = await actions.roomAdmin(
			session,
			conv.conv,
			requestFor(action, name, minutes),
		);
		if (onResult !== undefined) {
			onResult(action, name, error);
		}
		return { ok: error === null, error };
	};

	return {
		context,
		capabilities: (target) => memberCapabilities(context, target),
		op: (name) => apply("op", name),
		deop: (name) => apply("deop", name),
		kick: (name) => apply("kick", name),
		ban: (name) => apply("ban", name),
		unban: (name) => apply("unban", name),
		timeout: (name, minutes) => {
			if (!Number.isFinite(minutes) || minutes < 1) {
				const error = "Timeout must be at least one minute.";
				if (onResult !== undefined) {
					onResult("timeout", name, error);
				}
				return Promise.resolve({ ok: false, error });
			}
			return apply("timeout", name, minutes);
		},
	};
}