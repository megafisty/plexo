import m from "../../mithril.js";
import type * as Mithril from "mithril";
import type { JoinTarget } from "../../api.js";
import { joinLabel } from "../../lib/format.js";
import { Button } from "../primitives/form.js";
// Presentational settings primitives. Pure attrs -> vnode: they never read the
// store, never mutate view state, and never dispatch. The caller owns the draft
// and the save action; these only render and report intent.
//
// The labelled checkbox lives in primitives/form.ts as `Checkbox` and is used
// directly; there is no settings-specific variant.

export interface SettingsActionsAttrs {
	busy: boolean;
	dirty: boolean;
	onSave: () => void;
	onReset: () => void;
}

/** SettingsActions is the Save / Reset-to-defaults row shared by the settings
 * cards. Save is enabled only while the draft is dirty. */
export const SettingsActions: Mithril.Component<SettingsActionsAttrs> = {
	view: ({ attrs }) =>
		m("div.settings-actions", [
			m(Button, {
				label: "Save",
				busy: attrs.busy,
				busyLabel: "Saving…",
				disabled: !attrs.dirty,
				onclick: attrs.onSave,
			}),
			m(Button, {
				label: "Reset to defaults",
				variant: "secondary",
				disabled: attrs.busy,
				onclick: attrs.onReset,
			}),
		]),
};

export interface AutoJoinListAttrs {
	entries: JoinTarget[];
	disabled?: boolean;
	/** joined is how many channels the character is currently in, so the
	 * "replace" action can explain what it will write. */
	joined: number;
	onRemove: (index: number) => void;
	onReplace: () => void;
}

/** AutoJoinList is the managed auto-join list: entries are pruned with the X,
 * and the whole list can be replaced with the currently joined channels. */
export const AutoJoinList: Mithril.Component<AutoJoinListAttrs> = {
	view: ({ attrs }) =>
		m("div.settings-subsection", [
			m("div.settings-subsection-head", [
				m("span.settings-section-label", "Auto-join channels"),
				m(Button, {
					label: `Replace with joined (${attrs.joined})`,
					variant: "secondary",
					small: true,
					disabled: attrs.disabled,
					title: "Replace this list with the channels this character is in now",
					onclick: attrs.onReplace,
				}),
			]),
			attrs.entries.length === 0
				? m("p.settings-empty.muted", "No auto-join channels.")
				: m(
						"ul.settings-list",
						attrs.entries.map((entry, index) =>
							m(
								"li.settings-chip",
								{ key: `${entry.kind}:${entry.id}:${index}` },
								[
									m(
										"span.conv-kind",
										{ title: entry.kind },
										entry.kind === "room" ? "⌂" : "#",
									),
									m("span.settings-chip-label", joinLabel(entry)),
									m("button.chip-remove", {
										type: "button",
										title: `Remove ${entry.id}`,
										"aria-label": `Remove ${joinLabel(entry)}`,
										disabled: attrs.disabled,
										onclick: () => attrs.onRemove(index),
									}, "×"),
								],
							),
						),
					),
		]),
};

export interface HighlightListAttrs {
	values: string[];
	disabled?: boolean;
	onRemove: (index: number) => void;
}

/** HighlightList renders the chips for a character's highlight substrings. */
export const HighlightList: Mithril.Component<HighlightListAttrs> = {
	view: ({ attrs }) =>
		attrs.values.length === 0
			? m("p.settings-empty.muted", "No highlights.")
			: m(
					"ul.settings-list",
					attrs.values.map((value, index) =>
						m("li.settings-chip", { key: `${value}:${index}` }, [
							m("span.settings-chip-label", value),
							m("button.chip-remove", {
								type: "button",
								title: `Remove ${value}`,
								"aria-label": `Remove ${value}`,
								disabled: attrs.disabled,
								onclick: () => attrs.onRemove(index),
							}, "×"),
						]),
					),
				),
};