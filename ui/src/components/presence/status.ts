// status.ts — F-Chat status metadata plus the self-status editor.
// Absorbs status.ts and StatusDialog.ts.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import type { AutoStatus } from "../../api.js";
import { request } from "../../render.js";
import { closeModal } from "../../store/state.js";
import { useDispatch, useStore, useView } from "../../context.js";
import { loadAutoStatus, saveAutoStatus, setStatus } from "../../store/commands.js";
import { Dialog } from "../primitives/dialog.js";

// ==========================================================================
// status.ts
// ==========================================================================
// F-Chat status display metadata, shared by the roster, the self status
// control, and the status dialog. STATUS_OPTIONS is the set a character may
// choose; `crown` is a secret admin-granted status that renders but is never
// selectable.
export interface StatusOption {
	value: string;
	label: string;
	mark: string;
}

/** STATUS_OPTIONS lists the selectable statuses, in menu order. */
export const STATUS_OPTIONS: readonly StatusOption[] = [
	{ value: "online", label: "Online", mark: "🟢" },
	{ value: "looking", label: "Looking", mark: "👀" },
	{ value: "away", label: "Away", mark: "🌙" },
	{ value: "busy", label: "Busy", mark: "🟠" },
	{ value: "dnd", label: "Do not disturb", mark: "⛔" },
	{ value: "idle", label: "Idle", mark: "💤" },
];

/** OFFLINE_MARK is shown for a character known to be offline regardless of the
 * status they held when they left. */
export const OFFLINE_MARK = "⚪";

const STATUS_MARK: Record<string, string> = {
	online: "🟢",
	looking: "👀",
	away: "🌙",
	busy: "🟠",
	dnd: "⛔",
	idle: "💤",
	crown: "🍰",
};

const STATUS_LABEL: Record<string, string> = {
	online: "Online",
	looking: "Looking",
	away: "Away",
	busy: "Busy",
	dnd: "Do not disturb",
	idle: "Idle",
	crown: "Rewarded",
};

// Status strings are a tiny, repeated set; normalize once and reuse the result
// so long lists do not allocate per render.
const marks = new Map<string, string>();
const labels = new Map<string, string>();

export function statusMark(status?: string): string {
	const key = (status ?? "").toLowerCase();
	let mark = marks.get(key);
	if (mark === undefined) {
		mark = STATUS_MARK[key] ?? "";
		marks.set(key, mark);
	}
	return mark;
}

export function statusLabel(status?: string): string {
	const key = (status ?? "").toLowerCase();
	let label = labels.get(key);
	if (label === undefined) {
		label = STATUS_LABEL[key] ?? status ?? "";
		labels.set(key, label);
	}
	return label;
}

// ==========================================================================
// StatusDialog.ts
// ==========================================================================
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
	},
	view: (vnode) => {
		const state = vnode.state as StatusDialogState;
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();

		const session = view.activeSession;
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
			loadAuto(state, session);
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
				m("label.field", [
					m("span.field-label", "Status"),
					m(
						"select",
						{
							value: state.status,
							onchange: (e: Event) => {
								state.status = (e.target as HTMLSelectElement).value;
							},
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
				m("label.field", [
					m("span.field-label", "Status message"),
					m("textarea", {
						rows: 3,
						placeholder: "Say something (BBCode allowed)",
						value: state.text,
						oninput: (e: Event) => {
							state.text = (e.target as HTMLTextAreaElement).value;
						},
					}),
				]),
				autoSection(state, session),
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

/** loadAuto reads the session's saved automatic status into the dialog. A read
 * that finishes after the dialog switched sessions is discarded. */
function loadAuto(state: StatusDialogState, session: string): void {
	state.auto = undefined;
	state.autoBusy = null;
	state.autoError = null;
	state.autoNote = null;
	void loadAutoStatus(session).then((auto) => {
		if (state.seed !== session) {
			return;
		}
		if (auto === undefined) {
			state.auto = null;
			state.autoError = "Could not load the saved status.";
		} else {
			state.auto = auto;
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
			state.auto = { status, message };
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
			state.auto = null;
			state.autoNote = "Automatic status cleared.";
		}
		request();
	});
}

/** autoSection renders the automatic-status controls: the currently saved
 * status, a save action for the dialog's fields, and a clear button. Saving or
 * clearing here never changes the live status. */
function autoSection(
	state: StatusDialogState,
	session: string,
): Mithril.Children {
	const auto = state.auto;
	let saved: Mithril.Children;
	if (auto === undefined) {
		saved = m("p.settings-field-note", "Loading saved status…");
	} else if (auto === null) {
		saved = m("p.settings-field-note", "No automatic status saved.");
	} else {
		saved = m("div.status-auto-saved", [
			m(
				"span.status-auto-value",
				`${statusMark(auto.status)} ${statusLabel(auto.status)}`,
			),
			auto.message !== undefined && auto.message !== ""
				? m("span.status-auto-message.muted", ` — ${auto.message}`)
				: null,
			m(
				"button.button.button-secondary.button-small",
				{
					type: "button",
					disabled: state.autoBusy !== null,
					onclick: () => clearAuto(state, session),
				},
				state.autoBusy === "clear" ? "Clearing…" : "Clear",
			),
		]);
	}
	return m("div.settings-subsection.status-auto", [
		m("span.settings-section-label", "Automatic status on login"),
		m(
			"p.settings-field-note",
			"Saved for your next login; setting the live status does not change it.",
		),
		saved,
		m(
			"button.button.button-secondary.button-small",
			{
				type: "button",
				disabled: state.autoBusy !== null,
				onclick: () => saveAuto(state, session),
			},
			state.autoBusy === "save" ? "Saving…" : "Save for login",
		),
		state.autoError !== null ? m("p.form-error", state.autoError) : null,
		state.autoNote !== null ? m("p.settings-field-note", state.autoNote) : null,
	]);
}
