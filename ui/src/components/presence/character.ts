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
// Sister to RosterCharacter: the state and attrs mirror it, `row` folds the
// interactive element into the root (`button` with `data-character`), and
// render.pure skips the subtree while the record is referentially unchanged.

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
	/** row renders the root as a <button> row, adding the delegated
	 * `data-character` the shared context menu resolves. */
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
		const inner = [
			m(Avatar, { name: c.name, size: AVATAR_SIZE }),
			m("span.featured-character-body", [
				m("span.featured-character-head", [
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
				]),
				m(
					"span.featured-character-status-msg",
					c.statusMsg !== undefined && c.statusMsg !== ""
						? m.trust(c.statusMsg)
						: statusTitle,
				),
			]),
		];
		// row mode owns the clickable element; the delegated handler on the
		// list resolves the character from data-character.
		if (attrs.row === true) {
			return m(
				"button.featured-character.featured-row",
				{ type: "button", class: attrs.class, "data-character": c.name },
				inner,
			);
		}
		return m("span.featured-character", { class: attrs.class }, inner);
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
