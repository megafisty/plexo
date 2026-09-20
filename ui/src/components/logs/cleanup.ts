// cleanup.ts — the "Cleanup" tab of the Logs dialog. Three maintenance tools:
// prune by age, delete one conversation (entirely or by age), and sweep one-off
// DMs. Every tool is a two-step preview then confirm: the preview reports the
// conversations, messages, body bytes, and warpmarks at stake, and only an
// explicit delete applies it. The core vacuums afterward when enough space was
// freed. Deleting needs no live session: history is durable for any character.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { postLogCleanup } from "../../api.js";
import { useView } from "../../context.js";
import { request } from "../../render.js";
import { pushToast, type View } from "../../store/state.js";
import { FormError, Button } from "../primitives/form.js";
import { ConversationPicker } from "./picker.js";
import { formatBytes } from "./shared.js";
import type { LogCleanupRequest, LogCleanupResult, LogConvRef } from "../../transport/protocol.js";

/** CleanupPreview is one tool's transient preview/apply state. */
interface CleanupPreview {
	result: LogCleanupResult | null;
	busy: boolean;
	error: string | null;
}

interface CleanupState {
	ageDays: number;
	age: CleanupPreview;
	convSession: string | null;
	conv: LogConvRef | null;
	/** convAge trims the conversation by age instead of deleting it whole. */
	convAge: boolean;
	convDays: number;
	convPreview: CleanupPreview;
	dmDays: number;
	dmMax: number;
	dm: CleanupPreview;
}

function emptyCleanupPreview(): CleanupPreview {
	return { result: null, busy: false, error: null };
}

function resetCleanupPreview(slot: CleanupPreview): void {
	slot.result = null;
	slot.error = null;
}

export const Cleanup: Mithril.Component<{}, CleanupState> = {
	oninit: (vnode) => {
		const state = vnode.state;
		state.ageDays = 90;
		state.age = emptyCleanupPreview();
		state.convSession = null;
		state.conv = null;
		state.convAge = false;
		state.convDays = 90;
		state.convPreview = emptyCleanupPreview();
		state.dmDays = 7;
		state.dmMax = 5;
		state.dm = emptyCleanupPreview();
	},
	view: (vnode) => {
		const state = vnode.state;
		return m("div.logs-cleanup", [
			m(
				"p.logs-hint.muted",
				"Cleanup permanently deletes persisted history. A preview shows what will go before anything is removed.",
			),
			ageCleanupCard(vnode),
			conversationCleanupCard(vnode),
			dmCleanupCard(vnode),
		]);
	},
};

function ageCleanupCard(vnode: Mithril.Vnode<{}, CleanupState>): Mithril.Children {
	const state = vnode.state;
	const view = useView();
	return cleanupCard({
		title: "Old messages",
		description:
			"Delete every persisted message older than this, across all conversations.",
		controls: [
			numberField("Older than (days)", state.ageDays, 1, (value) => {
				state.ageDays = value;
				resetCleanupPreview(state.age);
			}),
		],
		preview: state.age,
		build: () => ({ op: "age", days: state.ageDays, apply: false }),
		onPreview: (req) => void previewCleanup(view, state.age, req),
		onDelete: (req) => void applyCleanup(view, state.age, req),
	});
}

function conversationCleanupCard(
	vnode: Mithril.Vnode<{}, CleanupState>,
): Mithril.Children {
	const state = vnode.state;
	const view = useView();
	const controls: Mithril.Children = [
		m(ConversationPicker, {
			character: state.convSession,
			conversation: state.conv,
			onChange: (session, conv) => {
				state.convSession = session;
				state.conv = conv;
				resetCleanupPreview(state.convPreview);
			},
		}),
		m("label.checkbox-field", [
			m("input", {
				type: "checkbox",
				checked: state.convAge,
				onchange: (e: Event) => {
					state.convAge = (e.target as HTMLInputElement).checked;
					resetCleanupPreview(state.convPreview);
				},
			}),
			m("span", "Only messages older than"),
			state.convAge
				? m("input.logs-cleanup-days", {
						type: "number",
						min: "1",
						value: String(state.convDays),
						oninput: (e: InputEvent) => {
							const n = Number.parseInt(
								(e.target as HTMLInputElement).value,
								10,
							);
							if (!Number.isNaN(n)) {
								state.convDays = n;
								resetCleanupPreview(state.convPreview);
							}
						},
					})
				: null,
			state.convAge ? m("span.muted", "days") : null,
		]),
	];
	return cleanupCard({
		title: "One conversation",
		description:
			"Delete one channel, room, or DM — entirely, or only its older messages.",
		controls,
		preview: state.convPreview,
		disabled: state.convSession === null || state.conv === null,
		build: () => ({
			op: "conversation",
			session: state.convSession ?? undefined,
			kind: state.conv?.kind,
			id: state.conv?.id,
			days: state.convAge ? state.convDays : 0,
			apply: false,
		}),
		onPreview: (req) => void previewCleanup(view, state.convPreview, req),
		onDelete: (req) => void applyCleanup(view, state.convPreview, req),
	});
}

function dmCleanupCard(vnode: Mithril.Vnode<{}, CleanupState>): Mithril.Children {
	const state = vnode.state;
	const view = useView();
	return cleanupCard({
		title: "One-off DMs",
		description:
			"Delete DM conversations with no activity for the given age that hold fewer than the given number of messages.",
		controls: [
			m("div.logs-cleanup-inline", [
				numberField("Inactive for (days)", state.dmDays, 1, (value) => {
					state.dmDays = value;
					resetCleanupPreview(state.dm);
				}),
				numberField("Fewer than (messages)", state.dmMax, 1, (value) => {
					state.dmMax = value;
					resetCleanupPreview(state.dm);
				}),
			]),
		],
		preview: state.dm,
		build: () => ({
			op: "dms",
			days: state.dmDays,
			maxEntries: state.dmMax,
			apply: false,
		}),
		onPreview: (req) => void previewCleanup(view, state.dm, req),
		onDelete: (req) => void applyCleanup(view, state.dm, req),
	});
}

interface CleanupCardAttrs {
	title: string;
	description: string;
	controls: Mithril.Children;
	preview: CleanupPreview;
	/** disabled suppresses the preview until the inputs are complete. */
	disabled?: boolean;
	build: () => LogCleanupRequest;
	onPreview: (req: LogCleanupRequest) => void;
	onDelete: (req: LogCleanupRequest) => void;
}

/** cleanupCard is one tool's shell: a titled form, then either a Preview button
 * or the preview's summary and the confirm/cancel pair. It owns no state; the
 * caller's CleanupPreview drives it. */
function cleanupCard(attrs: CleanupCardAttrs): Mithril.Children {
	const result = attrs.preview.result;
	const disabled = attrs.disabled === true;
	return m("section.logs-cleanup-card", [
		m("h3.logs-cleanup-title", attrs.title),
		m("p.logs-hint.muted", attrs.description),
		m("div.logs-cleanup-controls", attrs.controls),
		result === null
			? m("div.logs-cleanup-actions", [
					m(Button, {
						label: "Preview",
						variant: "secondary",
						small: true,
						busy: attrs.preview.busy,
						busyLabel: "Checking…",
						disabled,
						onclick: () => attrs.onPreview(attrs.build()),
					}),
			  ])
			: m("div.logs-cleanup-confirm", [
					m("p.logs-cleanup-summary", cleanupSummary(result)),
					m("div.logs-cleanup-actions", [
						m(Button, {
							label: cleanupDeleteLabel(result),
							small: true,
							busy: attrs.preview.busy,
							busyLabel: "Deleting…",
							disabled: result.entries === 0,
							onclick: () => attrs.onDelete(attrs.build()),
						}),
						m(Button, {
							label: "Cancel",
							variant: "secondary",
							small: true,
							disabled: attrs.preview.busy,
							onclick: () => {
								resetCleanupPreview(attrs.preview);
								request();
							},
						}),
					]),
			  ]),
		m(FormError, { message: attrs.preview.error }),
	]);
}

/** numberField is a labelled integer input clamped by min. */
function numberField(
	label: string,
	value: number,
	min: number,
	oninput: (value: number) => void,
): Mithril.Children {
	return m("label.field", [
		m("span.field-label", label),
		m("input", {
			type: "number",
			min: String(min),
			value: String(value),
			oninput: (e: InputEvent) => {
				const n = Number.parseInt((e.target as HTMLInputElement).value, 10);
				if (!Number.isNaN(n)) {
					oninput(n);
				}
			},
		}),
	]);
}

/** previewCleanup fetches a tool's impact without deleting. */
async function previewCleanup(
	view: View,
	slot: CleanupPreview,
	req: LogCleanupRequest,
): Promise<void> {
	slot.busy = true;
	slot.error = null;
	request();
	const res = await postLogCleanup({ ...req, apply: false });
	slot.busy = false;
	if (res.ok) {
		slot.result = res.result;
	} else {
		slot.error = res.error;
	}
	request();
}

/** applyCleanup performs the previewed deletion, then toasts the outcome. */
async function applyCleanup(
	view: View,
	slot: CleanupPreview,
	req: LogCleanupRequest,
): Promise<void> {
	slot.busy = true;
	slot.error = null;
	request();
	const res = await postLogCleanup({ ...req, apply: true });
	slot.busy = false;
	if (!res.ok) {
		slot.error = res.error;
		request();
		return;
	}
	slot.result = null;
	pushToast(view, cleanupToast(res.result));
	request();
}

/** cleanupSummary describes a preview: what is at stake and what is lost. */
function cleanupSummary(r: LogCleanupResult): string {
	let s = `${r.conversations} ${r.conversations === 1 ? "conversation" : "conversations"} · ${r.entries} ${r.entries === 1 ? "message" : "messages"}`;
	if (r.bodyBytes > 0) {
		s += ` · ~${formatBytes(r.bodyBytes)}`;
	}
	if (r.warpmarks > 0) {
		s += ` · ${r.warpmarks} ${r.warpmarks === 1 ? "warpmark" : "warpmarks"} will be lost`;
	}
	return s;
}

/** cleanupDeleteLabel names the confirm action by its size. */
function cleanupDeleteLabel(r: LogCleanupResult): string {
	if (r.entries === 0) {
		return "Nothing to delete";
	}
	return `Delete ${r.entries} ${r.entries === 1 ? "message" : "messages"}`;
}

/** cleanupToast summarizes an applied cleanup for the toast host. */
function cleanupToast(r: LogCleanupResult): string {
	let s = `${r.entries} ${r.entries === 1 ? "message" : "messages"} deleted`;
	if (r.warpmarks > 0) {
		s += `, ${r.warpmarks} ${r.warpmarks === 1 ? "warpmark" : "warpmarks"}`;
	}
	if (r.vacuumed && r.bytesReclaimed > 0) {
		s += `; reclaimed ${formatBytes(r.bytesReclaimed)}`;
	}
	return s;
}