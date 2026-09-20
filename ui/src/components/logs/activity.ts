// activity.ts — the export panel's range-picking strip. The overview draws one
// bar per local day (height = message count, shaded inside the chosen range);
// clicking a day drills into a second strip of that day's activity sessions,
// where roleplay stretches are accented and each block maps to an export range.
// Presentational: the shell owns the data and the selection.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { pad2 } from "../../lib/format.js";
import { DAY_MS, formatBytes } from "./shared.js";
import { Button } from "../primitives/form.js";
import type { LogActivityDetail, LogActivityOverview, LogActivityScope, LogParticipation, LogSession } from "../../transport/protocol.js";

export interface ActivityBarsAttrs {
	overview: LogActivityOverview | null;
	loading: boolean;
	drill: LogActivityDetail | null;
	drillLoading: boolean;
	drillDayStartMs: number | null;
	/** unitMs is the overview bucket width (one day). */
	unitMs: number;
	/** fromMs/toMs bound the selected export range, for shading. */
	fromMs: number | null;
	toMs: number | null;
	/** canToggle offers the conversation/self scope switch (not for DMs). */
	canToggle: boolean;
	onDrill: (dayStartMs: number) => void;
	onClose: () => void;
	onSession: (startMs: number, endMs: number) => void;
	onToggleScope: () => void;
}

export const ActivityBars: Mithril.Component<ActivityBarsAttrs> = {
	view: ({ attrs }) => {
		if (attrs.loading) {
			return m("div.logs-activity", m("p.logs-hint.muted", "Loading activity…"));
		}
		const ov = attrs.overview;
		if (ov === null) {
			return null;
		}
		const max = Math.max(1, ...ov.buckets.map((b) => b.count));
		const unit = attrs.unitMs > 0 ? attrs.unitMs : DAY_MS;
		// Self scope leads with "posting more than usual": highlight days above a
		// robust (median + scaled MAD) baseline.
		const peak =
			ov.scope === "self" ? peakThreshold(ov.buckets.map((b) => b.count)) : Number.POSITIVE_INFINITY;
		const bars = ov.buckets.map((b) => {
			const inRange =
				attrs.fromMs !== null &&
				attrs.toMs !== null &&
				b.startMs <= attrs.toMs &&
				b.startMs + unit > attrs.fromMs;
			const classes = ["logs-day"];
			if (b.count === 0) classes.push("is-empty");
			if (inRange) classes.push("in-range");
			if (b.count >= peak) classes.push("is-peak");
			if (attrs.drillDayStartMs === b.startMs) classes.push("is-drilled");
			return m("button", {
				type: "button",
				class: classes.join(" "),
				style: { height: `${Math.round((b.count / max) * 100)}%` },
				title: `${dayLabel(b.startMs)} · ${messagesLabel(b.count)}`,
				onclick: () => attrs.onDrill(b.startMs),
			});
		});
		return m("div.logs-activity", [
			ov.buckets.length > 0
				? m("div.logs-days", bars)
				: m("p.logs-hint.muted", "No activity."),
			m("div.logs-activity-foot", [
				m("span.logs-scope-label", scopeLabel(ov.scope, ov.participation)),
				attrs.canToggle
					? m(Button, {
							label:
								ov.scope === "self"
									? "Show all participants"
									: "Show my activity",
							variant: "secondary",
							small: true,
							onclick: attrs.onToggleScope,
						})
					: null,
			]),
			activityDrill(attrs),
		]);
	},
};

/** activityDrill renders the drilled day's session blocks and their intensity. */
function activityDrill(attrs: ActivityBarsAttrs): Mithril.Children {
	if (attrs.drillLoading) {
		return m("div.logs-drill", m("p.logs-hint.muted", "Loading day…"));
	}
	const drill = attrs.drill;
	const dayStart = attrs.drillDayStartMs;
	if (drill === null || dayStart === null) {
		return null;
	}
	const unit = attrs.unitMs > 0 ? attrs.unitMs : DAY_MS;
	const max = Math.max(1, ...drill.sessions.map((s) => s.count));
	const blocks = drill.sessions.map((s) => {
		const left = ((s.startMs - dayStart) / unit) * 100;
		const width = Math.max(0.4, ((s.endMs - s.startMs) / unit) * 100);
		return m("button", {
			type: "button",
			class: "logs-session" + (s.rp ? " is-rp" : ""),
			style: {
				left: `${left}%`,
				width: `${width}%`,
				height: `${Math.round((s.count / max) * 100)}%`,
			},
			title: sessionTitle(s),
			onclick: () => attrs.onSession(s.startMs, s.endMs),
		});
	});
	return m("div.logs-drill", [
		m(
			"div.logs-session-track",
			blocks.length > 0
				? blocks
				: m("span.logs-hint.muted", "No messages this day."),
		),
		m("div.logs-drill-foot", [
			m("span.logs-intensity", intensityLabel(drill.summary)),
			m(Button, {
				label: "Back to days",
				variant: "secondary",
				small: true,
				onclick: attrs.onClose,
			}),
		]),
	]);
}

/** peakThreshold returns the count at/above which a self-scope day is unusual,
 * from the median and scaled MAD of the nonzero buckets. Too few active days (or
 * a flat baseline) yields no peak. */
function peakThreshold(counts: number[]): number {
	const nonzero = counts.filter((c) => c > 0).sort((a, b) => a - b);
	if (nonzero.length < 3) {
		return Number.POSITIVE_INFINITY;
	}
	const med = nonzero[Math.floor(nonzero.length / 2)] ?? 0;
	const devs = nonzero.map((c) => Math.abs(c - med)).sort((a, b) => a - b);
	const mad = devs[Math.floor(devs.length / 2)] ?? 0;
	if (mad === 0) {
		return Math.max(3, med + 1);
	}
	return Math.max(3, Math.ceil(med + 2 * 1.4826 * mad));
}

/** scopeLabel names the primary series and, for self scope, the participant
 * estimate the switch used. */
function scopeLabel(scope: LogActivityScope, p: LogParticipation | undefined): string {
	if (scope === "self") {
		return p !== undefined && p.sampled > 0
			? `Your activity · ~${p.effective.toFixed(1)} participants`
			: "Your activity";
	}
	return "All participants";
}

/** sessionTitle is a session block's hover text. */
function sessionTitle(s: LogSession): string {
	return [
		s.rp ? "Roleplay" : "Chat",
		messagesLabel(s.count),
		formatDuration(s.endMs - s.startMs),
	].join(" · ");
}

/** intensityLabel summarizes a span: size, wall span, volume, rate, long share. */
function intensityLabel(s: LogSession): string {
	const activeMin = s.activeMs / 60000;
	const rate = activeMin > 0 ? s.count / activeMin : 0;
	const longPct = s.count > 0 ? Math.round((s.longCount / s.count) * 100) : 0;
	return `${messagesLabel(s.count)} · ${formatDuration(s.endMs - s.startMs)} · ${formatBytes(s.volume)} · ${rate.toFixed(1)}/min · ${longPct}% long`;
}

/** dayLabel renders a local day bucket as e.g. "Mar 3". */
function dayLabel(ms: number): string {
	return new Date(ms).toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

/** messagesLabel is a short "N message(s)" label. */
function messagesLabel(n: number): string {
	return `${n} ${n === 1 ? "message" : "messages"}`;
}

/** formatDuration renders a millisecond span compactly, e.g. "2h 05m" or "18m". */
function formatDuration(ms: number): string {
	const totalMin = Math.max(0, Math.round(ms / 60000));
	const h = Math.floor(totalMin / 60);
	return h > 0 ? `${h}h ${pad2(totalMin % 60)}m` : `${totalMin}m`;
}