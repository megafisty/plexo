// statusDialog.ts — the self-status editor modal. Split out of status.ts so the
// shared status metadata stays a leaf: presence/character.ts imports that
// metadata, and this dialog renders FeaturedCharacter, so keeping both in one
// file would cycle.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { renderBBCode, type AutoStatus } from "../../api.js";
import { request } from "../../render.js";
import { closeModal, openCommand, type View } from "../../store/state.js";
import { useDispatch, useStore, useView } from "../../context.js";
import { loadAutoStatus, saveAutoStatus, setStatus } from "../../store/commands.js";
import { Dialog } from "../primitives/dialog.js";
import { FormError, Spinner } from "../primitives/form.js";
import { Composer, type ComposerFormat, type ComposerPalette } from "../composer/composer.js";
import { FeaturedCharacter } from "./character.js";
import { STATUS_OPTIONS, statusLabel } from "./status.js";

// StatusDialog: edit the character's own status and status message. The status
// message is raw BBCode and travels to the core unchanged; the core renders it
// for display and remembers the raw text ephemerally so this dialog can prefill
// it. The dialog also manages the character's saved automatic status, which the
// core re-emits on every login. Setting the live status never changes the saved
// one; save and clear are explicit actions. Mounted by the Chatspace shell as
// the modal slot's "status" dialog.
//
// `crown` is a moderator-granted status: it is displayed when the server sends
// it but is deliberately absent from the selectable options, so the dialog can
// never emit it. A character holding it falls back to Online and is told why.

interface StatusDialogState {
	status: string;
	text: string;
	/** seed is the session the fields were seeded from; a switch reseeds. */
	seed: string | null;
	/** auto is the saved automatic status: undefined while loading, null when
	 * none is saved. */
	auto: AutoStatus | null | undefined;
	/** autoBusy names the in-flight automatic-status action, or null. */
	autoBusy: "save" | "clear" | null;
	autoError: string | null;
	/** autoNote is transient success feedback for the automatic-status actions. */
	autoNote: string | null;
	/** autoHTML is the rendered HTML for the saved automatic status message, or
	 * "" when there is no message (or the render failed). Null while in flight. */
	autoHTML: string | null;
	/** autoHTMLFor is the raw message autoHTML was rendered from, so an unchanged
	 * message is not re-rendered on a save or redraw. */
	autoHTMLFor: string | null;
	/** pendingAuto is the session whose saved status view queued a fetch for;
	 * drained in oncreate/onupdate so a render pass never starts async work. */
	pendingAuto?: string;
	/** preview flips the message field between the raw editor and the core's
	 * rendered HTML. */
	preview: boolean;
	/** previewHTML is the last rendered fragment, or null before/while loading. */
	previewHTML: string | null;
	/** previewFor is the raw text previewHTML was rendered from, so an unchanged
	 * message is not re-fetched when the user flips back to preview. */
	previewFor: string | null;
	previewBusy: boolean;
	previewError: string | null;
	/** refView is the live View, refreshed each render so the stable onformat
	 * callback below can open the palette slot. */
	refView?: View;
	/** onformat is reference-stable (the Composer is pure) and opens the shared
	 * command palette layered over this dialog. */
	onformat: (
		command: ComposerPalette,
		apply: ComposerFormat,
		selection: string,
		start?: string,
	) => void;
}

export const StatusDialog: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as StatusDialogState;
		state.status = "online";
		state.text = "";
		state.seed = null;
		state.auto = undefined;
		state.autoBusy = null;
		state.autoError = null;
		state.autoNote = null;
		state.autoHTML = null;
		state.autoHTMLFor = null;
		state.preview = false;
		state.previewHTML = null;
		state.previewFor = null;
		state.previewBusy = false;
		state.previewError = null;
		state.onformat = (command, apply, selection, start) => {
			const view = state.refView;
			if (view === undefined) {
				return;
			}
			openCommand(view, command, apply, selection, start);
		};
	},
	oncreate: (vnode) => runPendingAuto(vnode),
	onupdate: (vnode) => runPendingAuto(vnode),
	view: (vnode) => {
		const state = vnode.state as StatusDialogState;
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();

		const session = view.activeSession;
		state.refView = view;
		const sess = session === null ? undefined : store.sessions[session];
		if (session === null || sess === undefined) {
			return null;
		}
		const self = store.characters[session] ?? sess.self;
		const current = self?.status ?? "online";
		const selectable = STATUS_OPTIONS.some((o) => o.value === current);
		// Seed once per session so a redraw mid-edit does not clobber the draft.
		if (state.seed !== session) {
			state.status = selectable ? current : "online";
			state.text = sess.selfStatusText ?? "";
			state.seed = session;
			resetPreview(state);
			// Show the loading state now; the fetch itself is queued for after the
			// render (see runPendingAuto).
			resetAuto(state);
			state.pendingAuto = session;
		}

		const close = (): void => {
			closeModal(view);
		};
		return m(
			Dialog,
			{
				title: `Status for ${session}`,
				onClose: close,
				class: "status-dialog",
			},
			[
				!selectable
					? m(
							"p.muted.status-reserved",
							`Your current status (${statusLabel(current)}) is granted by a moderator and cannot be set here.`,
						)
					: null,
				m(StatusSelect, {
					value: state.status,
					onSelect: (value) => {
						state.status = value;
					},
				}),
				m("div.field", [
					m("div.status-message-head", [
						m("span.field-label", "Status message"),
						m(
							"button.button.button-small.button-secondary.status-preview-toggle",
							{
								type: "button",
								"aria-pressed": state.preview ? "true" : "false",
								onclick: () => togglePreview(state),
							},
							state.preview ? "Edit" : "Preview",
						),
					]),
					state.preview
						? statusPreview(state)
						: m(Composer, {
								value: state.text,
								placeholder: "Say something (BBCode allowed)",
								rows: 6,
								autoGrow: false,
								showModeToggle: false,
								showSend: false,
								showCount: false,
								// The Dialog owns Escape; do not blur out of it.
								blurOnEscape: false,
								ariaLabel: "Status message",
								onformat: state.onformat,
								oninput: (value: string) => {
									state.text = value;
								},
							}),
				]),
				m(AutoStatusSection, {
					auto: state.auto,
					autoHTML: state.autoHTML,
					name: session,
					gender: self?.gender,
					busy: state.autoBusy,
					error: state.autoError,
					note: state.autoNote,
					onSave: () => saveAuto(state, session),
					onCopy: () => copyAutoToDraft(state),
					onClear: () => clearAuto(state, session),
				}),
				m("div.dialog-actions", [
					m(
						"button.button.button-secondary",
						{ type: "button", onclick: close },
						"Cancel",
					),
					m(
						"button.button",
						{
							type: "button",
							onclick: () => {
								setStatus(store, view, dispatch, session, state.status, state.text);
							},
						},
						"Set status",
					),
				]),
			],
		);
	},
};

/** runPendingAuto starts the saved-status fetch view queued. Kept out of the
 * render pass; oncreate covers the first mount (onupdate does not run then),
 * onupdate a session switch while the dialog stays mounted. */
function runPendingAuto(vnode: Mithril.Vnode): void {
	const state = vnode.state as StatusDialogState;
	const session = state.pendingAuto;
	if (session === undefined) {
		return;
	}
	state.pendingAuto = undefined;
	loadAuto(state, session);
}

/** resetAuto clears the automatic-status fields to the loading state. */
function resetAuto(state: StatusDialogState): void {
	state.auto = undefined;
	state.autoBusy = null;
	state.autoError = null;
	state.autoNote = null;
	state.autoHTML = null;
	state.autoHTMLFor = null;
}

/** resetPreview returns the message field to editing and drops any rendered
 * fragment, so a session switch never shows the previous character's preview. */
function resetPreview(state: StatusDialogState): void {
	state.preview = false;
	state.previewHTML = null;
	state.previewFor = null;
	state.previewBusy = false;
	state.previewError = null;
}

/** copyAutoToDraft loads the saved automatic status message back into the
 * editor, overwriting the draft, and returns to edit mode so the copied text is
 * visible. Only the message round-trips: the status dropdown is left alone.
 * A no-op when nothing is saved. */
export function copyAutoToDraft(state: StatusDialogState): void {
	const auto = state.auto;
	if (auto === undefined || auto === null) {
		return;
	}
	resetPreview(state);
	state.text = auto.message ?? "";
}

/** togglePreview flips the message field between the raw editor and the core's
 * rendered preview. Switching to preview renders the current text once (and
 * caches it against that text); switching back only flips the flag, keeping the
 * draft untouched. */
export function togglePreview(state: StatusDialogState): void {
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
		// edit, typed, or the dialog seeded another session.
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

/** statusPreview renders the read-only message view: a spinner while the core
 * renders, the rendered HTML once it lands, or a failure note. The HTML comes
 * from the core's own renderer and is trusted, like every other rendered body
 * the client displays. */
function statusPreview(state: StatusDialogState): Mithril.Children {
	if (state.previewBusy) {
		return m("div.status-message-preview", m(Spinner, { label: "Rendering…" }));
	}
	if (state.previewError !== null) {
		return m(
			"div.status-message-preview",
			m(FormError, { message: state.previewError }),
		);
	}
	const html = state.previewHTML ?? "";
	if (html === "") {
		return m(
			"div.status-message-preview",
			m("span.muted", "No status message."),
		);
	}
	return m(
		"div.status-message-preview",
		m("div.status-message-body", m.trust(html)),
	);
}

/** applyAuto stores the saved automatic status and queues the one-off render of
 * its message through the core's preview endpoint, so the section can show the
 * character as it will look at login. The fragment is cached against the raw
 * message, so a redraw, a re-save of the same text, or a reload does not
 * re-request it. A failed render leaves autoHTML empty, so the FeaturedCharacter
 * falls back to the status label. */
export function applyAuto(state: StatusDialogState, auto: AutoStatus | null): void {
	state.auto = auto;
	const message = auto?.message ?? "";
	if (message.trim() === "") {
		state.autoHTML = "";
		state.autoHTMLFor = message;
		return;
	}
	if (state.autoHTMLFor === message && state.autoHTML !== null) {
		return;
	}
	state.autoHTML = null;
	state.autoHTMLFor = null;
	const seed = state.seed;
	void renderBBCode(message).then((html) => {
		// Discard a render for a session or auto status that has since moved on.
		if (state.seed !== seed || (state.auto?.message ?? "") !== message) {
			return;
		}
		state.autoHTML = html ?? "";
		state.autoHTMLFor = message;
		request();
	});
}

/** loadAuto reads the session's saved automatic status into the dialog. A read
 * that finishes after the dialog switched sessions is discarded. */
function loadAuto(state: StatusDialogState, session: string): void {
	void loadAutoStatus(session).then((auto) => {
		if (state.seed !== session) {
			return;
		}
		if (auto === undefined) {
			applyAuto(state, null);
			state.autoError = "Could not load the saved status.";
		} else {
			applyAuto(state, auto);
		}
		request();
	});
}

/** saveAuto stores the dialog's current status and message as the automatic
 * status. It never changes the character's live status. */
function saveAuto(state: StatusDialogState, session: string): void {
	const status = state.status;
	const message = state.text;
	state.autoBusy = "save";
	state.autoError = null;
	state.autoNote = null;
	void saveAutoStatus(session, { status, message }).then((err) => {
		state.autoBusy = null;
		if (err !== null) {
			state.autoError = err;
		} else {
			applyAuto(state, { status, message });
			state.autoNote = "Saved for login.";
		}
		request();
	});
}

/** clearAuto deletes the saved automatic status immediately, leaving the
 * character's live status alone. */
function clearAuto(state: StatusDialogState, session: string): void {
	state.autoBusy = "clear";
	state.autoError = null;
	state.autoNote = null;
	void saveAutoStatus(session, null).then((err) => {
		state.autoBusy = null;
		if (err !== null) {
			state.autoError = err;
		} else {
			applyAuto(state, null);
			state.autoNote = "Automatic status cleared.";
		}
		request();
	});
}

/** StatusSelect is the selectable-status dropdown. `crown` is deliberately
 * absent from STATUS_OPTIONS, so the dialog can never emit it. */
interface StatusSelectAttrs {
	value: string;
	onSelect: (value: string) => void;
}

const StatusSelect: Mithril.Component<StatusSelectAttrs> = {
	view: ({ attrs }) =>
		m("label.field", [
			m("span.field-label", "Status"),
			m(
				"select",
				{
					value: attrs.value,
					onchange: (e: Event) =>
						attrs.onSelect((e.target as HTMLSelectElement).value),
				},
				STATUS_OPTIONS.map((o) =>
					m(
						"option",
						{ key: o.value, value: o.value },
						`${o.mark} ${o.label}`.trim(),
					),
				),
			),
		]),
};

/** AutoStatusSection renders the automatic-status controls: the save/copy
 * action pair, the currently saved status, and a clear button. Saving, copying,
 * or clearing here never changes the live status. */
interface AutoStatusSectionAttrs {
	/** auto is the saved status: undefined while loading, null when none. */
	auto: AutoStatus | null | undefined;
	/** autoHTML is the saved message rendered to HTML, or "" when there is none
	 * (or the render failed). Null while it is in flight. */
	autoHTML: string | null;
	/** name and gender label the preview row as the active session. */
	name: string;
	gender?: string;
	busy: "save" | "clear" | null;
	error: string | null;
	note: string | null;
	onSave: () => void;
	/** onCopy loads the saved message back into the editor. */
	onCopy: () => void;
	onClear: () => void;
}

const AutoStatusSection: Mithril.Component<AutoStatusSectionAttrs> = {
	view: ({ attrs }) => {
		const auto = attrs.auto;
		let saved: Mithril.Children;
		if (auto === undefined) {
			saved = m("p.settings-field-note", "Loading saved status…");
		} else if (auto === null) {
			saved = m("p.settings-field-note", "No automatic status saved.");
		} else {
			saved = m("div.status-auto-saved", [
				m(FeaturedCharacter, {
					character: {
						name: attrs.name,
						gender: attrs.gender,
						status: auto.status,
						statusMsg: attrs.autoHTML ?? undefined,
						online: true,
					},
					class: "status-auto-character",
				}),
				m(
					"button.button.button-secondary.button-small",
					{
						type: "button",
						disabled: attrs.busy !== null,
						onclick: attrs.onClear,
					},
					attrs.busy === "clear" ? "Clearing…" : "Clear",
				),
			]);
		}
		return m("div.settings-subsection.status-auto", [
			m("span.settings-section-label", "Automatic status on login"),
			m(
				"p.settings-field-note",
				"Saved for your next login; setting the live status does not change it.",
			),
			m("div.status-auto-actions", [
				m(
					"button.button.button-secondary.button-small",
					{
						type: "button",
						disabled: attrs.busy !== null,
						onclick: attrs.onSave,
						title: "Save the current status and message for login",
					},
					attrs.busy === "save" ? "Saving…" : "Save ↓",
				),
				m(
					"button.button.button-secondary.button-small",
					{
						type: "button",
						// Nothing to pull back while the saved status is loading or absent.
						disabled:
							attrs.busy !== null ||
							attrs.auto === undefined ||
							attrs.auto === null,
						onclick: attrs.onCopy,
						title: "Copy the saved message into the editor",
					},
					"Copy ↑",
				),
			]),
			saved,
			attrs.error !== null ? m("p.form-error", attrs.error) : null,
			attrs.note !== null ? m("p.settings-field-note", attrs.note) : null,
		]);
	},
};

