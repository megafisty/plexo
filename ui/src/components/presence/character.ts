// character.ts — the character-rendering leaves: the clickable name, the
// roster row, and the avatar-forward featured row. Absorbs CharacterLink.ts,
// RosterCharacter.ts, and FeaturedCharacter.ts.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { genderClass } from "../../lib/characters.js";
import { pure } from "../../render.js";
import { OFFLINE_MARK, statusLabel, statusMark } from "./status.js";
import { Avatar } from "../primitives/Avatar.js";

// ==========================================================================
// CharacterLink.ts
// ==========================================================================
// CharacterLink: a clickable character name. Presentational — it renders the
// name with the gender color and carries `data-character` so a delegated
// handler (the shared character menu opener) can resolve it. It never reads the
// store: callers pass the gender. Used in message rows so the speaker is
// clickable like a roster entry.
//
// Gender is treated as static for the life of a message row: F-Chat does not
// change a character's gender while it is online, so a rendered color cannot go
// stale (a relog produces new rows with the new color).

export interface CharacterLinkAttrs {
	name: string;
	gender?: string;
	/** class is applied to the root button, for caller-owned layout. */
	class?: string;
}

export const CharacterLink: Mithril.Component<CharacterLinkAttrs> = {
	view: ({ attrs }) => {
		const cls = [attrs.class, genderClass(attrs.gender)]
			.filter((c) => c !== undefined && c !== "")
			.join(" ");
		return m(
			"button.character-link",
			{ type: "button", class: cls, "data-character": attrs.name },
			attrs.name,
		);
	},
};

// ==========================================================================
// RosterCharacter.ts
// ==========================================================================
// RosterCharacter: the canonical rendering of one character in a roster-like
// list. Surfaces online status as an emoji, gender as the name color, and an
// optional room/global moderator icon. Presentational only — callers own the
// surrounding list item and click behavior.
//
// `row` folds the interactive roster row into this component's root (`button`
// instead of `span`), so a roster row is `li > RosterCharacter` rather than
// `li > button > RosterCharacter`: one less vnode per member, which matters at
// 500+ members.
//
// Performance: callers pass the presence record straight from the store (no
// field-by-field attrs copy), the status/gender maps are looked up with a small
// cache, and render.pure skips the subtree while the attrs are unchanged.
// applyPresence replaces the record wholesale on any change, so reference
// equality is a correct "nothing changed" check.

/** RosterCharacterState is the minimal presence shape this component reads.
 * Both store.Character and transport.MemberInfo satisfy it, so callers pass
 * the state object through untouched. */
export interface RosterCharacterState {
	name: string;
	gender?: string;
	status?: string;
	online: boolean;
}

/** Moderator is a roster row's moderator mark: a room op, a global chat admin,
 * or none. */
export type Moderator = "room" | "global" | undefined;

/** moderatorFor resolves a row's moderator mark from global-admin status and
 * the room op set. Shared by the channel roster and the character picker so the
 * two lists mark the same character identically. */
export function moderatorFor(
	character: { admin?: boolean } | undefined,
	ops: ReadonlySet<string>,
	name: string,
): Moderator {
	if (character?.admin === true) {
		return "global";
	}
	if (ops.has(name)) {
		return "room";
	}
	return undefined;
}

/** CachedRow is one memoized row value plus the presence record and moderator
 * mark it was built from. */
interface CachedRow<V> {
	character: RosterCharacterState;
	moderator: Moderator;
	value: V;
}

/** RowCache memoizes per-name rendered values (roster row vnodes, palette rows)
 * against the presence record and moderator mark they were built from, and
 * hands out one stable placeholder record per presence-less member.
 *
 * Both properties are what make RosterCharacter's render.pure reference check
 * hold: the record handed to it must not change identity until it actually
 * changes, and a rebuilt row must be reused while its inputs are unchanged. The
 * equality check lives here, in one place, because it must match that contract
 * exactly.
 *
 * A cache is scoped to a render window; call prune() with the names still in
 * the window to drop values and placeholders that scrolled out. */
export class RowCache<V> {
	private rows = new Map<string, CachedRow<V>>();
	private placeholders = new Map<string, RosterCharacterState>();

	/** presenceOf returns the live record, or a stable placeholder for a member
	 * the registry has not reached yet. */
	presenceOf(
		name: string,
		known: RosterCharacterState | undefined,
	): RosterCharacterState {
		if (known !== undefined) {
			return known;
		}
		let record = this.placeholders.get(name);
		if (record === undefined) {
			record = { name, online: false };
			this.placeholders.set(name, record);
		}
		return record;
	}

	/** isStale reports whether the cached value for `name` was built from a
	 * different record or moderator mark, without rebuilding it. */
	isStale(
		name: string,
		character: RosterCharacterState,
		moderator: Moderator,
	): boolean {
		const cached = this.rows.get(name);
		return (
			cached === undefined ||
			cached.character !== character ||
			cached.moderator !== moderator
		);
	}

	/** value returns the cached value, rebuilding and re-caching it only when
	 * the presence record or moderator mark changed. */
	value(
		name: string,
		character: RosterCharacterState,
		moderator: Moderator,
		build: () => V,
	): V {
		const cached = this.rows.get(name);
		if (
			cached !== undefined &&
			cached.character === character &&
			cached.moderator === moderator
		) {
			return cached.value;
		}
		const value = build();
		this.rows.set(name, { character, moderator, value });
		return value;
	}

	/** prune drops cached values and placeholders for names not in `keep`, so a
	 * scrolled-out window does not grow the maps without bound. */
	prune(keep: ReadonlySet<string>): void {
		for (const name of this.rows.keys()) {
			if (!keep.has(name)) {
				this.rows.delete(name);
			}
		}
		for (const name of this.placeholders.keys()) {
			if (!keep.has(name)) {
				this.placeholders.delete(name);
			}
		}
	}

	/** clear empties the cache (a conversation switch or an empty roster). */
	clear(): void {
		this.rows.clear();
		this.placeholders.clear();
	}
}

export interface RosterCharacterAttrs {
	/** presence record, passed directly from state. */
	character: RosterCharacterState;
	/** moderator role, when known: a room op or a global chat admin. */
	moderator?: "room" | "global";
	/** extra class on the root element. */
	class?: string;
	/** row renders the root as a <button> roster row, adding the delegated
	 * `data-character` the shared context menu resolves. */
	row?: boolean;
}

function moderatorMark(role: "room" | "global"): Mithril.Vnode {
	const title = role === "global" ? "Global moderator" : "Room moderator";
	return m(
		"span.roster-character-mod",
		{ title, "aria-label": title },
		role === "global" ? "👑" : "🛡",
	);
}

const RawRosterCharacter: Mithril.Component<RosterCharacterAttrs> = {
	view: ({ attrs }) => {
		const c = attrs.character;
		const title = c.status ? statusLabel(c.status) : c.online ? "Online" : "Offline";
		const inner = [
			m(
				"span.roster-character-status",
				{
					title,
					"aria-label": c.online ? "Online" : "Offline",
				},
				c.online ? statusMark(c.status) : OFFLINE_MARK,
			),
			m(
				"span.roster-character-name",
				{ class: genderClass(c.gender) },
				c.name,
			),
			attrs.moderator !== undefined
				? moderatorMark(attrs.moderator)
				: null,
		];
		// row mode owns the clickable element; the delegated handler on the
		// list resolves the member from data-character.
		if (attrs.row === true) {
			return m(
				"button.roster-character.roster-row",
				{ type: "button", class: attrs.class, "data-character": c.name },
				inner,
			);
		}
		return m("span.roster-character", { class: attrs.class }, inner);
	},
};

/** RosterCharacter skips its subtree while the presence record, moderator mark,
 * class, and row mode are unchanged. */
export const RosterCharacter: Mithril.Component<RosterCharacterAttrs> = pure(
	RawRosterCharacter,
	(next, prev) =>
		next.character === prev.character &&
		next.moderator === prev.moderator &&
		next.class === prev.class &&
		next.row === prev.row,
);

// ==========================================================================
// FeaturedCharacter.ts
// ==========================================================================
// FeaturedCharacter: the avatar-forward sibling of RosterCharacter. Where the
// roster compresses a member to a status mark and a name, this renders a
// messenger-style row: a square avatar, the gender-colored name, and the
// character's status message on a second line. The message is ellipsized to one
// line, so a list of FeaturedCharacters keeps a uniform height no matter how
// long a status is; when a character has no message, the status label
// ("Looking", "Away", …) stands in.
//
// Sister to RosterCharacter: the state and attrs mirror it, `row` adds the
// delegated `data-character` activation, and render.pure skips the subtree
// while the record is referentially unchanged. Unlike the roster row, the
// root is a plain <span> in both modes: the status message is a display surface
// that may contain links, so the interactive element is the name header alone
// and a click on the status falls through to whatever it contains.

/** FeaturedCharacterState is the minimal presence shape this component reads.
 * store.Character satisfies it, so callers pass the record through untouched. */
export interface FeaturedCharacterState {
	name: string;
	gender?: string;
	status?: string;
	/** statusMsg is rendered HTML from the core, never raw BBCode. */
	statusMsg?: string;
	online: boolean;
}

export interface FeaturedCharacterAttrs {
	/** presence record, passed directly from state. */
	character: FeaturedCharacterState;
	/** extra class on the root element. */
	class?: string;
	/** row adds the delegated `data-character` activation to the name header,
	 * the element the shared character menu and profile opener resolve. The
	 * status message stays outside it so its links keep their own behavior. */
	row?: boolean;
}

/** AVATAR_SIZE matches the two-line text column. */
const AVATAR_SIZE = 40;

const RawFeaturedCharacter: Mithril.Component<FeaturedCharacterAttrs> = {
	view: ({ attrs }) => {
		const c = attrs.character;
		const statusTitle = c.status
			? statusLabel(c.status)
			: c.online
				? "Online"
				: "Offline";
		const head = m("span.featured-character-head", [
			m(
				"span.featured-character-status",
				{
					title: statusTitle,
					"aria-label": c.online ? "Online" : "Offline",
				},
				c.online ? statusMark(c.status) : OFFLINE_MARK,
			),
			m(
				"span.featured-character-name",
				{ class: genderClass(c.gender) },
				c.name,
			),
		]);
		// Row mode makes the name header the activation element, not the whole
		// row: the status message below it can carry links, and a <button> must
		// not wrap interactive content. The delegated handler resolves
		// data-character from the header, so a click on the status falls through.
		const main =
			attrs.row === true
				? m(
						"button.featured-character-main",
						{ type: "button", "data-character": c.name },
						head,
					)
				: head;
		// The avatar is a second, redundant mouse target for the same character:
		// hidden from assistive tech and out of the tab order so the name button
		// stays the single accessible control. The status message sits outside
		// both, so its links keep their own behavior.
		const avatar = m(Avatar, { name: c.name, size: AVATAR_SIZE });
		const inner = [
			attrs.row === true
				? m(
						"button.featured-character-avatar",
						{
							type: "button",
							"data-character": c.name,
							"tabindex": "-1",
							"aria-hidden": "true",
						},
						avatar,
					)
				: avatar,
			m("span.featured-character-body", [
				main,
				m(
					"span.featured-character-status-msg",
					c.statusMsg !== undefined && c.statusMsg !== ""
						? m.trust(c.statusMsg)
						: statusTitle,
				),
			]),
		];
		const cls = [
			"featured-character",
			attrs.row === true ? "featured-row" : "",
			attrs.class ?? "",
		]
			.filter((part) => part !== "")
			.join(" ");
		return m("span", { class: cls }, inner);
	},
};

/** FeaturedCharacter skips its subtree while the presence record, class, and row
 * mode are unchanged. */
export const FeaturedCharacter: Mithril.Component<FeaturedCharacterAttrs> =
	pure(RawFeaturedCharacter, (next, prev) =>
		next.character === prev.character &&
		next.class === prev.class &&
		next.row === prev.row,
	);
