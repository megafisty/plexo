// roomadmin.ts — the room management modal. Opened from a room header's Manage
// button when the reporting session has mod/owner rights in that room. The
// actual tools (describe, mods, kick/ban, visibility, destroy) are not built
// yet; this shell reserves the single modal slot and renders placeholder
// content. It reads the conversation from the store so its title stays live
// while the dialog is open.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useStore, useView } from "../../context.js";
import { closeModal } from "../../store/state.js";
import { convKey, type ConvRef } from "../../transport/protocol.js";
import { Dialog } from "../primitives/dialog.js";

export interface RoomAdminDialogAttrs {
	session: string;
	conv: ConvRef;
}

export const RoomAdminDialog: Mithril.Component<RoomAdminDialogAttrs> = {
	view: ({ attrs }) => {
		const store = useStore();
		const view = useView();
		const conv = store.conversations[attrs.session]?.[convKey(attrs.conv)];
		const title =
			conv?.title !== undefined && conv.title !== "" ? conv.title : attrs.conv.id;

		return m(
			Dialog,
			{
				title: "Manage room",
				subtitle: title,
				class: "room-admin-dialog",
				onClose: () => closeModal(view),
			},
			m("div.room-admin", [
				m("p.muted", "Room management tools are coming soon."),
			]),
		);
	},
};