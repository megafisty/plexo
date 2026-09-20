// format.ts — the composer's BBCode format palettes. Unlike the other shells
// they are opened by the composer, not shortcuts.ts: a chord inside the field
// asks MessageEditor to open one and hands it the closure that wraps the
// current selection. That closure rides on the command modal and reaches the
// list through CommandContext.format, so the palettes never hold a textarea.
//
// `format-marks` (Ctrl/Cmd-S) is a flat list of the non-parameterized marks.
// `format-advanced` (Ctrl/Cmd-D, with Ctrl/Cmd-U as the url mnemonic) is the
// parameterized family: a root list whose rows are subcommands, drilled with
// the same current/previous swap as the main command menu. Colors, URL, and
// Link Character are the subcommands; the URL and exact-name lists are
// free-text, using the palette input as the value.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useActions, useDispatch, useStore, useView } from "../../context.js";
import { loggedInNames, seenOnlineNames } from "../../lib/characters.js";
import { isUrl } from "../../lib/format.js";
import { request } from "../../render.js";
import { closeCommand } from "../../store/state.js";
import { FeaturedCharacter, RosterCharacter } from "../presence/character.js";
import { Palette } from "../primitives/palette.js";
import {
	type CommandAttrs,
	type CommandContext,
	type CommandItem,
	type CommandList,
} from "./list.js";

/** FormatTag is a row's precomputed BBCode request. */
interface FormatTag {
	tag: string;
	param: boolean;
	value?: string;
}

/** applyChoice applies one row's tag through the modal's closure, if present. */
function applyChoice(
	item: CommandItem<FormatTag>,
	context: CommandContext,
): void {
	const value = item.value;
	if (value === undefined) {
		return;
	}
	context.format?.(value.tag, value.param, value.value);
}

// ==========================================================================
// marks (format-marks)
// ==========================================================================

/** FormatMarksList wraps the selection in one of the non-parameterized text
 * marks. Each row carries the tag/param pair; the apply closure comes from the
 * modal through the context. */
export const FormatMarksList: CommandList<FormatTag> = {
	id: "format-marks",
	placeholder: "Format text",
	emptyText: "No formats",
	list: () => [
		{
			id: "sub",
			// Plain label, an arrow, then the tag applied, so sub and sup are
			// distinguishable at a glance.
			title: ["Subscript → ", m.trust("<sub>Subscript</sub>")],
			filterable: "Subscript sub",
			value: { tag: "sub", param: false },
		},
		{
			id: "sup",
			title: ["Superscript → ", m.trust("<sup>Superscript</sup>")],
			filterable: "Superscript sup",
			value: { tag: "sup", param: false },
		},
		{
			id: "s",
			title: ["Strikethrough → ", m.trust("<s>Strikethrough</s>")],
			filterable: "Strikethrough strike s",
			value: { tag: "s", param: false },
		},
		{
			id: "u",
			title: ["Underline → ", m.trust("<u>Underline</u>")],
			filterable: "Underline underline u",
			value: { tag: "u", param: false },
		},
	],
	onSelect: applyChoice,
};

// ==========================================================================
// advanced format (format-advanced)
// ==========================================================================

/** NAMED_COLORS is the fixed set of CSS color names offered by the color
 * subcommand. The renderer validates a named parameter as letters only; this is
 * the curated subset, so the picker cannot offer a typo. */
const NAMED_COLORS: readonly string[] = [
	"red",
	"orange",
	"yellow",
	"green",
	"cyan",
	"purple",
	"blue",
	"pink",
	"black",
	"brown",
	"white",
	"gray",
];

/** ColorList wraps the selection in a named color. Each row's value is the
 * `color` tag with its parameter; the sample is shown as full blocks in that
 * color followed by the neutral name, so the row reads as a swatch. */
export const ColorList: CommandList<FormatTag> = {
	id: "format-colors",
	placeholder: "Color",
	emptyText: "No colors",
	list: () =>
		NAMED_COLORS.map((name) => ({
			id: `color:${name}`,
			title: [
				m.trust(`<span style="color:${name}">██</span>`),
				` ${name}`,
			],
			filterable: name,
			value: { tag: "color", param: true, value: name },
		})),
	onSelect: applyChoice,
};

/** URL_PREVIOUS is the URL list's context header when the selection is already
 * a URL. It names the palette input (the link text) instead of the row the
 * shell drilled in from. */
const URL_PREVIOUS: CommandItem<FormatTag> = {
	id: "link-text",
	title: "Link Text",
	description: "Add a link text to your URL.",
	filterable: "Link Text",
};

/** UrlList turns the selection into a link. It is the only free-text palette:
 * the input is the value, not a filter, and the rows stay pinned.
 *
 * When the selection is already a URL the input is the optional link text
 * ("Set Link Text" uses it as the body; "Just URL" uses the URL for both the
 * target and the body). Otherwise the input is the URL itself, with one option,
 * "Set URL", that uses the selection as the body. The URL is not validated:
 * anything typed becomes the target. */
export const UrlList: CommandList<FormatTag> = {
	id: "format-url",
	placeholder: "URL",
	emptyText: "Nothing to link",
	list: (context) => {
		if (!isUrl(context.selection ?? "")) {
			return [
				{
					id: "set-url",
					title: "Set URL",
					description: "Link the selection to the URL you type.",
					filterable: "Set URL",
					value: { tag: "url", param: true },
				},
			];
		}
		return [
			{
				id: "link-text",
				title: "Set Link Text",
				description: "Show the link text you type instead of the URL.",
				filterable: "Set Link Text",
				value: { tag: "url", param: true },
			},
			{
				id: "just-url",
				title: "Just URL",
				description: "Show the URL itself as the link text.",
				filterable: "Just URL",
				value: { tag: "url", param: true },
			},
		];
	},
	onSelect: (item, context) => {
		const selection = context.selection ?? "";
		if (item.id === "link-text") {
			// The selection is the URL; the typed input is the body.
			context.format?.("url", true, selection, item.input ?? "");
		} else if (item.id === "just-url") {
			context.format?.("url", true, selection, selection);
		} else if (item.id === "set-url") {
			// The selection is the link text; the typed input is the target.
			context.format?.("url", true, item.input ?? "", selection);
		}
	},
};

// ==========================================================================
// character links (character-link)
// ==========================================================================

/** characterLinkPrevious is the normalized item the character rows produce: the
 * link-style step shows it as its header and reads the name back from `input`. */
function characterLinkPrevious(name: string): CommandItem<FormatTag> {
	return {
		id: `link:${name}`,
		title: `Link: ${name}`,
		description: "choose link style",
		filterable: "",
		input: name,
	};
}

/** characterStyleRows are the two link representations, shared by the drilled-to
 * style list and the exact-name free-text list. */
function characterStyleRows(): CommandItem<FormatTag>[] {
	return [
		{
			id: "icon",
			title: "As Icon",
			description: "Embed the character's avatar.",
			filterable: "As Icon icon",
			value: { tag: "icon", param: false },
		},
		{
			id: "user",
			title: "As Link",
			description: "Link to the character's profile.",
			filterable: "As Link user",
			value: { tag: "user", param: false },
		},
	];
}

/** applyCharacterStyle applies `[icon]name[/icon]` or `[user]name[/user]`. */
function applyCharacterStyle(
	item: CommandItem<FormatTag>,
	context: CommandContext,
	name: string,
): void {
	const value = item.value;
	if (value === undefined) {
		return;
	}
	context.format?.(value.tag, value.param, value.value, name);
}

/** CharacterStyleList is the last step for a chosen character: it reads the name
 * from the previous (normalized) item and applies the chosen tag. */
export const CharacterStyleList: CommandList<FormatTag> = {
	id: "format-character-style",
	placeholder: "Link style",
	emptyText: "No styles",
	list: characterStyleRows,
	onSelect: (item, context) => {
		const name = context.previous?.input;
		if (name !== undefined) {
			applyCharacterStyle(item, context, name);
		}
	},
};

/** ExactNameList is the free-text source: the input is the character name and
 * the style rows apply it directly, so no further drill is needed. */
export const ExactNameList: CommandList<FormatTag> = {
	id: "format-character-exact",
	placeholder: "Character name",
	emptyText: "No styles",
	list: characterStyleRows,
	onSelect: (item, context) =>
		applyCharacterStyle(item, context, item.input ?? ""),
};

/** ownCharacterRow renders one of the account's characters, FeaturedCharacter
 * style, and drills into the link-style step. */
function ownCharacterRow(
	name: string,
	context: CommandContext,
): CommandItem<FormatTag> {
	return {
		id: name,
		title: m(FeaturedCharacter, {
			character: context.store.characters[name] ?? { name, online: false },
		}),
		filterable: name,
		next: CharacterStyleList,
		previous: characterLinkPrevious(name),
	};
}

/** MyCharactersList lists the account's own characters. */
export const MyCharactersList: CommandList<FormatTag> = {
	id: "format-character-mine",
	placeholder: "My characters",
	emptyText: "No characters on this account.",
	list: (context) =>
		[...(context.store.account.characters ?? [])]
			.sort((a, b) => a.localeCompare(b))
			.map((name) => ownCharacterRow(name, context)),
};

/** seenCharacterRow renders one seen character, RosterCharacter style, as the
 * Ctrl-K picker does. */
function seenCharacterRow(
	name: string,
	context: CommandContext,
): CommandItem<FormatTag> {
	return {
		id: name,
		title: m(RosterCharacter, {
			character: context.store.characters[name] ?? { name, online: false },
		}),
		filterable: name,
		next: CharacterStyleList,
		previous: characterLinkPrevious(name),
	};
}

/** SeenCharacterList lists the online characters the client has seen, exactly as
 * the Ctrl-K picker does: the user's own logged-in characters are excluded, and
 * the recently-closed-DM rows are left out. */
export const SeenCharacterList: CommandList<FormatTag> = {
	id: "format-character-seen",
	placeholder: "Characters in chat",
	emptyText: "No characters are online.",
	list: (context) => {
		const seen = seenOnlineNames(
			context.store.characters,
			loggedInNames(context.store.sessions),
		);
		return seen.map((name) => seenCharacterRow(name, context));
	},
};

/** CharacterSourceList offers the three ways to name a character. */
/** EXACT_ROW is the CharacterSourceList's exact-name row. It is defined once so
 * a selected-text shortcut can open the exact-name step with the same header. */
const EXACT_ROW: CommandItem<FormatTag> = {
	id: "exact",
	title: "Exact Name",
	description: "Type any character name.",
	filterable: "Exact Name",
	next: ExactNameList,
};

export const CharacterSourceList: CommandList<FormatTag> = {
	id: "format-character-source",
	placeholder: "Character source",
	emptyText: "No sources",
	list: () => [
		{
			id: "mine",
			title: "My Characters",
			description: "Link one of your own characters.",
			filterable: "My Characters mine own",
			next: MyCharactersList,
		},
		{
			id: "seen",
			title: "Characters in Chat",
			description: "Link a character you have seen online.",
			filterable: "Characters in Chat seen",
			next: SeenCharacterList,
		},
		EXACT_ROW,
	],
};

/** CHARACTER_LINK_ROW is the advanced root's character-link subcommand. */
const CHARACTER_LINK_ROW: CommandItem<FormatTag> = {
	id: "character-link",
	title: "Link Character",
	description: "Link a character as an icon or a profile link.",
	filterable: "Link Character icon user",
	next: CharacterSourceList,
};

/** COLORS_ROW and URL_ROW are the advanced root's subcommand rows. They are
 * defined once so the shell can also show one as the context header when a
 * toolbar button opens its list directly on init. */
const COLORS_ROW: CommandItem<FormatTag> = {
	id: "colors",
	title: "Colors",
	description: "Color the selection with a named color.",
	filterable: "Colors color",
	next: ColorList,
};

const URL_ROW: CommandItem<FormatTag> = {
	id: "url",
	title: "Make Link",
	description: "Wrap the selection in a link.",
	filterable: "Make Link link url",
	next: UrlList,
};

/** SPOILER_ROW is the advanced root's one direct leaf: a non-parameterized tag
 * that just wraps the selection, so it needs no further list. */
const SPOILER_ROW: CommandItem<FormatTag> = {
	id: "spoiler",
	title: "Spoiler",
	description: "Hide the selection behind a spoiler.",
	filterable: "Spoiler",
	value: { tag: "spoiler", param: false },
};

/** AdvancedFormatList is the parameterized palette's root: one subcommand per
 * parameterized tag family, plus the direct Spoiler leaf. */
export const AdvancedFormatList: CommandList<FormatTag> = {
	id: "format-advanced",
	placeholder: "Advanced format",
	emptyText: "No formats",
	list: () => [COLORS_ROW, URL_ROW, CHARACTER_LINK_ROW, SPOILER_ROW],
	onSelect: applyChoice,
};

// ==========================================================================
// shells
// ==========================================================================

/** FORMAT_HEADER is the display-only header the direct (non-drilling) marks
 * palette shows above its input. */
const FORMAT_HEADER: CommandItem<FormatTag> = {
	id: "format",
	title: "Format BBCode",
	filterable: "Format BBCode",
};

interface FormatShellState {
	query: string;
}

/** FormatShell is the flat marks palette. It renders only the shared Palette; a
 * leaf selection applies the tag through the modal's closure and closes. */
export const FormatShell: Mithril.Component<CommandAttrs> = {
	oninit: (vnode) => {
		(vnode.state as FormatShellState).query = "";
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const actions = useActions();
		const state = vnode.state as FormatShellState;
		const session = view.activeSession;
		const format = vnode.attrs.onFormat;
		if (session === null || format === undefined) {
			// The modal was opened without an apply closure (or the session
			// vanished); nothing to format, so render empty like the other shells.
			return null;
		}
		const context: CommandContext = {
			store,
			view,
			dispatch,
			actions,
			session,
			format,
			selection: vnode.attrs.selection,
		};
		return m(Palette, {
			list: FormatMarksList,
			context,
			query: state.query,
			minInput: 0,
			previousItem: FORMAT_HEADER,
			onQuery: (query: string) => {
				state.query = query;
			},
			onSelect: () => closeCommand(view),
			onClose: () => closeCommand(view),
		});
	},
};

/** ADVANCED_HEADER is the display-only header the advanced root shows before a
 * subcommand is chosen. */
const ADVANCED_HEADER: CommandItem<FormatTag> = {
	id: "advanced-format",
	title: "Advanced Format",
	filterable: "Advanced Format",
};

interface AdvancedFormatState {
	/** current is the list showing; a subcommand swaps it. */
	current: CommandList<FormatTag>;
	query: string;
	/** previous is the row a subcommand was opened from, shown as context. */
	previous: CommandItem<FormatTag> | undefined;
}

/** Opened is the shell state a transition adopts: the list to show, the context
 * header, and an optional pre-filled input. */
interface Opened {
	current: CommandList<FormatTag>;
	previous: CommandItem<FormatTag>;
	query?: string;
}

/** characterLinkShortcut returns the exact-name step when the composer already
 * has text selected: entering the character-link flow treats that text as the
 * character name, so it skips the source picker. Undefined otherwise. */
function characterLinkShortcut(selection: string | undefined): Opened | undefined {
	if (selection === undefined || selection === "") {
		return undefined;
	}
	return { current: ExactNameList, previous: EXACT_ROW, query: selection };
}

/** advancedStartList maps a `start` list id (a toolbar button's pre-loaded
 * mode) to the list the shell should open on, the root row that stands in as
 * its context header, and any input to pre-fill. It returns undefined to open
 * at the root. With a selection, the character-link start skips straight to the
 * exact-name step. Pure, so the mapping is unit-testable. */
export function advancedStartList(
	start: string | undefined,
	selection?: string,
): Opened | undefined {
	if (start === ColorList.id) {
		return { current: ColorList, previous: COLORS_ROW };
	}
	if (start === UrlList.id) {
		return { current: UrlList, previous: URL_ROW };
	}
	if (start === CharacterSourceList.id) {
		return (
			characterLinkShortcut(selection) ?? {
				current: CharacterSourceList,
				previous: CHARACTER_LINK_ROW,
			}
		);
	}
	return undefined;
}

/** AdvancedFormatShell is the parameterized palette. Like the main command menu
 * it owns a stack of CommandLists: a row carrying `next` swaps `current`, and
 * the chosen row (or its normalized `previous`) becomes the palette's
 * previous-item context. */
export const AdvancedFormatShell: Mithril.Component<CommandAttrs> = {
	oninit: (vnode) => {
		const state = vnode.state as AdvancedFormatState;
		state.query = "";
		// A toolbar button can ask to open directly on a sub-list (color, url, or
		// character link); the matching root row then stands in as the context
		// header, as if the user had drilled in from the root. With text already
		// selected, the character-link start skips to the exact-name step.
		const opened = advancedStartList(vnode.attrs.start, vnode.attrs.selection);
		if (opened === undefined) {
			state.current = AdvancedFormatList;
			state.previous = undefined;
		} else {
			state.current = opened.current;
			state.previous = opened.previous;
			state.query = opened.query ?? "";
		}
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const actions = useActions();
		const state = vnode.state as AdvancedFormatState;
		const session = view.activeSession;
		const format = vnode.attrs.onFormat;
		if (session === null || format === undefined) {
			return null;
		}
		const context: CommandContext = {
			store,
			view,
			dispatch,
			actions,
			session,
			format,
			selection: vnode.attrs.selection,
			previous: state.previous,
		};
		// The URL list repurposes the palette input: every row stays pinned and the
		// typed text becomes a link part. When the selection is already a URL the
		// input is the link text and the header names it; otherwise the input is
		// the URL and the drilled-in row stays as context. The exact-name list
		// repurposes the input the same way, as the character name.
		const urlMode = state.current === UrlList;
		const exactNameMode = state.current === ExactNameList;
		const linkTextMode = urlMode && isUrl(vnode.attrs.selection ?? "");
		// The palette goes in a single-element keyed fragment: a key only remounts
		// within a fragment, and remounting on a list swap is what resets the
		// highlight to the first row and clears the typed filter.
		return [
			m(Palette, {
				key: state.current.id,
				list: state.current,
				context,
				query: state.query,
				minInput: 0,
				freeText: urlMode || exactNameMode,
				placeholder: urlMode
					? linkTextMode
						? "Link text"
						: "URL"
					: undefined,
				previousItem: linkTextMode
					? URL_PREVIOUS
					: state.previous ?? ADVANCED_HEADER,
				onQuery: (query: string) => {
					state.query = query;
				},
				onSubcommand: (item: CommandItem<FormatTag>) => {
					if (item.next === undefined) {
						return;
					}
					// Choosing Link Character with text selected treats it as the
					// exact name and skips the source picker.
					const shortcut =
						item.id === CHARACTER_LINK_ROW.id
							? characterLinkShortcut(vnode.attrs.selection)
							: undefined;
					if (shortcut !== undefined) {
						state.current = shortcut.current;
						state.previous = shortcut.previous;
						state.query = shortcut.query ?? "";
						request();
						return;
					}
					// A row may normalize what the next step shows (character rows
					// produce a "Link: name" header).
					state.previous = item.previous ?? item;
					state.current = item.next;
					state.query = "";
					request();
				},
				onSelect: () => closeCommand(view),
				onClose: () => closeCommand(view),
			}),
		];
	},
};
