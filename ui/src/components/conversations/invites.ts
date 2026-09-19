// invites.ts — InvitesPane: the client-only virtual conversation for a
// session's pending room invitations. It is presented like a conversation but
// is not one: its content is derived from the session's SessionSnapshot.invites
// list (the core's set-to list, already deduplicated by room), nothing is
// streamed or persisted, and each row offers the two terminal actions. Accept
// joins the room through the normal path (the core drops the invitation when
// the self JCH arrives); Dismiss sends dismiss_invite. The pane disappears
// when the list empties or the user closes it (see applyInvites/closeInvites).

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useActions, useDispatch, useStore, useView } from "../../context.js";
import { closeInvites } from "../../store/commands.js";
import { pushToast } from "../../store/state.js";
import { request } from "../../render.js";
import { convKey, OPS, type RoomInvite } from "../../transport/protocol.js";

interface InvitesPaneAttrs {
	session: string;
}

export const InvitesPane: Mithril.Component<InvitesPaneAttrs> = {
	view: ({ attrs }) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const actions = useActions();
		const invites = store.sessions[attrs.session]?.invites ?? [];

		const accept = (inv: RoomInvite): void => {
			const kind = inv.conv.kind === "official" ? "official" : "room";
			void actions.joinChannel(attrs.session, kind, inv.conv.id).then((err) => {
				if (err !== null) {
					pushToast(view, `Join failed: ${err}`);
				}
				request();
			});
		};
		const dismiss = (inv: RoomInvite): void => {
			dispatch({
				op: OPS.dismissInvite,
				session: attrs.session,
				conv: inv.conv,
			});
		};

		return m("section.conversation-pane.invites-pane", [
			m("div.conversation-header", [
				m("h2.pane-title", "Invitations"),
				m("div.header-side", [
					m(
						"button.button.button-small.button-secondary",
						{
							type: "button",
							title: "Hide invitations until a new one arrives",
							onclick: () => closeInvites(view, attrs.session),
						},
						"Close",
					),
				]),
			]),
			m(
				"div.pane-body",
				invites.length === 0
					? m("div.pane-empty", m("p.muted", "No pending invitations."))
					: m(
							"ul.invite-list",
							invites.map((inv) => {
								const title =
									inv.title !== undefined && inv.title !== ""
										? inv.title
										: inv.conv.id;
								return m("li.invite-item", { key: convKey(inv.conv) }, [
									m("div.invite-info", [
										m("span.invite-title", title),
										inv.invitedBy !== undefined && inv.invitedBy !== ""
											? m("span.invite-by", `invited by ${inv.invitedBy}`)
											: null,
									]),
									m("div.invite-actions", [
										m(
											"button.button.button-small",
											{ type: "button", onclick: () => accept(inv) },
											"Accept",
										),
										m(
											"button.button.button-small.button-secondary",
											{ type: "button", onclick: () => dismiss(inv) },
											"Dismiss",
										),
									]),
								]);
							}),
						),
			),
		]);
	},
};