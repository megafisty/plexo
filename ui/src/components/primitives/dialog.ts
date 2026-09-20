import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { request } from "../../render.js";
// Dialog primitives: presentational shells shared by the modal components.
// Pure attrs -> vnode; they never read the store or mutate view state, except
// through the `onClose` callback the caller supplies.

export interface DialogHeaderAttrs {
	title: string;
	onClose: () => void;
	/** id names the header's <h2> so a dialog can point aria-labelledby at it. */
	id?: string;
}

/** DialogHeader is the standard modal title row: the title on the left and a
 * top-right X that closes the dialog. */
export const DialogHeader: Mithril.Component<DialogHeaderAttrs> = {
	view: ({ attrs }) =>
		m("div.dialog-head", [
			m("h2.card-title", { id: attrs.id }, attrs.title),
			m(
				"button.icon-button.dialog-close",
				{
					type: "button",
					"aria-label": "Close",
					title: "Close",
					onclick: attrs.onClose,
				},
				"×",
			),
		]),
};

/** EscapeState is the slice of component state useEscape reads and writes. */
export interface EscapeState {
	/** onKey is the document Escape listener installed while mounted. */
	onKey?: (e: KeyboardEvent) => void;
}

/** EscapeOptions tunes the document listener's phase. A palette mounted over a
 * dialog sets `capture` so its document capture listener runs before the
 * dialog's bubble listener and stops the event, leaving the dialog open when
 * Escape closes only the palette. */
export interface EscapeOptions {
	/** capture installs the listener in the capture phase and stops Escape from
	 * reaching a lower overlay's bubble listener. */
	capture?: boolean;
}

/** useEscape returns the lifecycle hooks that close a component on Escape.
 * Spread the result into the component's object:
 *
 *   const Menu: Mithril.Component = {
 *     ...useEscape((vnode) => () => close(useView())),
 *     view,
 *   };
 *
 * A native document listener is outside Mithril's event system, so it
 * schedules the redraw itself. */
export function useEscape<A = {}, S extends EscapeState = EscapeState>(
	makeAction: (vnode: Mithril.Vnode<A, S>) => () => void,
	options?: EscapeOptions,
): { oncreate: (vnode: Mithril.VnodeDOM<A, S>) => void; onremove: (vnode: Mithril.VnodeDOM<A, S>) => void } {
	const capture = options?.capture === true;
	return {
		oncreate: (vnode) => {
			const state = vnode.state as unknown as S;
			const action = makeAction(vnode);
			state.onKey = (e: KeyboardEvent) => {
				if (e.key !== "Escape") {
					return;
				}
				if (capture) {
					// The topmost overlay owns Escape. Stopping the capture-phase event
					// keeps a dialog below this palette from also closing.
					e.stopPropagation();
				}
				action();
				request();
			};
			document.addEventListener("keydown", state.onKey, capture);
		},
		onremove: (vnode) => {
			const state = vnode.state as unknown as S;
			if (state.onKey !== undefined) {
				document.removeEventListener("keydown", state.onKey, capture);
				state.onKey = undefined;
			}
		},
	};
}

export interface DialogAttrs {
	title: string;
	onClose: () => void;
	/** subtitle renders a muted line under the title (e.g. the room or partner
	 * name a detail dialog is about). */
	subtitle?: string;
	/** class adds the component-specific card classes (e.g. "logs-dialog"). */
	class?: string;
	/** children are the dialog body, rendered between the header and the
	 * caller's actions. */
}

interface DialogState extends EscapeState {
	/** id is the element that names the dialog for assistive tech. */
	id: string;
}

let dialogSeq = 0;

/** Dialog is the standard modal shell. It owns the dimmed backdrop (clicking
 * outside the card closes), the header with its close button, and the
 * Escape-to-close binding, and it exposes the dialog semantics
 * (role/aria-modal/aria-labelledby) the individual dialogs used to repeat.
 *
 * The overlay closes only when the click target is the overlay itself, so a
 * card never needs its own stopPropagation. */
export const Dialog: Mithril.Component<DialogAttrs, DialogState> = {
	...useEscape<DialogAttrs, DialogState>((vnode) => () => vnode.attrs.onClose()),
	oninit: (vnode) => {
		const state = vnode.state as unknown as DialogState;
		dialogSeq += 1;
		state.id = `dialog-title-${dialogSeq}`;
	},
	view: (vnode) => {
		const state = vnode.state as unknown as DialogState;
		const { attrs } = vnode;
		return m(
			"div.dialog-overlay",
			{
				role: "presentation",
				onclick: (e: MouseEvent) => {
					// Backdrop only; clicks inside the card must not close it.
					if (e.target === e.currentTarget) {
						attrs.onClose();
					}
				},
			},
			m(
				"div.card.dialog",
				{
					class: attrs.class,
					role: "dialog",
					"aria-modal": "true",
					"aria-labelledby": state.id,
				},
				[
					m(DialogHeader, {
						title: attrs.title,
						onClose: attrs.onClose,
						id: state.id,
					}),
					attrs.subtitle === undefined
						? null
						: m("p.card-subtitle", attrs.subtitle),
					vnode.children,
				],
			),
		);
	},
};

/** DialogTab is one switchable panel in a DialogTabs strip. */
export interface DialogTab {
	/** id is the tab's stable identity, matched against DialogTabs.active. */
	id: string;
	label: string;
	/** render builds this tab's panel; called only for the active tab, so an
	 * expensive panel is never built for a tab the user is not looking at. */
	render: () => Mithril.Children;
}

export interface DialogTabsAttrs {
	/** tabs are the panels, in display order. */
	tabs: ReadonlyArray<DialogTab>;
	/** active is the selected tab id; the caller owns it (a switch may need to
	 * reset the caller's own filter/selection state). */
	active: string;
	onSelect: (id: string) => void;
	/** fill makes the tabs share the row width evenly (default: content width). */
	fill?: boolean;
}

interface DialogTabsState {
	/** base namespaces the generated tab and panel element ids. */
	base: string;
}

let tabsSeq = 0;

/** DialogTabs is the shared tab strip: a role="tablist" whose buttons carry the
 * WAI-ARIA tab semantics and Left/Right/Home/End keyboard navigation, followed
 * by the active tab's panel as a role="tabpanel". The caller owns the selected
 * id so a switch can reset caller-owned state; the component owns everything
 * else. */
export const DialogTabs: Mithril.Component<DialogTabsAttrs, DialogTabsState> = {
	oninit: (vnode) => {
		const state = vnode.state as unknown as DialogTabsState;
		tabsSeq += 1;
		state.base = `tabs-${tabsSeq}`;
	},
	view: (vnode) => {
		const state = vnode.state as unknown as DialogTabsState;
		const { tabs, active, onSelect, fill } = vnode.attrs;
		const activeTab = tabs.find((t) => t.id === active) ?? tabs[0];
		return [
			m(
				"div.dialog-tabs",
				{
					role: "tablist",
					class: fill === true ? "is-fill" : "",
					onkeydown: (e: KeyboardEvent) =>
						handleTabKey(e, state.base, tabs, active, onSelect),
				},
				tabs.map((tab) => {
					const selected = tab.id === active;
					return m(
						"button.dialog-tab",
						{
							key: tab.id,
							type: "button",
							role: "tab",
							id: `${state.base}-${tab.id}-tab`,
							"aria-controls": `${state.base}-${tab.id}`,
							"aria-selected": selected ? "true" : "false",
							// Roving tabindex: only the active tab is in the page
							// tab order; the arrows reach the rest.
							tabindex: selected ? "0" : "-1",
							class: selected ? "is-active" : "",
							onclick: () => onSelect(tab.id),
						},
						tab.label,
					);
				}),
			),
			m(
				"div.dialog-tabpanel",
				{
					role: "tabpanel",
					id: `${state.base}-${activeTab?.id ?? active}`,
					"aria-labelledby": `${state.base}-${activeTab?.id ?? active}-tab`,
					tabindex: "0",
				},
				activeTab === undefined ? null : activeTab.render(),
			),
		];
	},
};

/** handleTabKey implements the ARIA tabs keyboard model: Left/Right cycle
 * (wrapping), Home/End jump to the ends, and focus follows the selection. */
function handleTabKey(
	e: KeyboardEvent,
	base: string,
	tabs: ReadonlyArray<DialogTab>,
	active: string,
	onSelect: (id: string) => void,
): void {
	let next: string | undefined;
	const index = tabs.findIndex((t) => t.id === active);
	switch (e.key) {
		case "ArrowLeft":
			next = tabs[(index - 1 + tabs.length) % tabs.length]?.id;
			break;
		case "ArrowRight":
			next = tabs[(index + 1) % tabs.length]?.id;
			break;
		case "Home":
			next = tabs[0]?.id;
			break;
		case "End":
			next = tabs[tabs.length - 1]?.id;
			break;
		default:
			return;
	}
	if (next === undefined) {
		return;
	}
	e.preventDefault();
	onSelect(next);
	// The button already exists; focus it directly rather than after redraw.
	document.getElementById(`${base}-${next}-tab`)?.focus();
}