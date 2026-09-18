import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useDispatch, useStore, useView } from "../../context.js";
import { activateConv, activatePendingConv } from "../../store/commands.js";
import { ensureActiveInterest } from "../../store/interest.js";
import { ConversationPane } from "../conversations/pane.js";
import { ConversationSidebar } from "../conversations/sidebar.js";
import { ChannelRoster } from "../presence/roster.js";
import { RosterPanel } from "../presence/roster.js";
// SessionView: the active character's three-pane workspace — conversation
// sidebar, message pane, roster panel. Auto-selects the most recent
// conversation the first time a session appears.

export const SessionView: Mithril.Component = {
	view: () => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();

		const name = view.activeSession;
		if (name === null) {
			return m("p.muted", "Select a character tab.");
		}
		// The session record may not have arrived yet (a tab bound right after
		// login). Render the grid regardless so the columns keep their size;
		// ConversationPane shows a connecting skeleton until the record lands.

		// A [session] link requested this conversation; open it only now that the
		// core's JCH has created it. Runs before auto-select so a pending target
		// wins over the most-recent heuristic.
		activatePendingConv(store, view, dispatch, name);

		// First render for a session with conversations: open the most recent
		// one so the pane is never pointlessly empty.
		const per = store.conversations[name];
		if (view.activeConv[name] === undefined && per !== undefined) {
			const best = Object.values(per)
				.filter((c) => c.readOnly !== true)
				.sort((a, b) => b.lastActivity - a.lastActivity)[0];
			if (best !== undefined) {
				activateConv(store, view, dispatch, name, best.key);
			}
		}

		// A conversation selected outside activateConv (snapshot auto-select)
		// may never have asked for its materialization; ensure full interest until
		// the window exists. Idempotent while the conv_view is in flight.
		ensureActiveInterest(store, view, dispatch, name);

		// The right column is the channel/room roster when one is active, and
		// the character-search panel otherwise (DMs, or nothing selected).
		const activeKey = view.activeConv[name];
		const activeConv =
			activeKey === undefined
				? undefined
				: store.conversations[name]?.[activeKey];
		const isChannel =
			activeConv !== undefined &&
			(activeConv.conv.kind === "official" || activeConv.conv.kind === "room");

		return m("div.session-view", [
			m(ConversationSidebar),
			m(ConversationPane),
			isChannel
				? m(ChannelRoster, { session: name, conv: activeConv })
				: m(RosterPanel),
		]);
	},
};
