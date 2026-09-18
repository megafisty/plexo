// status.ts — F-Chat status metadata plus the self-status editor.
// Absorbs status.ts and StatusDialog.ts.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { closeModal } from "../../store/state.js";
import { useDispatch, useStore, useView } from "../../context.js";
import { setStatus } from "../../store/commands.js";
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
// it. It is never re-emitted on login. Mounted by the Chatspace shell as the
// modal slot's "status" dialog.
//
// `crown` is a moderator-granted status: it is displayed when the server sends
// it but is deliberately absent from the selectable options, so the dialog can
// never emit it. A character holding it falls back to Online and is told why.

interface StatusDialogState {
	status: string;
	text: string;
	/** seed is the session the fields were seeded from; a switch reseeds. */
	seed: string | null;
}

export const StatusDialog: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as StatusDialogState;
		state.status = "online";
		state.text = "";
		state.seed = null;
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
