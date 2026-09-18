// pane.ts — the center column: ConversationPane, its header, and the detail
// dialog. Absorbs ConversationPane.ts and ConversationHeader.ts.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useStore, useDispatch, useView, useActions } from "../../context.js";
import type { Conversation } from "../../store/state.js";
import { Dialog } from "../primitives/dialog.js";
import { FeaturedCharacter, type FeaturedCharacterState } from "../presence/character.js";
import { request } from "../../render.js";
import { Composer } from "../composer/composer.js";
import { MessageList } from "../messages/timeline.js";
import { dismissConv, openRealWarpConv } from "../../store/commands.js";
import { convKey } from "../../transport/protocol.js";


// ==========================================================================
// ConversationHeader.ts
// ==========================================================================
// ConversationHeader: the active conversation's identity plus its actions. The
// channel member list and inline topic are deliberately absent: the header is
// the room name on the left and actions on the right. A DM has no room title or
// topic, so it leads with the partner's presence instead (avatar, name, status
// mark, one-line status message) and its detail button opens the full status
// message rather than a description.

export interface HeaderAttrs {
	conv: Conversation;
	/** action is the header's contextual dismiss button — Leave for a channel or
	 * room, Close for a DM — or null when the conversation has no dismiss. */
	action: { label: string; onAction: () => void } | null;
	/** secondary is an optional extra button rendered before `action` (a warp
	 * pane pairs "Open conversation" with "Close"). */
	secondary?: { label: string; onAction: () => void } | null;
}

interface HeaderState {
	detailOpen: boolean;
}

export const ConversationHeader: Mithril.Component<HeaderAttrs> = {
	view: (vnode) => {
		const store = useStore();
		const state = vnode.state as HeaderState;
		const { conv } = vnode.attrs;
		const title =
			conv.title !== undefined && conv.title !== "" ? conv.title : conv.conv.id;
		// A DM's id is the partner's character name; its "description" is empty,
		// so the header shows live presence and the detail dialog shows status.
		const isDM = conv.conv.kind === "dm";
		const partner: FeaturedCharacterState | undefined = isDM
			? (store.characters[conv.conv.id] ?? {
					name: conv.conv.id,
					online: false,
				})
			: undefined;

		return m("div.conversation-header", [
			partner !== undefined
				? m(FeaturedCharacter, {
						character: partner,
						class: "conversation-header-character",
						row: true,
					})
				: m("h2.pane-title", title),
			m("div.header-side", [
				// A warp pane has no description to show, so its detail button is
				// omitted along with the composer.
				vnode.attrs.conv.readOnly === true
					? null
					: m(
							"button.button.button-small.button-secondary",
							{
								type: "button",
								onclick: () => {
									state.detailOpen = true;
								},
							},
							partner !== undefined ? "View Full Status" : "View description",
						),
				vnode.attrs.secondary !== undefined && vnode.attrs.secondary !== null
					? m(
							"button.button.button-small.button-secondary",
							{
								type: "button",
								title: vnode.attrs.secondary.label,
								onclick: vnode.attrs.secondary.onAction,
							},
							vnode.attrs.secondary.label,
						)
					: null,
				vnode.attrs.action !== null
					? m(
							"button.button.button-small.button-secondary",
							{
								type: "button",
								title: vnode.attrs.action.label,
								onclick: vnode.attrs.action.onAction,
							},
							vnode.attrs.action.label,
						)
					: null,
			]),
			state.detailOpen
				? partner !== undefined
					? m(DetailDialog, {
							title: "Full status",
							subtitle: partner.name,
							html: partner.statusMsg,
							empty: "No status message.",
							onClose: () => {
								state.detailOpen = false;
							},
						})
					: m(DetailDialog, {
							title: "Description",
							subtitle: title,
							html: conv.description,
							empty: "No description.",
							onClose: () => {
								state.detailOpen = false;
							},
						})
				: null,
		]);
	},
};

interface DetailDialogAttrs {
	/** dialog heading, e.g. "Description" or "Full status". */
	title: string;
	/** secondary line for context: the room name or the partner's name. */
	subtitle: string;
	/** html is rendered HTML from the core, never raw BBCode; may be empty. */
	html?: string;
	/** empty is the muted fallback shown when html is blank. */
	empty: string;
	onClose: () => void;
}

// DetailDialog is a self-contained modal built on the shared Dialog shell: the
// header X (or Escape, or a backdrop click) closes it, and the body scrolls so
// long content cannot push the close control off-screen.
const DetailDialog: Mithril.Component<DetailDialogAttrs> = {
	view: ({ attrs }) =>
		m(
			Dialog,
			{
				title: attrs.title,
				subtitle: attrs.subtitle,
				onClose: attrs.onClose,
				class: "detail-dialog",
			},
			attrs.html !== undefined && attrs.html !== ""
				? m("div.detail-body", m.trust(attrs.html))
				: m("p.muted", attrs.empty),
		),
};

// ==========================================================================
// ConversationPane.ts
// ==========================================================================
// ConversationPane: the center column. Owns the active conversation's header,
// message list, typing bar, and composer. A container: it activates interest
// and renders the empty state when nothing is selected.

// TYPING_TTL_MS is only a safety net for a lost "clear": senders do not
// refresh (Horizon, and we now match it), so the indicator must not expire on
// its own during a long post. Normal retirement is explicit: clear/paused TPNs,
// a delivered message, FLN, or releasing the conversation (see releaseConv).
// Five minutes is longer than any plausible unbroken stretch of typing, so the
// bar cannot vanish mid-sentence while still eventually clearing a sender whose
// clear signal was lost.
const TYPING_TTL_MS = 5 * 60_000;

interface PaneState {
	/** typingTimer is the one-shot aimed at the next typing expiry. */
	typingTimer?: number;
	typingDeadline?: number;
}

export const ConversationPane: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as PaneState;
		state.typingTimer = undefined;
		state.typingDeadline = undefined;
	},
	onremove: (vnode) => {
		clearTypingTimer(vnode.state as PaneState);
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();

		const session = view.activeSession;
		const sessionRecord =
			session === null ? undefined : store.sessions[session];
		const key = session === null ? undefined : view.activeConv[session];
		const conv =
			session === null || key === undefined
				? undefined
				: store.conversations[session]?.[key];

		if (session === null || conv === undefined) {
			// No conversation selected. A session that is absent or still
			// connecting has none to select yet, so hold the pane's shape with a
			// skeleton instead of the empty-conversation hint; a live session with
			// nothing open gets the real hint.
			const connecting =
				session !== null &&
				(sessionRecord === undefined || sessionRecord.state === "connecting");
			return m(
				"section.conversation-pane",
				connecting
					? m(PaneSkeleton)
					: m(
							"div.pane-empty",
							m("p.muted", "Select a conversation, or join a new channel."),
						),
			);
		}

		const now = Date.now();
		const active: string[] = [];
		const paused: string[] = [];
		let expiresIn = Infinity;
		for (const name of Object.keys(conv.typing)) {
			const state = conv.typing[name];
			const remaining = (state?.at ?? 0) + TYPING_TTL_MS - now;
			if (remaining <= 0) {
				continue;
			}
			if (state?.paused === true) {
				paused.push(name);
			} else {
				active.push(name);
			}
			if (remaining < expiresIn) {
				expiresIn = remaining;
			}
		}
		const typists = active.length + paused.length;
		// There is no guaranteed "stopped typing" event, so schedule the redraw
		// that clears the bar; otherwise it lingers in a quiet conversation.
		syncTypingTimer(
			vnode.state as PaneState,
			typists === 0 ? undefined : now + expiresIn,
		);
		const parts: string[] = [];
		if (active.length > 0) {
			parts.push(`${active.join(", ")} typing…`);
		}
		if (paused.length > 0) {
			parts.push(`${paused.join(", ")} has entered text`);
		}
		const leavable = conv.conv.kind === "official" || conv.conv.kind === "room";
		const dm = conv.conv.kind === "dm";
		// A warp pane is read-only: its action leaves for the real conversation
		// (join/track/live stream), the only warp path that touches the core, and
		// a secondary Close drops the ephemeral pane.
		const readOnly = conv.readOnly === true;
		const close = (): void => dismissConv(store, view, dispatch, session, conv.key);
		const action = readOnly
			? {
					label: "Open conversation",
					onAction: () =>
						openRealWarpConv(
							store,
							view,
							dispatch,
							useActions(),
							session,
							conv.conv.id,
						),
				}
			: leavable
				? { label: "Leave", onAction: close }
				: dm
					? { label: "Close", onAction: close }
					: null;
		const secondary = readOnly ? { label: "Close", onAction: close } : null;

		return m("section.conversation-pane", { key: convKey(conv.conv) }, [
			m(ConversationHeader, { conv, action, secondary }),
			m(MessageList),
			!readOnly && typists > 0 ? m("div.typing-bar", parts.join(" · ")) : null,
			readOnly ? null : m(Composer),
		]);
	},
};

/** PaneSkeleton holds the conversation pane's shape while a freshly bound
 * session is still connecting, so the three-column layout does not jump when
 * the session record lands. */
const PaneSkeleton: Mithril.Component = {
	view: () =>
		m("div.pane-skeleton", { "aria-hidden": "true" }, [
			m("div.skeleton.skeleton-title"),
			m("div.skeleton.skeleton-line"),
			m("div.skeleton.skeleton-line"),
			m("div.skeleton.skeleton-line.skeleton-line-short"),
		]),
};

/** syncTypingTimer keeps one timer aimed at the next typing expiry so the bar
 * clears without an unrelated redraw. It reschedules only when the deadline
 * moves by more than a frame's worth, avoiding per-render timer churn. */
function syncTypingTimer(
	state: PaneState,
	deadline: number | undefined,
): void {
	if (deadline === undefined) {
		clearTypingTimer(state);
		return;
	}
	if (
		state.typingTimer !== undefined &&
		state.typingDeadline !== undefined &&
		Math.abs(state.typingDeadline - deadline) <= 50
	) {
		return;
	}
	clearTypingTimer(state);
	state.typingDeadline = deadline;
	state.typingTimer = window.setTimeout(() => {
		state.typingTimer = undefined;
		state.typingDeadline = undefined;
		request();
	}, Math.max(1, deadline - Date.now()));
}

function clearTypingTimer(state: PaneState): void {
	if (state.typingTimer !== undefined) {
		window.clearTimeout(state.typingTimer);
		state.typingTimer = undefined;
		state.typingDeadline = undefined;
	}
}
