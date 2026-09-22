// select.ts — filtered select controls: the shared popup machinery plus the
// single-select Combobox and the multi-select MultiSelect. Absorbs popup.ts,
// Combobox.ts, and MultiSelect.ts.

import type * as Mithril from "mithril";
import { boundMatches, type Bounded } from "../../lib/list.js";
import { request } from "../../render.js";
import m from "../../mithril.js";


// ==========================================================================
// popup.ts
// ==========================================================================
// Shared machinery for the filtered popup controls (Combobox, MultiSelect).
// Both open a fixed-position list anchored to a text box, filter it by that
// box's text through a lowercase index, move a keyboard cursor with the arrows,
// and close on Escape or an outside click. This module owns that state and
// those calculations; the two components keep their own option shape, selection
// model, and row markup.
//
// It is deliberately not a component: it operates on the caller's component
// state, so each control keeps its own attrs types and Mithril lifecycle.

/** BaseOption is the minimal shape the index needs: a stable id. */
export interface BaseOption {
	readonly id: string | number;
}

/** IndexedOption is one option plus its precomputed lowercase filter key. */
export interface IndexedOption<O> {
	readonly option: O;
	readonly lower: string;
}

/** PopupState is the shared slice of a popup component's state. */
export interface PopupState<O extends BaseOption> {
	open: boolean;
	/** query is the live filter text; the list is filtered from it directly. */
	query: string;
	/** index is the lowercase search index, rebuilt when `indexRef` changes. */
	index: ReadonlyArray<IndexedOption<O>>;
	indexRef?: ReadonlyArray<O>;
	/** active is the keyboard cursor into the filtered list; -1 is none. */
	active: number;
	/** listId names the listbox so the input can point aria-controls at it. */
	listId: string;
	/** control is the input element, used to anchor the fixed-position popup. */
	control?: HTMLInputElement;
	/** rect is the control's viewport box, captured on open and on any scroll or
	 * resize, so listStyle() does not force a layout read on every redraw. */
	rect?: DOMRect;
	/** onViewportChange re-anchors the popup when the page scrolls or resizes. */
	onViewportChange?: () => void;
}

/** DEFAULT_MAX_VISIBLE caps rendered rows so a broad query cannot build
 * hundreds of DOM nodes; the overflow is summarized in one tail row. */
export const DEFAULT_MAX_VISIBLE = 100;

/** popupLifecycle returns the shared oncreate/onremove hooks that re-anchor the
 * fixed-position popup on any scroll or resize. Spread into the component. */
export function popupLifecycle<S extends PopupState<any>>(): {
	oncreate: (vnode: Mithril.VnodeDOM<any, S>) => void;
	onremove: (vnode: Mithril.VnodeDOM<any, S>) => void;
} {
	return {
		oncreate: (vnode) => {
			const state = vnode.state as unknown as S;
			state.onViewportChange = () => {
				if (state.open) {
					refreshPopupAnchor(state);
					request();
				}
			};
			window.addEventListener("scroll", state.onViewportChange, true);
			window.addEventListener("resize", state.onViewportChange);
		},
		onremove: (vnode) => {
			const state = vnode.state as unknown as S;
			if (state.onViewportChange !== undefined) {
				window.removeEventListener("scroll", state.onViewportChange, true);
				window.removeEventListener("resize", state.onViewportChange);
				state.onViewportChange = undefined;
			}
		},
	};
}

/** popupInit seeds the shared state fields. Call from oninit. */
let popupSeq = 0;
export function popupInit<O extends BaseOption>(state: PopupState<O>): void {
	popupSeq += 1;
	state.listId = `popup-list-${popupSeq}`;
	state.open = false;
	state.query = "";
	state.active = -1;
	state.index = [];
}

/** ensureIndex rebuilds the lowercase search index only when the options array
 * identity changes, so the list is indexed once, not every redraw. */
export function ensureIndex<O extends BaseOption>(
	state: PopupState<O>,
	options: ReadonlyArray<O>,
	labelOf: (option: O) => string,
): void {
	if (state.indexRef === options) {
		return;
	}
	state.indexRef = options;
	state.index = options.map((option) => ({
		option,
		lower: labelOf(option).toLowerCase(),
	}));
}

/** filterOptions returns the query's matches, capped at maxVisible, plus the
 * number of matches left unrendered. */
export function filterOptions<O extends BaseOption>(
	state: PopupState<O>,
	maxVisible: number,
): Bounded<IndexedOption<O>> {
	const query = state.query.trim().toLowerCase();
	const matches =
		query === "" ? state.index : state.index.filter((e) => e.lower.includes(query));
	return boundMatches(matches, maxVisible);
}

/** openPopup opens the list, clearing the query and placing the cursor at
 * `active` (0 highlights the first row; -1 leaves no cursor). */
export function openPopup<O extends BaseOption>(
	state: PopupState<O>,
	active = 0,
): void {
	state.open = true;
	state.query = "";
	state.active = active;
	refreshPopupAnchor(state);
}

/** closePopup closes the list and clears the query and cursor. */
export function closePopup<O extends BaseOption>(state: PopupState<O>): void {
	state.open = false;
	state.query = "";
	state.active = -1;
}

/** refreshPopupAnchor re-reads the control's viewport box. It is called on open
 * and whenever the page scrolls or resizes; caching the box keeps listStyle()
 * from forcing a layout read on every redraw while the popup is open. */
function refreshPopupAnchor<O extends BaseOption>(state: PopupState<O>): void {
	if (state.control !== undefined) {
		state.rect = state.control.getBoundingClientRect();
	}
}

/** listStyle anchors the fixed-position popup to the control. Fixed positioning
 * escapes the parent's overflow clipping, and `.is-open` raises the open field
 * so the popup stacks above later fields. It flips above the control when there
 * is more room there than below. */
export function listStyle<O extends BaseOption>(
	state: PopupState<O>,
): Record<string, string> | undefined {
	const rect = state.rect;
	if (!state.open || rect === undefined) {
		return undefined;
	}
	const below = window.innerHeight - rect.bottom;
	const above = rect.top;
	if (below < 180 && above > below) {
		return {
			bottom: `${window.innerHeight - rect.top + 4}px`,
			left: `${rect.left}px`,
			width: `${rect.width}px`,
		};
	}
	return {
		top: `${rect.bottom + 4}px`,
		left: `${rect.left}px`,
		width: `${rect.width}px`,
	};
}

/** popupKey drives the popup from the focused text box: arrows move the cursor,
 * Enter hands the active option to `onEnter`, Escape closes. Consumed keys stop
 * propagating so an outer dialog's Escape handler does not also fire while the
 * popup is open. */
export function popupKey<O extends BaseOption>(
	e: KeyboardEvent,
	state: PopupState<O>,
	visible: ReadonlyArray<IndexedOption<O>>,
	onEnter: (option: O) => void,
): void {
	switch (e.key) {
		case "ArrowDown":
			e.preventDefault();
			e.stopPropagation();
			state.active = Math.min(state.active + 1, visible.length - 1);
			break;
		case "ArrowUp":
			e.preventDefault();
			e.stopPropagation();
			state.active = Math.max(state.active - 1, 0);
			break;
		case "Enter": {
			const entry = visible[state.active];
			if (state.open && entry !== undefined) {
				e.preventDefault();
				e.stopPropagation();
				onEnter(entry.option);
			}
			break;
		}
		case "Escape":
			if (state.open) {
				e.preventDefault();
				e.stopPropagation();
				closePopup(state);
			}
			break;
		default:
			break;
	}
}

/** PopupMode selects the popup's CSS family. */
type PopupMode = "combobox" | "multiselect";

/** POPUP_CLASSES holds each family's class names literally, so the CSS stays
 * greppable even though the shell builds them from the mode. */
const POPUP_CLASSES: Record<
	PopupMode,
	{ root: string; overlay: string; control: string; input: string; list: string }
> = {
	combobox: {
		root: "combobox",
		overlay: "combobox-overlay",
		control: "combobox-control",
		input: "combobox-input",
		list: "combobox-list",
	},
	multiselect: {
		root: "multiselect",
		overlay: "multiselect-overlay",
		control: "multiselect-control",
		input: "multiselect-input",
		list: "multiselect-list",
	},
};

/** PopupBoxAttrs is the shared popup chrome: the anchored text box, the
 * full-viewport click-catcher, and the fixed-position list. The caller owns the
 * component state and passes its handlers; the shell owns the shared ARIA and
 * class structure, so Combobox and MultiSelect cannot drift apart. */
interface PopupBoxAttrs {
	mode: PopupMode;
	open: boolean;
	listId: string;
	/** name is the form-field name for the text box. */
	name: string;
	onOverlay: () => void;
	value: string;
	placeholder?: string;
	disabled?: boolean;
	onfocus: () => void;
	oninput: (e: InputEvent) => void;
	onkeydown: (e: KeyboardEvent) => void;
	onclick?: () => void;
	onControl: (el: HTMLInputElement) => void;
	trailing?: Mithril.Children;
	listStyle: Record<string, string> | undefined;
	multiselectable?: boolean;
	rows: Mithril.Children;
}

const PopupBox: Mithril.Component<PopupBoxAttrs> = {
	view: ({ attrs }) => {
		const cls = POPUP_CLASSES[attrs.mode];
		return m("div", { class: `${cls.root}${attrs.open ? " is-open" : ""}` }, [
			attrs.open
				? m("div", { class: cls.overlay, onclick: attrs.onOverlay })
				: null,
			m("div", { class: cls.control }, [
				m("input", {
					class: cls.input,
					type: "text",
					name: attrs.name,
					value: attrs.value,
					placeholder: attrs.placeholder,
					disabled: attrs.disabled,
					role: "combobox",
					"aria-expanded": attrs.open ? "true" : "false",
					"aria-controls": attrs.listId,
					"aria-autocomplete": "list",
					oncreate: (vn) => attrs.onControl(vn.dom as HTMLInputElement),
					onfocus: attrs.onfocus,
					oninput: attrs.oninput,
					onkeydown: attrs.onkeydown,
					onclick: attrs.onclick,
				}),
				attrs.trailing ?? null,
				attrs.open
					? m(
							"ul",
							{
								class: cls.list,
								id: attrs.listId,
								role: "listbox",
								"aria-multiselectable":
									attrs.multiselectable === true ? "true" : undefined,
								style: attrs.listStyle,
							},
							attrs.rows,
						)
					: null,
			]),
		]);
	},
};

// ==========================================================================
// Combobox.ts
// ==========================================================================
// Combobox: a filterable single-select combobox. It renders a text box that,
// on focus, pops out a scrollable list of options filtered by the box's text.
// The caller owns the selection: it passes `selected` (an option id or null)
// and receives the next id from `onchange`, so the component stays
// presentational — it reads no store, mutates no view state, and dispatches
// nothing. An optional clear button emits null. The popup is fixed-positioned
// and anchored to the control, exactly like MultiSelect, so it escapes the
// dialog's overflow; Escape or an outside click closes it.
//
// The popup state, filtering, anchor math, and keyboard model live in the popup
// section above, shared with MultiSelect.

export interface ComboboxOption {
	/** id is the option's stable identity, matched against `selected`. */
	readonly id: string;
	readonly label: string;
	/** hint is secondary text shown at the right of the row (e.g. a kind). */
	readonly hint?: string;
}

export interface ComboboxAttrs {
	label: string;
	options: ReadonlyArray<ComboboxOption>;
	selected: string | null;
	onchange: (id: string | null) => void;
	placeholder?: string;
	disabled?: boolean;
	/** clearable shows a × that emits null when a value is selected. */
	clearable?: boolean;
	/** maxVisible caps rendered rows (default 100); extras are summarized. */
	maxVisible?: number;
}

type ComboboxState = PopupState<ComboboxOption>;

export const Combobox: Mithril.Component<ComboboxAttrs> = {
	...popupLifecycle<ComboboxState>(),
	oninit: (vnode) => {
		popupInit(vnode.state as ComboboxState);
	},
	view: (vnode) => {
		const state = vnode.state as ComboboxState;
		const attrs = vnode.attrs;

		// Only the open popup needs its index and filtered rows. A closed control is
		// usually one of many (the search dialog mounts one per filter field), so
		// skipping the scan on unrelated redraws keeps those renders cheap.
		let visible: ReadonlyArray<IndexedOption<ComboboxOption>> = [];
		let hidden = 0;
		if (state.open) {
			ensureIndex(state, attrs.options, (option) => option.label);
			({ visible, hidden } = filterOptions(
				state,
				attrs.maxVisible ?? DEFAULT_MAX_VISIBLE,
			));
		}
		const selectedLabel = labelFor(attrs.options, attrs.selected);
		const rows: Mithril.Vnode[] = visible.map((entry, i) =>
			m(
				"li.combobox-option",
				{
					key: entry.option.id,
					role: "option",
					"aria-selected": entry.option.id === attrs.selected ? "true" : "false",
					class: i === state.active ? "is-active" : "",
					onclick: () => choose(attrs, state, entry.option.id),
				},
				[
					m("span.combobox-option-label", entry.option.label),
					entry.option.hint !== undefined
						? m("span.combobox-option-hint", entry.option.hint)
						: null,
				],
			),
		);
		if (hidden > 0) {
			rows.push(
				m(
					"li.combobox-more.muted",
					{ key: "#more" },
					`${hidden} more — keep typing to narrow`,
				),
			);
		}

		return m(PopupBox, {
			mode: "combobox",
			open: state.open,
			listId: state.listId,
			name: attrs.label,
			onOverlay: () => closePopup(state),
			value: state.open ? state.query : (selectedLabel ?? ""),
			placeholder: attrs.placeholder ?? attrs.label,
			disabled: attrs.disabled,
			onfocus: () => {
				if (attrs.disabled !== true) {
					openPopup(state, 0);
				}
			},
			oninput: (e: InputEvent) => {
				state.query = (e.target as HTMLInputElement).value;
				state.open = true;
				state.active = 0;
			},
			onkeydown: (e: KeyboardEvent) => {
				popupKey(e, state, visible, (option) => choose(attrs, state, option.id));
			},
			onControl: (el) => {
				state.control = el;
			},
			trailing:
				attrs.clearable === true && attrs.selected !== null && attrs.disabled !== true
					? m(
							"button.combobox-clear",
							{
								type: "button",
								"aria-label": "Clear",
								onclick: () => {
									attrs.onchange(null);
									closePopup(state);
								},
							},
							"×",
						)
					: null,
			listStyle: listStyle(state),
			rows:
				visible.length === 0 ? m("li.combobox-empty.muted", "No matches") : rows,
		});
	},
};

/** choose commits one option and closes the popup. */
function choose(
	attrs: ComboboxAttrs,
	state: ComboboxState,
	id: string,
): void {
	attrs.onchange(id);
	closePopup(state);
}

/** labelFor finds the selected option's display label. */
function labelFor(
	options: ReadonlyArray<ComboboxOption>,
	selected: string | null,
): string | null {
	if (selected === null) {
		return null;
	}
	const option = options.find((o) => o.id === selected);
	return option === undefined ? null : option.label;
}

// ==========================================================================
// MultiSelect.ts
// ==========================================================================
// MultiSelect: a filterable multi-select combobox. It renders a text box that,
// on focus, pops out a scrollable list of checkboxes filtered by the box's
// text. The caller owns the selection: it passes `selected` (ids) and receives
// the whole next selection from `onchange`, so the component stays
// presentational — it reads no store, mutates no view state, and dispatches
// nothing. The character-search dialog mounts one per mapping field, passing
// that field's `name` and `entries` straight through as `label` and `options`;
// the emitted ids are exactly what the FKS payload needs. The text box
// shows the selected count as its placeholder when idle, opens on focus, and
// closes on Escape or an outside click (a full-viewport overlay, matching the
// roster context menu).
//
// The popup state, filtering, anchor math, and keyboard model live in the popup
// section above, shared with Combobox.

/** MultiSelectID is one option's identity: a kink id (number) or an enum value
 * (string), exactly as it will be placed in the FKS payload. */
export type MultiSelectID = string | number;

export interface MultiSelectOption {
	readonly id: MultiSelectID;
	readonly name: string;
}

export interface MultiSelectAttrs {
	/** label names the control and seeds the idle placeholder. */
	label: string;
	/** options are the selectable entries, in display order. */
	options: ReadonlyArray<MultiSelectOption>;
	/** selected is the caller-owned selection, by id. */
	selected: ReadonlyArray<MultiSelectID>;
	/** onchange receives the entire next selection (in option order). */
	onchange: (next: MultiSelectID[]) => void;
	placeholder?: string;
	/** maxVisible caps rendered rows (default 100); extras are summarized. */
	maxVisible?: number;
}

type MultiSelectState = PopupState<MultiSelectOption>;

export const MultiSelect: Mithril.Component<MultiSelectAttrs> = {
	...popupLifecycle<MultiSelectState>(),
	oninit: (vnode) => {
		popupInit(vnode.state as MultiSelectState);
	},
	view: (vnode) => {
		const state = vnode.state as MultiSelectState;
		const attrs = vnode.attrs;

		// Only the open popup needs its index and filtered rows; see Combobox.
		let visible: ReadonlyArray<IndexedOption<MultiSelectOption>> = [];
		let hidden = 0;
		if (state.open) {
			ensureIndex(state, attrs.options, (option) => option.name);
			({ visible, hidden } = filterOptions(
				state,
				attrs.maxVisible ?? DEFAULT_MAX_VISIBLE,
			));
		}
		const selected = new Set<MultiSelectID>(attrs.selected);
		const rows: Mithril.Vnode[] = visible.map((entry, i) =>
			m(
				"li.multiselect-option",
				{
					key: entry.option.id,
					role: "option",
					"aria-selected": selected.has(entry.option.id) ? "true" : "false",
					class: i === state.active ? "is-active" : "",
				},
				m("label.multiselect-option-label", [
					m("input", {
						type: "checkbox",
						name: attrs.label,
						checked: selected.has(entry.option.id),
						onchange: () => {
							toggleOption(attrs, entry.option.id);
						},
					}),
					m("span", entry.option.name),
				]),
			),
		);
		if (hidden > 0) {
			// The list is keyed, so this row must be keyed too; "#more" cannot
			// collide with an option id. Pushing it conditionally (rather than
			// emitting a null hole) keeps the fragment all-keyed.
			rows.push(
				m(
					"li.multiselect-more.muted",
					{ key: "#more" },
					`${hidden} more — keep typing to narrow`,
				),
			);
		}

		return m(PopupBox, {
			mode: "multiselect",
			open: state.open,
			listId: state.listId,
			name: attrs.label,
			onOverlay: () => closePopup(state),
			value: state.query,
			placeholder:
				attrs.selected.length > 0
					? `${attrs.label} (${attrs.selected.length} selected)`
					: (attrs.placeholder ?? attrs.label),
			onfocus: () => {
				openPopup(state, -1);
			},
			onclick: () => {
				state.open = true;
			},
			oninput: (e: InputEvent) => {
				state.query = (e.target as HTMLInputElement).value;
				state.open = true;
				state.active = 0;
			},
			onkeydown: (e: KeyboardEvent) => {
				popupKey(e, state, visible, (option) => toggleOption(attrs, option.id));
			},
			onControl: (el) => {
				state.control = el;
			},
			listStyle: listStyle(state),
			multiselectable: true,
			rows:
				visible.length === 0 ? m("li.multiselect-empty.muted", "No matches") : rows,
		});
	},
};

/** toggleOption flips one id and reports the whole next selection in option
 * order. It never mutates `attrs.selected`. */
function toggleOption(attrs: MultiSelectAttrs, id: MultiSelectID): void {
	const selected = new Set<MultiSelectID>(attrs.selected);
	const add = !selected.has(id);
	attrs.onchange(
		attrs.options
			.filter((option) => (option.id === id ? add : selected.has(option.id)))
			.map((option) => option.id),
	);
}
