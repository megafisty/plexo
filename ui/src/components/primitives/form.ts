// Presentational primitives. Pure attrs -> vnode: they never read the store,
// never mutate view state, and never dispatch.
import m from "../../mithril.js";
import type * as Mithril from "mithril";

export interface TextFieldAttrs {
	label: string;
	value: string;
	type?: "text" | "password";
	/** name is the form-field name; defaults to the label. */
	name?: string;
	autocomplete?: string;
	disabled?: boolean;
	oninput: (value: string) => void;
	onsubmit?: () => void;
}

export const TextField: Mithril.Component<TextFieldAttrs> = {
	view: ({ attrs }) =>
		m("label.field", [
			m("span.field-label", attrs.label),
			m("input", {
				type: attrs.type ?? "text",
				name: attrs.name ?? attrs.label,
				value: attrs.value,
				autocomplete: attrs.autocomplete,
				disabled: attrs.disabled,
				oninput: (e: InputEvent) => {
					attrs.oninput((e.target as HTMLInputElement).value);
				},
				onkeydown: (e: KeyboardEvent) => {
					if (e.key === "Enter" && attrs.onsubmit !== undefined) {
						e.preventDefault();
						attrs.onsubmit();
					}
				},
			}),
		]),
};

export interface ButtonAttrs {
	label: string;
	disabled?: boolean;
	busy?: boolean;
	/** busyLabel replaces the label while busy (default "Working…"). */
	busyLabel?: string;
	/** variant picks the button family: primary (default) or secondary. */
	variant?: "primary" | "secondary";
	/** small renders the compact size. */
	small?: boolean;
	/** title is the hover tooltip. */
	title?: string;
	/** class adds caller-owned classes (a position hook, a modifier). */
	class?: string;
	onclick: () => void;
}

export const Button: Mithril.Component<ButtonAttrs> = {
	view: ({ attrs }) => {
		const cls = [
			"button",
			attrs.small === true ? "button-small" : "",
			attrs.variant === "secondary" ? "button-secondary" : "",
			attrs.class ?? "",
		]
			.filter((c) => c !== "")
			.join(" ");
		return m(
			"button",
			{
				type: "button",
				class: cls,
				title: attrs.title,
				disabled: attrs.disabled === true || attrs.busy === true,
				onclick: attrs.onclick,
			},
			attrs.busy === true ? (attrs.busyLabel ?? "Working…") : attrs.label,
		);
	},
};

export const FormError: Mithril.Component<{ message: string | null }> = {
	view: ({ attrs }) =>
		attrs.message === null ? null : m("p.form-error", attrs.message),
};

export const Spinner: Mithril.Component<{ label?: string }> = {
	view: ({ attrs }) => m("p.spinner", attrs.label ?? "Loading…"),
};

export interface CheckboxAttrs {
	label: string;
	checked: boolean;
	disabled?: boolean;
	/** name is the form-field name; defaults to the label. */
	name?: string;
	onchange: (value: boolean) => void;
}

// Checkbox is a labelled checkbox. It reports the new value; the caller owns
// the value.
export const Checkbox: Mithril.Component<CheckboxAttrs> = {
	view: ({ attrs }) =>
		m("label.checkbox-field", [
			m("input", {
				type: "checkbox",
				name: attrs.name ?? attrs.label,
				checked: attrs.checked,
				disabled: attrs.disabled,
				onchange: (e: Event) => {
					attrs.onchange((e.target as HTMLInputElement).checked);
				},
			}),
			m("span", attrs.label),
		]),
};

export interface CardAttrs {
	title: string;
	subtitle?: string;
}

export const Card: Mithril.Component<CardAttrs> = {
	view: ({ attrs, children }) =>
		m("section.card", [
			m("h1.card-title", attrs.title),
			attrs.subtitle === undefined
				? null
				: m("p.card-subtitle", attrs.subtitle),
			children,
		]),
};