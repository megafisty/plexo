// previewfield.ts — a dialog-bound Composer with a Preview/Edit toggle. It wraps
// the generic Composer: the caller still owns the raw draft and its oninput,
// while this component owns only the rendered preview (fetch, cache, staleness).
// It is shared by the status dialog and the room-management description editor,
// so it carries no feature state.
//
// Preview state is ephemeral. It flips back to editing whenever `resetKey`
// changes, so a caller can drop a preview that belonged to another subject (a
// different session or room, or a draft it just overwrote) without reaching into
// the component. `text` mirrors the caller's controlled value so a late render
// can tell whether the draft it was asked for is still the one on screen.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { renderBBCode } from "../../api.js";
import { request } from "../../render.js";
import { Composer, type ComposerAttrs } from "./composer.js";
import { FormError, Spinner } from "../primitives/form.js";

/** PreviewState is the derived preview for one draft. */
export interface PreviewState {
	/** text mirrors the caller's draft, so a render that lands after the field
	 * moved on can be discarded. */
	text: string;
	/** preview flips the field between the raw editor and the core's rendered
	 * HTML. */
	preview: boolean;
	/** previewHTML is the last rendered fragment, or null before/while loading. */
	previewHTML: string | null;
	/** previewFor is the raw text previewHTML was rendered from, so an unchanged
	 * message is not re-fetched when the user flips back to preview. */
	previewFor: string | null;
	previewBusy: boolean;
	previewError: string | null;
}

export function newPreviewState(): PreviewState {
	return {
		text: "",
		preview: false,
		previewHTML: null,
		previewFor: null,
		previewBusy: false,
		previewError: null,
	};
}

/** resetPreview returns the field to editing and drops any rendered fragment. */
export function resetPreview(state: PreviewState): void {
	state.preview = false;
	state.previewHTML = null;
	state.previewFor = null;
	state.previewBusy = false;
	state.previewError = null;
}

/** togglePreview flips between the raw editor and the core's rendered preview.
 * Switching to preview renders the current text once (and caches it against that
 * text); switching back only flips the flag, keeping the draft untouched. */
export function togglePreview(state: PreviewState): void {
	if (state.preview) {
		state.preview = false;
		state.previewBusy = false;
		state.previewError = null;
		return;
	}
	state.preview = true;
	state.previewError = null;
	const text = state.text;
	if (text.trim() === "") {
		state.previewHTML = "";
		state.previewFor = text;
		state.previewBusy = false;
		return;
	}
	if (state.previewFor === text && state.previewHTML !== null) {
		state.previewBusy = false;
		return;
	}
	state.previewBusy = true;
	state.previewHTML = null;
	state.previewFor = null;
	void renderBBCode(text).then((html) => {
		// Discard a result the field no longer wants: the user flipped back to
		// edit or typed, or the caller reset the field.
		if (!state.preview || state.text !== text) {
			return;
		}
		state.previewBusy = false;
		if (html === null) {
			state.previewError = "Could not render a preview.";
		} else {
			state.previewHTML = html;
			state.previewFor = text;
		}
		request();
	});
}

/** previewBody renders the read-only view: a spinner while the core renders, the
 * rendered HTML once it lands, an empty note, or a failure. The HTML comes from
 * the core's own renderer and is trusted, like every other rendered body the
 * client displays. */
export function previewBody(state: PreviewState, emptyText: string): Mithril.Children {
	if (state.previewBusy) {
		return m("div.bbcode-preview", m(Spinner, { label: "Rendering…" }));
	}
	if (state.previewError !== null) {
		return m("div.bbcode-preview", m(FormError, { message: state.previewError }));
	}
	const html = state.previewHTML ?? "";
	if (html === "") {
		return m("div.bbcode-preview", m("span.muted", emptyText));
	}
	return m("div.bbcode-preview", m("div.bbcode-preview-body", m.trust(html)));
}

export interface PreviewFieldAttrs {
	/** value is the caller-owned draft; oninput updates it synchronously. */
	value: string;
	oninput: (value: string) => void;
	/** label titles the field and the preview toggle. */
	label: string;
	placeholder?: string;
	rows?: number;
	limit?: number;
	disabled?: boolean;
	showFormat?: boolean;
	showModeToggle?: boolean;
	showCount?: boolean;
	ariaLabel?: string;
	/** emptyText is the muted note shown when the preview has nothing to render. */
	emptyText?: string;
	onformat?: ComposerAttrs["onformat"];
	/** resetKey drops the preview when it changes. Callers pass whatever
	 * identifies the draft's subject (a session or room) and bump it after
	 * overwriting the draft in place. */
	resetKey?: unknown;
}

interface PreviewFieldState {
	pv: PreviewState;
	resetKey: unknown;
}

export const PreviewField: Mithril.Component<PreviewFieldAttrs, PreviewFieldState> = {
	oninit: (vnode) => {
		const state = vnode.state as PreviewFieldState;
		state.pv = newPreviewState();
		state.pv.text = vnode.attrs.value;
		state.resetKey = vnode.attrs.resetKey;
	},
	onbeforeupdate: (vnode) => {
		const state = vnode.state as PreviewFieldState;
		if (state.resetKey !== vnode.attrs.resetKey) {
			state.resetKey = vnode.attrs.resetKey;
			resetPreview(state.pv);
		}
		state.pv.text = vnode.attrs.value;
	},
	view: (vnode) => {
		const attrs = vnode.attrs;
		const pv = (vnode.state as PreviewFieldState).pv;
		return m("div.field", [
			m("div.bbcode-field-head", [
				m("span.field-label", attrs.label),
				m(
					"button.button.button-small.button-secondary.bbcode-preview-toggle",
					{
						type: "button",
						"aria-pressed": pv.preview ? "true" : "false",
						onclick: () => togglePreview(pv),
					},
					pv.preview ? "Edit" : "Preview",
				),
			]),
			pv.preview
				? previewBody(pv, attrs.emptyText ?? "No message.")
				: m(Composer, {
						value: attrs.value,
						placeholder: attrs.placeholder,
						rows: attrs.rows,
						limit: attrs.limit,
						disabled: attrs.disabled,
						showFormat: attrs.showFormat,
						showModeToggle: attrs.showModeToggle,
						showCount: attrs.showCount,
						// Dialog-bound: fixed height, no Send, and Escape reaches the
						// Dialog's own close handler rather than blurring the field.
						autoGrow: false,
						showSend: false,
						blurOnEscape: false,
						ariaLabel: attrs.ariaLabel,
						onformat: attrs.onformat,
						oninput: attrs.oninput,
					}),
		]);
	},
};