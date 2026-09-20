// export.ts — the "Export Chatlogs" tab. It composes the shared two-sided
// ConversationPicker with the export form: a pick reports the resolved pair,
// which loads the conversation's coverage, defaults the range to it, and opens
// the artifact as a new-tab GET via LogsExport. The store is not consulted:
// logs are durable and readable for any character, connected or not.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { fetchLogActivityDetail, fetchLogActivityOverview, fetchLogCoverage, logExportURL } from "../../api.js";
import { formatOffset, fromLocalInput, toLocalInput } from "../../lib/format.js";
import { request } from "../../render.js";
import { ActivityBars } from "./activity.js";
import { ConversationPicker } from "./picker.js";
import { Button, FormError } from "../primitives/form.js";
import { logKindLabel } from "./labels.js";
import { DAY_MS } from "./shared.js";
import type { LogActivityDetail, LogActivityOverview, LogActivityScope, LogConvRef, LogCoverage } from "../../transport/protocol.js";

export interface LogsExportAttrs {
	/** label is the chosen conversation's title, or null before a pick. */
	label: string | null;
	kind: LogConvRef["kind"] | null;
	coverage: LogCoverage | null;
	loading: boolean;
	/** from/to are datetime-local values ("" when unset). */
	from: string;
	to: string;
	tzLabel: string;
	ready: boolean;
	rangeError: boolean;
	onFrom: (value: string) => void;
	onTo: (value: string) => void;
	onFullRange: () => void;
	onOpen: () => void;
	/** activity is the overview strip; drill is the currently open day. */
	activity: LogActivityOverview | null;
	activityLoading: boolean;
	drill: LogActivityDetail | null;
	drillLoading: boolean;
	drillDayStartMs: number | null;
	/** fromMs/toMs are the parsed bounds of the current from/to, for shading. */
	fromMs: number | null;
	toMs: number | null;
	/** canToggle offers the scope switch (rooms and channels, not DMs). */
	canToggle: boolean;
	onDrill: (dayStartMs: number) => void;
	onCloseDrill: () => void;
	onSession: (startMs: number, endMs: number) => void;
	onToggleScope: () => void;
}

/** RangeField is one datetime-local bound in the export range. */
interface RangeFieldAttrs {
	label: string;
	value: string;
	disabled: boolean;
	oninput: (value: string) => void;
}

const RangeField: Mithril.Component<RangeFieldAttrs> = {
	view: ({ attrs }) =>
		m("label.field", [
			m("span.field-label", attrs.label),
			m("input", {
				type: "datetime-local",
				value: attrs.value,
				disabled: attrs.disabled,
				oninput: (e: Event) =>
					attrs.oninput((e.target as HTMLInputElement).value),
			}),
		]),
};

export const LogsExport: Mithril.Component<LogsExportAttrs> = {
	view: ({ attrs }) => {
		const c = attrs.coverage;
		const hasHistory = c !== null && c.count > 0;
		return m("section.logs-export", [
			m("div.logs-export-head", [
				m("h3.logs-export-title", "Export"),
				attrs.label === null
					? m("span.logs-hint.muted", "No conversation selected.")
					: m("span.logs-export-target", [
							m("span.logs-item-name", attrs.label),
							attrs.kind !== null
								? m("span.logs-badge", logKindLabel(attrs.kind))
								: null,
						]),
				attrs.loading
					? m("span.logs-hint.muted", "Loading history…")
					: c === null
						? null
						: hasHistory
							? m("span.logs-hint.muted", `${c.count} messages`)
							: m("span.logs-hint.muted", "No history to export."),
			]),
			m(ActivityBars, {
				overview: attrs.activity,
				loading: attrs.activityLoading,
				drill: attrs.drill,
				drillLoading: attrs.drillLoading,
				drillDayStartMs: attrs.drillDayStartMs,
				unitMs: attrs.activity?.unitMs ?? DAY_MS,
				fromMs: attrs.fromMs,
				toMs: attrs.toMs,
				canToggle: attrs.canToggle,
				onDrill: attrs.onDrill,
				onClose: attrs.onCloseDrill,
				onSession: attrs.onSession,
				onToggleScope: attrs.onToggleScope,
			}),
			m("div.logs-range", [
				m(RangeField, {
					label: "From",
					value: attrs.from,
					disabled: !hasHistory,
					oninput: attrs.onFrom,
				}),
				m(RangeField, {
					label: "To",
					value: attrs.to,
					disabled: !hasHistory,
					oninput: attrs.onTo,
				}),
				m(Button, {
					label: "Full range",
					variant: "secondary",
					small: true,
					class: "logs-range-reset",
					disabled: !hasHistory,
					onclick: attrs.onFullRange,
				}),
				m("span.logs-tz", attrs.tzLabel),
			]),
			m(FormError, {
				message: attrs.rangeError ? '"To" must not precede "From".' : null,
			}),
			m("div.logs-export-actions", [
				m(Button, {
					label: "Open log in new tab",
					disabled: !attrs.ready,
					title: attrs.ready ? undefined : "Choose a conversation and a valid range",
					onclick: attrs.onOpen,
				}),
			]),
		]);
	},
};

interface State {
	character: string | null;
	conversation: LogConvRef | null;
	coverage: LogCoverage | null;
	coverageLoading: boolean;
	activity: LogActivityOverview | null;
	activityLoading: boolean;
	drill: LogActivityDetail | null;
	drillLoading: boolean;
	drillDayStartMs: number | null;
	/** scopeOverride forces a scope; null resolves automatically per conversation. */
	scopeOverride: LogActivityScope | null;
	from: string;
	to: string;
	tz: number;
	/** request sequence guards drop responses for a superseded request. */
	covSeq: number;
	activitySeq: number;
	drillSeq: number;
}

export const ExportChatlogs: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as State;
		state.character = null;
		state.conversation = null;
		state.coverage = null;
		state.coverageLoading = false;
		state.activity = null;
		state.activityLoading = false;
		state.drill = null;
		state.drillLoading = false;
		state.drillDayStartMs = null;
		state.scopeOverride = null;
		state.from = "";
		state.to = "";
		state.tz = -new Date().getTimezoneOffset();
		state.covSeq = 0;
		state.activitySeq = 0;
		state.drillSeq = 0;
	},
	view: (vnode) => {
		const state = vnode.state as State;
		const selection = state.character !== null && state.conversation !== null;
		const fromMs = fromLocalInput(state.from);
		const toStartMs = fromLocalInput(state.to);
		// The export's `to` is inclusive; cover the whole chosen minute.
		const toMs = toEndLocalInput(state.to);
		const hasHistory = state.coverage !== null && state.coverage.count > 0;
		const rangeError =
			fromMs !== null && toStartMs !== null && toStartMs < fromMs;
		const ready =
			selection && hasHistory && fromMs !== null && toMs !== null && !rangeError;

		return m("div.export-chatlogs", [
			m(ConversationPicker, {
				character: state.character,
				conversation: state.conversation,
				onChange: (session, conv) => selectConversation(state, session, conv),
			}),
			m(LogsExport, {
				label: state.conversation === null ? null : state.conversation.name,
				kind: state.conversation === null ? null : state.conversation.kind,
				coverage: state.coverage,
				loading: state.coverageLoading,
				activity: state.activity,
				activityLoading: state.activityLoading,
				drill: state.drill,
				drillLoading: state.drillLoading,
				drillDayStartMs: state.drillDayStartMs,
				fromMs,
				toMs,
				canToggle: state.conversation !== null && state.conversation.kind !== "dm",
				from: state.from,
				to: state.to,
				tzLabel: formatOffset(state.tz),
				ready,
				rangeError,
				onFrom: (value: string) => {
					state.from = value;
				},
				onTo: (value: string) => {
					state.to = value;
				},
				onFullRange: () => fullRange(state),
				onOpen: () => openExport(state, fromMs, toMs, rangeError),
				onDrill: (dayStartMs: number) => drillInto(state, dayStartMs),
				onCloseDrill: () => closeDrill(state),
				onSession: (startMs: number, endMs: number) =>
					applySession(state, startMs, endMs),
				onToggleScope: () => toggleScope(state),
			}),
		]);
	},
};

/** selectConversation adopts the picker's resolved pair and reloads coverage. */
function selectConversation(
	state: State,
	session: string | null,
	conv: LogConvRef | null,
): void {
	state.character = session;
	state.conversation = conv;
	clearCoverage(state);
	if (session !== null && conv !== null) {
		loadCoverage(state);
	}
}

/** loadCoverage fetches the selected conversation's span and defaults the range
 * to it. */
function loadCoverage(state: State): void {
	const session = state.character;
	const conv = state.conversation;
	if (session === null || conv === null) {
		return;
	}
	state.coverage = null;
	state.coverageLoading = true;
	clearActivity(state);
	clearRange(state);
	const seq = ++state.covSeq;
	void fetchLogCoverage(session, conv.kind, conv.id).then((coverage) => {
		if (seq !== state.covSeq) {
			return;
		}
		state.coverageLoading = false;
		if (coverage !== null) {
			state.coverage = coverage;
			if (coverage.count > 0) {
				state.from = toLocalInput(coverage.firstMs);
				state.to = toLocalInput(coverage.lastMs);
				loadActivity(state);
			}
		}
		request();
	});
}

/** loadActivity fetches the day overview for the selected conversation. */
function loadActivity(state: State): void {
	const session = state.character;
	const conv = state.conversation;
	if (session === null || conv === null) {
		return;
	}
	state.activity = null;
	state.activityLoading = true;
	const seq = ++state.activitySeq;
	void fetchLogActivityOverview(
		session,
		conv.kind,
		conv.id,
		state.tz,
		state.scopeOverride ?? "auto",
	).then((overview) => {
		if (seq !== state.activitySeq) {
			return;
		}
		state.activityLoading = false;
		state.activity = overview;
		request();
	});
}

/** drillInto segments the clicked local day into activity sessions. */
function drillInto(state: State, dayStartMs: number): void {
	const session = state.character;
	const conv = state.conversation;
	if (session === null || conv === null) {
		return;
	}
	const unit = state.activity?.unitMs ?? 86_400_000;
	const fromMs = dayStartMs;
	const toMs = dayStartMs + unit - 1;
	state.drill = null;
	state.drillLoading = true;
	state.drillDayStartMs = dayStartMs;
	const seq = ++state.drillSeq;
	void fetchLogActivityDetail(
		session,
		conv.kind,
		conv.id,
		state.tz,
		fromMs,
		toMs,
		state.activity?.scope ?? "auto",
	).then((detail) => {
		if (seq !== state.drillSeq) {
			return;
		}
		state.drillLoading = false;
		state.drill = detail;
		request();
	});
}

/** closeDrill returns the strip to the day overview. */
function closeDrill(state: State): void {
	state.drill = null;
	state.drillLoading = false;
	state.drillDayStartMs = null;
	state.drillSeq++;
}

/** toggleScope flips between the conversation and self activity views. */
function toggleScope(state: State): void {
	const current = state.activity?.scope ?? "conversation";
	state.scopeOverride = current === "self" ? "conversation" : "self";
	closeDrill(state);
	loadActivity(state);
}

/** applySession sets the export range to a session's span. */
function applySession(state: State, startMs: number, endMs: number): void {
	state.from = toLocalInput(startMs);
	state.to = toLocalInput(endMs);
}

/** openExport opens the streamed artifact in a new tab. */
function openExport(
	state: State,
	fromMs: number | null,
	toMs: number | null,
	rangeError: boolean,
): void {
	const session = state.character;
	const conv = state.conversation;
	if (
		session === null ||
		conv === null ||
		fromMs === null ||
		toMs === null ||
		rangeError
	) {
		return;
	}
	window.open(
		logExportURL({ session, kind: conv.kind, id: conv.id }, fromMs, toMs, state.tz),
		"_blank",
	);
}

function clearCoverage(state: State): void {
	state.coverage = null;
	state.coverageLoading = false;
	state.covSeq++;
	state.scopeOverride = null;
	clearActivity(state);
	clearRange(state);
}

/** clearActivity drops the overview and any open drilldown. */
function clearActivity(state: State): void {
	state.activity = null;
	state.activityLoading = false;
	state.drill = null;
	state.drillLoading = false;
	state.drillDayStartMs = null;
	state.activitySeq++;
	state.drillSeq++;
}

function clearRange(state: State): void {
	state.from = "";
	state.to = "";
}

function fullRange(state: State): void {
	const c = state.coverage;
	if (c !== null && c.count > 0) {
		state.from = toLocalInput(c.firstMs);
		state.to = toLocalInput(c.lastMs);
	}
}

/** toEndLocalInput parses a datetime-local value as the last millisecond of its
 * minute. The export's end bound is inclusive, so a minute-granular pick must
 * cover the whole minute; otherwise messages later in that minute are dropped. */
function toEndLocalInput(value: string): number | null {
	const start = fromLocalInput(value);
	return start === null ? null : start + 59_999;
}