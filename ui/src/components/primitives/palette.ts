// palette.ts — the command palette primitive: a filterable, keyboard-driven
// overlay list. It is presentational only: it renders an input, a bounded
// result list (a title line, an optional muted description line, and an
// optional subcommand chevron per row), and a transparent backdrop, and it
// reports the query and the selected item through callbacks. It owns no data
// and performs no action; an invisible shell (components/commands/) supplies the
// items and decides what a selection means.
//
// The shell owns the committed query. The palette keeps the live input value
// locally and debounces it before calling `onQuery`, so the list updates while
// typing without a store write per keystroke. A `query` change from outside
// resets the input, which lets a shell swap its item set (subcommands) cleanly.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { debounce, type Debounced } from "../../lib/debounce.js";
import { request } from "../../render.js";
import { useEscape, type EscapeState } from "./dialog.js";

/** PALETTE_DEBOUNCE_MS is the quiet period before the typed query reaches the
 * shell. */
const PALETTE_DEBOUNCE_MS = 120;

/** PaletteItem is one row's data. `title` and `description` are plain strings
 * or prebuilt Mithril content -- pass a component as `m(Component, attrs)` --
 * so a shell can supply rich rows without a per-item render callback.
 * `subcommand` marks a row that will open a further palette when chosen; it
 * adds a right chevron. `value` is an optional precomputed result the shell
 * reads back off the chosen item in `onSelect`. */
export interface PaletteItem<R = unknown> {
	/** id is the row's stable identity (Mithril key and a11y id). */
	id: string;
	title: Mithril.Children;
	description?: Mithril.Children;
	subcommand?: boolean;
	/** value is an optional precomputed result a shell can attach for later
	 * handling; it is passed back untouched on the item given to onSelect. */
	value?: R;
}

export interface PaletteAttrs<R = unknown> {
	/** items are the rows to show, already filtered by the shell for `query`. */
	items: ReadonlyArray<PaletteItem<R>>;
	/** query is the committed filter text, owned by the shell. The palette keeps
	 * the live input value locally and debounces `onQuery`; setting this from
	 * outside (e.g. a shell clearing it for a new item set) resets the input. */
	query: string;
	/** minInput is the query length below which results are hidden and the
	 * prompt is shown (default 0). */
	minInput?: number;
	placeholder?: string;
	/** promptText is shown while the query is shorter than minInput. */
	promptText?: string;
	/** emptyText is shown when the query meets minInput but nothing matches. */
	emptyText?: string;
	/** onQuery receives the debounced query text. */
	onQuery: (query: string) => void;
	/** onSelect receives the chosen item itself, so the shell can read its `id`,
	 * its optional `value`, or any other field; the shell decides whether to
	 * close or to replace the item set (subcommands). */
	onSelect: (item: PaletteItem<R>) => void;
	/** onClose is called for Escape and a click outside the palette. */
	onClose: () => void;
}

interface PaletteState extends EscapeState {
	/** raw is the live input text; it leads the debounced `query`. */
	raw: string;
	/** lastQuery is the last `attrs.query` seen, so an external change (a shell
	 * reset) is distinguishable from an echo of our own emit. */
	lastQuery: string;
	/** active is the highlighted row index; -1 when no row is highlighted. */
	active: number;
	/** listId names the listbox for aria-controls / aria-activedescendant. */
	listId: string;
	/** timer is the trailing debounce that delivers the query to the shell. */
	timer: Debounced;
}

let paletteSeq = 0;

/** escapeHook closes the palette on Escape from anywhere on the page, not only
 * while the input has focus. */
const escapeHook = useEscape<PaletteAttrs<any>, PaletteState>(
	(vnode) => () => vnode.attrs.onClose(),
);

/** Palette is the reusable command-palette primitive. */
export const Palette: Mithril.Component<PaletteAttrs<any>, PaletteState> = {
	...escapeHook,
	oninit: (vnode) => {
		const state = vnode.state as unknown as PaletteState;
		paletteSeq += 1;
		state.listId = `palette-${paletteSeq}`;
		state.raw = vnode.attrs.query;
		state.lastQuery = vnode.attrs.query;
		state.active = 0;
		state.timer = debounce(PALETTE_DEBOUNCE_MS);
	},
	onbeforeupdate: (vnode) => {
		const state = vnode.state as unknown as PaletteState;
		// An external `query` change re-seeds the input. Our own debounced emit
		// sets lastQuery first, so the echoed value is not mistaken for a reset.
		if (vnode.attrs.query !== state.lastQuery) {
			state.timer.cancel();
			state.raw = vnode.attrs.query;
			state.lastQuery = vnode.attrs.query;
			state.active = 0;
		}
	},
	onbeforeremove: (vnode) => {
		(vnode.state as unknown as PaletteState).timer.cancel();
	},
	view: (vnode) => {
		const state = vnode.state as unknown as PaletteState;
		const attrs = vnode.attrs;
		const ready = attrs.query.length >= (attrs.minInput ?? 0);
		const rows = attrs.items;
		const listable = ready && rows.length > 0;
		// Keep the highlight inside the current row set.
		const active = listable
			? Math.min(Math.max(state.active, 0), rows.length - 1)
			: -1;
		state.active = active;
		const activeId =
			active >= 0 ? `${state.listId}-opt-${active}` : undefined;

		return m(
			"div.palette-overlay",
			{
				role: "presentation",
				onclick: (e: MouseEvent) => {
					// Backdrop only; clicks inside the box must not close it.
					if (e.target === e.currentTarget) {
						attrs.onClose();
					}
				},
			},
			m(
				"div.palette",
				{
					role: "dialog",
					"aria-modal": "true",
					"aria-label": attrs.placeholder ?? "Command palette",
				},
				[
					m("input.palette-input", {
						type: "text",
						value: state.raw,
						placeholder: attrs.placeholder ?? "Search",
						role: "combobox",
						"aria-expanded": "true",
						"aria-controls": listable ? state.listId : undefined,
						"aria-activedescendant": activeId,
						"aria-autocomplete": "list",
						"aria-label": attrs.placeholder ?? "Search",
						oncreate: (vn) => {
							(vn.dom as HTMLInputElement).focus();
						},
						oninput: (e: InputEvent) => {
							const value = (e.target as HTMLInputElement).value;
							state.raw = value;
							state.active = 0;
							state.timer.schedule(() => {
								state.lastQuery = value;
								attrs.onQuery(value);
								request();
							});
						},
						onkeydown: (e: KeyboardEvent) =>
							handleKey(e, attrs, state, rows, active),
					}),
					paletteBody(attrs, state, rows, active, ready),
				],
			),
		);
	},
};

/** paletteBody renders the prompt, the empty note, or the option list. */
function paletteBody(
	attrs: PaletteAttrs<any>,
	state: PaletteState,
	rows: ReadonlyArray<PaletteItem<any>>,
	active: number,
	ready: boolean,
): Mithril.Children {
	if (!ready) {
		return m("div.palette-empty.muted", attrs.promptText ?? "Type to search");
	}
	if (rows.length === 0) {
		return m("div.palette-empty.muted", attrs.emptyText ?? "No matches");
	}
	return m(
		"ul.palette-list",
		{ id: state.listId, role: "listbox" },
		rows.map((item, i) => {
			const note = item.description;
			return m(
				"li.palette-option",
				{
					key: item.id,
					id: `${state.listId}-opt-${i}`,
					role: "option",
					"aria-selected": i === active ? "true" : "false",
					class: i === active ? "is-active" : "",
					onmouseenter: () => {
						state.active = i;
						request();
					},
					onclick: () => attrs.onSelect(item),
				},
				[
					m("div.palette-option-text", [
						m("span.palette-option-title", item.title),
						note === null || note === undefined || note === ""
							? null
							: m("span.palette-option-note", note),
					]),
					item.subcommand === true
						? m(
								"span.palette-option-chevron",
								{ "aria-hidden": "true" },
								"›",
							)
						: null,
				],
			);
		}),
	);
}

/** handleKey moves the highlight and commits the active row. */
function handleKey(
	e: KeyboardEvent,
	attrs: PaletteAttrs<any>,
	state: PaletteState,
	rows: ReadonlyArray<PaletteItem<any>>,
	active: number,
): void {
	switch (e.key) {
		case "ArrowDown":
			e.preventDefault();
			moveActive(state, rows.length, active + 1);
			break;
		case "ArrowUp":
			e.preventDefault();
			moveActive(state, rows.length, active - 1);
			break;
		case "Home":
			e.preventDefault();
			moveActive(state, rows.length, 0);
			break;
		case "End":
			e.preventDefault();
			moveActive(state, rows.length, rows.length - 1);
			break;
		case "Enter": {
			const item = rows[active];
			if (item !== undefined) {
				e.preventDefault();
				e.stopPropagation();
				attrs.onSelect(item);
			}
			break;
		}
		default:
			break;
	}
}

/** moveActive clamps the highlight into range and keeps it scrolled into view. */
function moveActive(state: PaletteState, count: number, next: number): void {
	if (count === 0) {
		return;
	}
	const clamped = Math.min(Math.max(next, 0), count - 1);
	state.active = clamped;
	scrollActiveIntoView(state, clamped);
}

/** scrollActiveIntoView scrolls the list by the minimum amount so the active
 * row is fully visible. It computes against bounding rects rather than
 * element.scrollIntoView(options), which the QtWebKit target does not support. */
function scrollActiveIntoView(state: PaletteState, index: number): void {
	const list = document.getElementById(state.listId);
	const option = document.getElementById(`${state.listId}-opt-${index}`);
	if (list === null || option === null) {
		return;
	}
	const listRect = list.getBoundingClientRect();
	const optRect = option.getBoundingClientRect();
	if (optRect.top < listRect.top) {
		list.scrollTop -= listRect.top - optRect.top;
	} else if (optRect.bottom > listRect.bottom) {
		list.scrollTop += optRect.bottom - listRect.bottom;
	}
}
