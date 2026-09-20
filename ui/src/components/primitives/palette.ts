// palette.ts — the command palette primitive: a filterable, keyboard-driven
// overlay list. It renders an optional previous-context row, an input, a
// bounded result list (a title line, an optional muted description line, and
// an optional subcommand chevron per row), and a transparent backdrop.
//
// The palette is always driven by a PaletteList: it materializes the list's
// rows once per list identity in oninit (and again only when the shell swaps
// in a different list), then filters and displays that frozen snapshot itself.
// It never re-reads the source on a redraw, so live store changes do not reach
// an open palette. A row carrying `next` is a subcommand: the palette reports
// it through `onSubcommand` so the shell can swap its current list, while a
// leaf goes to the list's own `onSelect`. The shell still owns the committed
// query, the previous-context row, and every behavior outside presentation and
// selection (closing, drilling, toggling).
//
// The shell owns the committed query. The palette keeps the live input value
// locally and debounces it before calling `onQuery`, so the list updates while
// typing without a store write per keystroke. A `query` change from outside
// resets the input, which lets a shell clear it for a new item set; a shell
// that keeps the query across a list swap (the character picker's Ctrl-K) leaves
// it untouched.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { debounce, type Debounced } from "../../lib/debounce.js";
import { request } from "../../render.js";
import { useEscape, type EscapeState } from "./dialog.js";

/** PALETTE_DEBOUNCE_MS is the quiet period before the typed query reaches the
 * shell. */
const PALETTE_DEBOUNCE_MS = 120;

/** DEFAULT_MAX_VISIBLE caps how many matching rows the palette builds when a
 * shell does not set `maxVisible`. A broad query over a large catalog (the
 * public room list) then renders a bounded prefix plus a "keep typing" note
 * instead of thousands of DOM nodes. */
const DEFAULT_MAX_VISIBLE = 100;

/** PAGE_FALLBACK is the PageUp/PageDown step used before the list has been laid
 * out or when its height cannot be measured, so the keys always move. */
const PAGE_FALLBACK = 10;

/** PaletteItem is one row's data. `title` and `description` are plain strings
 * or prebuilt Mithril content -- pass a component as `m(Component, attrs)` --
 * so a shell can supply rich rows without a per-item render callback.
 * `next` is the list this row opens: when set the row is a subcommand, shown
 * with a right chevron, and the palette reports it through `onSubcommand`
 * instead of running a leaf action. `value` is an optional precomputed result
 * the shell reads back off a chosen leaf in the list's `onSelect`.
 *
 * `C` is the context type the palette routes to `PaletteList` callbacks; the
 * primitive never inspects it. */
export interface PaletteItem<R = unknown, C = unknown> {
	/** id is the row's stable identity (Mithril key and a11y id). */
	id: string;
	title: Mithril.Children;
	description?: Mithril.Children;
	/** filterable is the plain text the palette matches the query against. Set
	 * it when generating the row; it is usually the title text but may differ
	 * (e.g. add an id or a status code, or drop decoration). The palette filters
	 * on this field alone. */
	filterable: string;
	/** next is the subcommand list this row opens. The palette only checks that
	 * it is present; the shell reads it to swap its current list. */
	next?: PaletteList<any, C>;
	/** previous is the context item a drilling shell shows when this row is
	 * chosen (its `previousItem`), instead of the row itself. It lets a list
	 * normalize what a subcommand displays, e.g. a character row producing a
	 * "Link: name" header. The palette never reads it. */
	previous?: PaletteItem<R, C>;
	/** value is an optional precomputed result a shell can attach for later
	 * handling; it is passed back untouched on the item given to onSelect. */
	value?: R;
	/** input is the palette's raw input text, attached in free-text mode
	 * (`PaletteAttrs.freeText`) to the item delivered to `onSelect`, so the list
	 * can read what was typed alongside the row that was chosen. */
	input?: string;
}

/** PaletteList is one palette's worth of rows plus the action for a chosen leaf
 * row, parameterized by the context the shell hands in. `list` produces the
 * rows once (the palette caches them); `onSelect` runs the leaf action. The
 * metadata fields label the palette while that list is showing. A shell
 * specializes the context type; the primitive stays app-agnostic. */
export interface PaletteList<R = unknown, C = unknown> {
	/** id is the list's stable identity. The palette materializes a new list
	 * when this id changes and ignores redraws otherwise. */
	id: string;
	/** placeholder is the palette input prompt for this list. */
	placeholder: string;
	/** emptyText is shown when this list produces no rows for the context. */
	emptyText: string;
	/** list returns the rows to show. It is called once per list identity, so a
	 * shell need not be defensive about repeated or live reads. */
	list: (context: C) => ReadonlyArray<PaletteItem<R, C>>;
	/** onSelect runs this list's action for a chosen leaf row. Omitted when every
	 * row is a subcommand, so the shell never needs to call it. */
	onSelect?: (item: PaletteItem<R, C>, context: C) => void;
}

export interface PaletteAttrs<R = unknown, C = unknown> {
	/** list is the current source of rows. The palette materializes it once and
	 * rebuilds only when its id changes (a shell swap). */
	list: PaletteList<R, C>;
	/** context is handed to the list's `list` and `onSelect` and never inspected
	 * by the palette. */
	context: C;
	/** query is the committed filter text, owned by the shell. The palette keeps
	 * the live input value locally and debounces `onQuery`; setting this from
	 * outside (e.g. a shell clearing it for a new item set) resets the input. */
	query: string;
	/** placeholder overrides the current list's own input prompt. It is for a
	 * list that serves more than one mode from the same identity. */
	placeholder?: string;
	/** minInput is the query length below which results are hidden and the
	 * prompt is shown (default 0). Ignored when `freeText` is set. */
	minInput?: number;
	/** freeText keeps every row visible regardless of the query and delivers the
	 * raw input text on the chosen item's `input` field. It is for palettes whose
	 * input is itself the value (e.g. a link's text) rather than a filter. */
	freeText?: boolean;
	/** promptText is shown while the query is shorter than minInput. */
	promptText?: string;
	/** noMatchesText is shown when the materialized list has rows but none match
	 * the query (default "No matches"). The list's own `emptyText` is used when
	 * it produced no rows at all. */
	noMatchesText?: string;
	/** maxVisible caps how many matching rows are built and rendered. When more
	 * rows match, the palette shows the first `maxVisible` and a note that the
	 * rest are hidden until the query narrows. Set 0 to render every match.
	 * Defaults to DEFAULT_MAX_VISIBLE. */
	maxVisible?: number;
	/** previousItem is the row the shell drilled in from, when this palette is a
	 * subcommand. The palette shows it above the input as context; it is
	 * display-only and never part of the filtered or selectable rows. */
	previousItem?: PaletteItem<R, C>;
	/** onQuery receives the debounced query text. */
	onQuery: (query: string) => void;
	/** onSelect is called for a chosen leaf row, after the list's own onSelect,
	 * so the shell can close or otherwise finish. It is never called for a
	 * subcommand row. */
	onSelect?: (item: PaletteItem<R, C>) => void;
	/** onSubcommand is called for a chosen row carrying `next`, so the shell can
	 * swap its current list. Omitted by a shell that never drills. */
	onSubcommand?: (item: PaletteItem<R, C>) => void;
	/** onClose is called for Escape and a click outside the palette. */
	onClose: () => void;
}

/** filterPaletteItems returns the rows whose `filterable` text contains the
 * query, case-insensitively; an empty (or whitespace) query keeps every row.
 * It is the palette's only matching rule, so a shell passes its full item set
 * and the primitive decides what is visible. */
export function filterPaletteItems<R, C>(
	items: ReadonlyArray<PaletteItem<R, C>>,
	query: string,
): PaletteItem<R, C>[] {
	return matchPaletteItems(items, query, 0).items;
}

/** matchPaletteItems is the palette's bounded matcher: it collects at most
 * `limit` matching rows (`limit <= 0` means no cap) plus the total number that
 * matched. A broad query over a large catalog therefore allocates only the
 * rows it will render, while `total` still counts every match so the palette
 * can report the hidden remainder. The query is matched case-insensitively
 * against `filterable` alone. */
export function matchPaletteItems<R, C>(
	items: ReadonlyArray<PaletteItem<R, C>>,
	query: string,
	limit: number,
): { items: PaletteItem<R, C>[]; total: number } {
	const q = query.trim().toLowerCase();
	const cap = limit > 0 ? limit : Number.POSITIVE_INFINITY;
	const out: PaletteItem<R, C>[] = [];
	let total = 0;
	for (const item of items) {
		if (q !== "" && !item.filterable.toLowerCase().includes(q)) {
			continue;
		}
		total += 1;
		if (out.length < cap) {
			out.push(item);
		}
	}
	return { items: out, total };
}

/** materialized is a list's rows frozen under the id they were built from. The
 * palette replaces it only when the shell swaps in a different list. */
interface Materialized {
	id: string;
	items: ReadonlyArray<PaletteItem<any, any>>;
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
	/** source is the materialized current list, built once on init and rebuilt
	 * only when the shell swaps the list id. */
	source: Materialized;
}

let paletteSeq = 0;

/** materialize builds a fresh frozen snapshot of a list. */
function materialize(
	list: PaletteList<any, any>,
	context: unknown,
): Materialized {
	return { id: list.id, items: list.list(context) };
}

/** escapeHook closes the palette on Escape from anywhere on the page, not only
 * while the input has focus. It captures so a palette layered over a dialog
 * closes alone: the dialog's bubble listener never sees the event. */
const escapeHook = useEscape<PaletteAttrs<any, any>, PaletteState>(
	(vnode) => () => vnode.attrs.onClose(),
	{ capture: true },
);

/** Palette is the reusable command-palette primitive. */
export const Palette: Mithril.Component<PaletteAttrs<any, any>, PaletteState> = {
	...escapeHook,
	oninit: (vnode) => {
		const state = vnode.state as unknown as PaletteState;
		paletteSeq += 1;
		state.listId = `palette-${paletteSeq}`;
		state.raw = vnode.attrs.query;
		state.lastQuery = vnode.attrs.query;
		state.active = 0;
		state.timer = debounce(PALETTE_DEBOUNCE_MS);
		state.source = materialize(vnode.attrs.list, vnode.attrs.context);
	},
	onbeforeupdate: (vnode) => {
		const state = vnode.state as unknown as PaletteState;
		// A deliberate list swap (a shell drilling or toggling) has a new id and
		// rematerializes once; a redraw never does, so live store changes do not
		// reach the rows.
		if (state.source.id !== vnode.attrs.list.id) {
			state.source = materialize(vnode.attrs.list, vnode.attrs.context);
		}
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
		const ready =
			attrs.freeText === true || attrs.query.length >= (attrs.minInput ?? 0);
		// Only filter the frozen snapshot once the prompt is satisfied, and stop
		// after `maxVisible` matches. `hidden` is the count beyond the cap,
		// computed from the total without materializing rows that are not shown.
		let rows: ReadonlyArray<PaletteItem<any, any>> = [];
		let hidden = 0;
		if (ready) {
			if (attrs.freeText === true) {
				// Free-text mode: the input is the value, so every row stays visible
				// and the query is not matched against `filterable`.
				rows = state.source.items;
			} else {
				const matched = matchPaletteItems(
					state.source.items,
					attrs.query,
					attrs.maxVisible ?? DEFAULT_MAX_VISIBLE,
				);
				rows = matched.items;
				hidden = matched.total - matched.items.length;
			}
		}
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
					"aria-label": attrs.list.placeholder,
				},
				[
					attrs.previousItem !== undefined
						? palettePrevious(attrs.previousItem)
						: null,
					m("input.palette-input", {
						type: "text",
						value: state.raw,
						placeholder: attrs.placeholder ?? attrs.list.placeholder,
						role: "combobox",
						"aria-expanded": "true",
						"aria-controls": listable ? state.listId : undefined,
						"aria-activedescendant": activeId,
						"aria-autocomplete": "list",
						"aria-label": attrs.placeholder ?? attrs.list.placeholder,
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
						onkeydown: (e: KeyboardEvent) => handleKey(e, attrs, state, rows, active),
					}),
					paletteBody(state, rows, hidden, active, ready, attrs),
				],
			),
		);
	},
};

/** palettePrevious renders the previous-context row above the input. It mirrors
 * a row's title/description lines but is a plain header: never highlighted,
 * never selected, and outside the filtered list. */
function palettePrevious(item: PaletteItem<any, any>): Mithril.Children {
	const note = item.description;
	return m("div.palette-previous", [
		m("span.palette-previous-title", item.title),
		note === null || note === undefined || note === ""
			? null
			: m("span.palette-previous-note", note),
	]);
}

/** selectRow handles a chosen row: a subcommand is reported to the shell, which
 * swaps its list; a leaf runs the current list's action and then the shell's
 * post-selection callback. In free-text mode the raw input is attached to the
 * item so the list can use the typed value. */
function selectRow(
	attrs: PaletteAttrs<any, any>,
	item: PaletteItem<any, any>,
	input: string,
): void {
	if (item.next !== undefined) {
		attrs.onSubcommand?.(item);
		return;
	}
	const chosen = attrs.freeText === true ? { ...item, input } : item;
	attrs.list.onSelect?.(chosen, attrs.context);
	attrs.onSelect?.(chosen);
}

/** paletteBody renders the prompt, the empty note, or the option list. */
function paletteBody(
	state: PaletteState,
	rows: ReadonlyArray<PaletteItem<any, any>>,
	hidden: number,
	active: number,
	ready: boolean,
	attrs: PaletteAttrs<any, any>,
): Mithril.Children {
	if (!ready) {
		return m("div.palette-empty.muted", attrs.promptText ?? "Type to search");
	}
	if (rows.length === 0) {
		// A list that produced nothing has its own message; a list that produced
		// rows but no match is a plain "no matches".
		const text =
			state.source.items.length === 0
				? attrs.list.emptyText
				: (attrs.noMatchesText ?? "No matches");
		return m("div.palette-empty.muted", text);
	}
	const options = rows.map((item, i) => {
		const note = item.description;
		return m(
			"li.palette-option",
			{
				key: item.id,
				id: `${state.listId}-opt-${i}`,
				role: "option",
				"aria-selected": i === active ? "true" : "false",
				class: i === active ? "is-active" : "",
				onmousemove: () => {
					// Highlight only on real pointer movement. A list swap remounts
					// the palette under a stationary cursor; a synthetic
					// mouseenter would otherwise re-highlight whatever row sits
					// under it, undoing the reset to the first row.
					if (state.active !== i) {
						state.active = i;
						request();
					}
				},
				onclick: () => selectRow(attrs, item, state.raw),
			},
			[
				m("div.palette-option-text", [
					m("span.palette-option-title", item.title),
					note === null || note === undefined || note === ""
						? null
						: m("span.palette-option-note", note),
				]),
				item.next !== undefined
					? m(
							"span.palette-option-chevron",
							{ "aria-hidden": "true" },
							"›",
						)
					: null,
			],
		);
	});
	if (hidden > 0) {
		// Not an option: role=presentation keeps it out of the listbox's
		// selectable rows, and it is rendered after the capped options so the
		// keyboard highlight never lands on it.
		options.push(
			m(
				"li.palette-more.muted",
				{ key: "#more", role: "presentation" },
				`${hidden} more — keep typing to narrow`,
			),
		);
	}
	return m("ul.palette-list", { id: state.listId, role: "listbox" }, options);
}

/** handleKey moves the highlight and commits the active row. Home/End and any
 * modified navigation keys are deliberately left to the input so caret
 * movement and text selection keep working; the unmodified arrows move the
 * highlight and PageUp/PageDown page the result list. */
function handleKey(
	e: KeyboardEvent,
	attrs: PaletteAttrs<any, any>,
	state: PaletteState,
	rows: ReadonlyArray<PaletteItem<any, any>>,
	active: number,
): void {
	switch (e.key) {
		case "ArrowDown":
		case "ArrowUp":
		case "PageDown":
		case "PageUp": {
			// A modifier means an editing shortcut (Shift extends the selection,
			// Ctrl/Alt move by word); leave those to the input. Unmodified keys
			// drive the highlight instead.
			if (e.shiftKey || e.ctrlKey || e.metaKey || e.altKey) {
				break;
			}
			e.preventDefault();
			const down = e.key === "ArrowDown" || e.key === "PageDown";
			const direction = down ? 1 : -1;
			if (e.key === "PageDown" || e.key === "PageUp") {
				moveActiveByPage(state, rows.length, active, direction);
			} else {
				moveActive(state, rows.length, active + direction);
			}
			break;
		}
		case "Enter": {
			const item = rows[active];
			if (item !== undefined) {
				e.preventDefault();
				e.stopPropagation();
				selectRow(attrs, item, state.raw);
			}
			break;
		}
		default:
			break;
	}
}

/** moveActiveByPage moves the highlight a viewport's worth of rows up or down,
 * which scrolls the list through scrollActiveIntoView. It clamps at the ends
 * rather than wrapping: a page step is larger than one row, so a wrap would
 * land unpredictably. */
function moveActiveByPage(
	state: PaletteState,
	count: number,
	active: number,
	direction: number,
): void {
	if (count === 0) {
		return;
	}
	const next = Math.min(
		Math.max(active + direction * pageStep(state), 0),
		count - 1,
	);
	setActive(state, next);
}

/** pageStep estimates how many rows fit in the list viewport from the actual
 * rendered row height, so paging tracks the layout; it falls back to a fixed
 * step before the list exists. */
function pageStep(state: PaletteState): number {
	const list = document.getElementById(state.listId);
	const first = document.getElementById(`${state.listId}-opt-0`);
	if (list === null || first === null) {
		return PAGE_FALLBACK;
	}
	const rowHeight = first.getBoundingClientRect().height;
	if (rowHeight <= 0) {
		return PAGE_FALLBACK;
	}
	// Keep one row of overlap so paging does not jump a row at the boundary.
	const step = Math.floor(list.clientHeight / rowHeight) - 1;
	return step > 0 ? step : 1;
}

/** wrapActive folds a row index into `[0, count)` so arrow-key navigation
 * wraps around the ends: stepping past the last row lands on the first and
 * stepping before the first lands on the last. */
export function wrapActive(next: number, count: number): number {
	return ((next % count) + count) % count;
}

/** moveActive wraps the highlight around the row set and keeps it scrolled into
 * view. Arrow keys use it; paging uses moveActiveByPage, which clamps. */
function moveActive(state: PaletteState, count: number, next: number): void {
	if (count === 0) {
		return;
	}
	setActive(state, wrapActive(next, count));
}

/** setActive commits the highlight and scrolls it into view. */
function setActive(state: PaletteState, index: number): void {
	state.active = index;
	scrollActiveIntoView(state, index);
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