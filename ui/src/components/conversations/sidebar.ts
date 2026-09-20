import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useDispatch, useStore, useView } from "../../context.js";
import { genderClass, profileURL } from "../../lib/characters.js";
import { convTitle, splitConversations } from "../../lib/order.js";
import { memo, memoInit, type Memo } from "../../render.js";
import { activateConv, activateInvites } from "../../store/commands.js";
import type { Conversation } from "../../store/state.js";
import { INVITES_KEY, openModal } from "../../store/state.js";
import { convSeverity } from "../../store/unread.js";
import { Avatar } from "../primitives/Avatar.js";
import { Button } from "../primitives/form.js";
import { OFFLINE_MARK, statusLabel, statusMark } from "../presence/status.js";
// ConversationSidebar: the left column. Lists the session's joined
// conversations in two stable blocks — channels/rooms and direct messages —
// each sorted alphabetically, plus the join button that opens the join dialog.
// Which DMs are present is core-owned (a session tracks the ones the user wants
// visible); the sidebar lists exactly the DMs in the store. Ordering is never
// driven by activity, so rows do not jump around as messages arrive.
//
// Performance: the sorted arrays are rebuilt only when the store's conversation
// revision changes, and each block's whole <ul> vnode is memoized against
// (session, conversationsRev, activeKey, unreadRev). An unrelated redraw — a
// message, typing, presence — reuses the cached vnodes, so Mithril's diff
// short-circuits on `old === new` and touches zero sidebar rows.

interface SidebarState {
	/** session/conversationsRev the sorted arrays were built for. */
	session: string | null;
	conversationsRev: number;
	channels: Conversation[];
	dms: Conversation[];
	warps: Conversation[];
	/** channelsVNode / dmsVNode / warpsVNode cache the built <ul>s. */
	channelsVNode: Memo<Mithril.Vnode>;
	dmsVNode: Memo<Mithril.Vnode>;
	warpsVNode: Memo<Mithril.Vnode>;
}

export const ConversationSidebar: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as SidebarState;
		state.session = null;
		state.conversationsRev = -1;
		state.channels = [];
		state.dms = [];
		state.channelsVNode = memoInit();
		state.dmsVNode = memoInit();
		state.warpsVNode = memoInit();
	},
	view: (vnode) => {
		const state = vnode.state as SidebarState;
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();

		const session = view.activeSession;
		const per = session === null ? undefined : store.conversations[session];
		// Own presence is merged into store.characters by applyPresence; fall back
		// to the snapshot's self record before the first event lands.
		const self =
			session === null
				? undefined
				: (store.characters[session] ?? store.sessions[session]?.self);
		const live = session !== null && store.sessions[session]?.state === "live";

		// Re-sort only when the conversation set or a title changed, or the
		// session switched (the revision counter is global).
		if (
			state.session !== session ||
			state.conversationsRev !== store.conversationsRev
		) {
			state.session = session;
			state.conversationsRev = store.conversationsRev;
			const { channels, dms, warps } = splitConversations(per);
			state.channels = channels;
			state.dms = dms;
			state.warps = warps;
		}
		const activeKey = session === null ? undefined : view.activeConv[session];
		const invites =
			session === null ? [] : (store.sessions[session]?.invites ?? []);
		const invitesOpen =
			session !== null &&
			invites.length > 0 &&
			view.invitesClosed[session] !== true;

		const list = (items: Conversation[]): Mithril.Vnode =>
			m(
				"ul.conversation-list",
				items.map((conv) => {
					const severity = convSeverity(conv);
					return m(
						"li",
						{ key: conv.key },
						m(
							"button.conversation-item",
							{
								class: conv.key === activeKey ? "is-active" : "",
								type: "button",
								onclick: () => {
									if (session !== null) {
										activateConv(store, view, dispatch, session, conv.key);
									}
								},
							},
							[
								m(
									"span.conv-kind",
									{ title: conv.conv.kind },
									kindGlyph(conv.conv.kind),
								),
								m("span.conv-title", convTitle(conv)),
								severity !== "none"
									? m(
											severity === "elevated"
												? "span.unread-badge.is-severe"
												: "span.unread-badge",
											{
												title:
													severity === "elevated"
														? "Unread (important)"
														: "Unread",
											},
										)
									: null,
							],
						),
					);
				}),
			);

		const keys = [session, store.conversationsRev, activeKey, store.unreadRev];
		const channelsVNode = memo(state.channelsVNode, keys, () =>
			list(state.channels),
		);
		const dmsVNode = memo(state.dmsVNode, keys, () => list(state.dms));
		const warpsVNode = memo(state.warpsVNode, keys, () => list(state.warps));

		return m("nav.conversation-sidebar", [
			m("div.sidebar-head", [
				m("h2.sidebar-title", "Channels"),
				m(Button, {
					label: "+ Join",
					small: true,
					title: "Join a channel or room",
					disabled: session === null,
					onclick: () => openModal(view, { kind: "join" }),
				}),
			]),
			m("div.conversation-sections", [
				state.channels.length === 0
					? m("p.sidebar-empty.muted", "No joined conversations yet.")
					: channelsVNode,
				invitesOpen
					? m("div.conversation-section", [
							m("h2.sidebar-title", "Invites"),
							m(
								"ul.conversation-list",
								m(
									"li",
									{ key: INVITES_KEY },
									m(
										"button.conversation-item",
										{
											class:
												activeKey === INVITES_KEY ? "is-active" : "",
											type: "button",
											onclick: () => {
												if (session !== null) {
													activateInvites(store, view, dispatch, session);
												}
											},
										},
										[
											m("span.conv-kind", { title: "invites" }, "✉"),
											m(
												"span.conv-title",
												invites.length > 1
													? `Invitations (${invites.length})`
													: "Invitations",
											),
										],
									),
								),
							),
						])
					: null,
				state.dms.length > 0
					? m("div.conversation-section", [
							m("h2.sidebar-title", "Direct messages"),
							dmsVNode,
						])
					: null,
				state.warps.length > 0
					? m("div.conversation-section", [
							m("h2.sidebar-title", "Warps"),
							warpsVNode,
						])
					: null,
			]),
			session === null
				? null
				: m("div.sidebar-self", [
						m("div.sidebar-self-id", [
							m(Avatar, { name: session, size: 32 }),
							m(
								"a.sidebar-self-name",
								{
									class: genderClass(self?.gender),
									href: profileURL(session),
									target: "_blank",
									rel: "noopener noreferrer",
									title: `View ${session}'s profile`,
								},
								session,
							),
						]),
						m(
							"button.status-button",
							{
								type: "button",
								disabled: !live,
								title: live
									? "Set your status and status message"
									: "Status is available once connected",
								onclick: () => {
									openModal(view, { kind: "status" });
								},
							},
							[
								m(
									"span.status-button-mark",
									self?.online === false
										? OFFLINE_MARK
										: statusMark(self?.status ?? "online"),
								),
								m(
									"span.status-button-label",
									self?.online === false
										? "Offline"
										: statusLabel(self?.status ?? "online"),
								),
							],
						),
					]),
		]);
	},
};

function kindGlyph(kind: string): string {
	switch (kind) {
		case "official":
			return "#";
		case "room":
			return "⌂";
		case "dm":
			return "@";
		case "broadcast":
			return "!";
		case "warp":
			return "★";
		default:
			return "·";
	}
}
