// roomadmin.ts — the room management modal. Opened from a room header's Manage
// button when the reporting session has mod/owner rights in that room. It reads
// the conversation from the store so its title stays live while the dialog is
// open, and fetches the on-demand management view (GET /api/room) once on open
// for the state that is not streamed (owner, ops, bans, visibility).
//
// The dialog is split across DialogTabs:
//   General     — description editor; publish/close toggle; a public room shows
//                 the copyable [session] link, a private one the targeted
//                 invite.
//   Moderators  — the owner, the mod list, add/remove mod, and ownership
//                 transfer (owner-only).
//   Bans        — the observed ban list with unban, plus a ban form.
//
// The live conversation `ops` set drives the mod list, so a COA/COR broadcast
// is reflected immediately; owner and bans are pull-only, so any action that
// changes them refetches RoomInfo.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { fetchRoomInfo } from "../../api.js";
import { useActions, useStore, useView } from "../../context.js";
import { closeModal, openCommand } from "../../store/state.js";
import { request } from "../../render.js";
import { convLabel, formatClock, overLimit } from "../../lib/format.js";
import {
	convKey,
	type ConvRef,
	type RoomBan,
	type RoomInfo,
} from "../../transport/protocol.js";
import { roomOps, type RoomOpsResult } from "../../lib/moderation.js";
import { Dialog, DialogTabs, type DialogTab } from "../primitives/dialog.js";
import { Composer, type ComposerFormat, type ComposerPalette } from "../composer/composer.js";
import {
	Button,
	Checkbox,
	FormError,
	Spinner,
	TextField,
} from "../primitives/form.js";

export interface RoomAdminDialogAttrs {
	session: string;
	conv: ConvRef;
}

interface RoomAdminDialogState {
	/** info is the fetched management view; null until it lands or on failure. */
	info: RoomInfo | null;
	loading: boolean;
	/** visibility is the toggle's current value. Unknown server state falls
	 * back to private: a room is created closed, and the session only learns a
	 * published state when it issues the change itself or sees the room in the
	 * public catalog. */
	visibility: "public" | "private";
	/** busy is true while a visibility change is awaiting its ack. */
	busy: boolean;
	/** error is the last fetch or visibility failure, shown under the toggle. */
	error: string | null;
	/** tab is the active DialogTabs id. */
	tab: string;

	/** description is the raw BBCode description draft. */
	description: string;
	/** descriptionSeeded is true once RoomInfo has filled the editor; later
	 * refetches must not clobber an in-progress edit. */
	descriptionSeeded: boolean;
	/** descriptionBusy is true while a description save is in flight. */
	descriptionBusy: boolean;
	/** descriptionStatus is the last description save result. */
	descriptionStatus: { ok: boolean; text: string } | null;

	/** inviteName is the character name in the private-room invite field. */
	inviteName: string;
	/** inviteBusy is true while an invite is awaiting its ack. */
	inviteBusy: boolean;
	/** inviteError is a rejected invite, shown under the field. */
	inviteError: string | null;
	/** inviteSent is the character just invited, shown as a confirmation. */
	inviteSent: string | null;

	/** actionBusy is true while a moderator or ban action is in flight. */
	actionBusy: boolean;
	/** pendingRow keys the row whose action is in flight, for the busy label. */
	pendingRow: string | null;
	/** actionStatus is the last moderator/ban result, tagged with the tab that
	 * produced it so it renders where it happened. */
	actionStatus: { tab: string; ok: boolean; text: string } | null;
	/** modName is the add-moderator field. */
	modName: string;
	/** ownerName is the transfer-ownership field. */
	ownerName: string;
	/** banName is the ban-character field. */
	banName: string;
}

/** sessionLink returns the BBCode tag that opens a room in F-Chat: the title
 * as the tag's parameter and the ADH id as its content. */
function sessionLink(title: string, id: string): string {
	return `[session=${title}]${id}[/session]`;
}

/** copyText copies a value to the clipboard, preferring the async Clipboard API
 * and falling back to a hidden textarea for engines that predate it. Best
 * effort: a failure leaves the code block selectable by hand. */
function copyText(text: string): void {
	if (navigator.clipboard !== undefined) {
		void navigator.clipboard.writeText(text);
		return;
	}
	const area = document.createElement("textarea");
	area.value = text;
	area.setAttribute("readonly", "");
	area.style.position = "fixed";
	area.style.opacity = "0";
	document.body.appendChild(area);
	area.select();
	document.execCommand("copy");
	document.body.removeChild(area);
}

/** expiryLabel renders a ban's expiry: a permanent ban (zero) or a local
 * "Mar 3 14:30" instant. */
function expiryLabel(ms: number | undefined): string {
	if (ms === undefined || ms === 0) {
		return "permanent";
	}
	const day = new Date(ms).toLocaleDateString(undefined, {
		month: "short",
		day: "numeric",
	});
	return `${day} ${formatClock(ms)}`;
}

/** banMeta folds a ban's banner and expiry into one muted line. */
function banMeta(ban: RoomBan): string {
	const parts: string[] = [];
	if (ban.banner !== undefined && ban.banner !== "") {
		parts.push(`by ${ban.banner}`);
	}
	parts.push(expiryLabel(ban.expiresAtMs));
	return parts.join(" · ");
}

export const RoomAdminDialog: Mithril.Component<
	RoomAdminDialogAttrs,
	RoomAdminDialogState
> = {
	oninit: (vnode) => {
		const state = vnode.state as RoomAdminDialogState;
		state.info = null;
		state.loading = true;
		state.visibility = "private";
		state.busy = false;
		state.error = null;
		state.tab = "general";
		state.description = "";
		state.descriptionSeeded = false;
		state.descriptionBusy = false;
		state.descriptionStatus = null;
		state.inviteName = "";
		state.inviteBusy = false;
		state.inviteError = null;
		state.inviteSent = null;
		state.actionBusy = false;
		state.pendingRow = null;
		state.actionStatus = null;
		state.modName = "";
		state.ownerName = "";
		state.banName = "";
		const { session, conv } = vnode.attrs;
		void fetchRoomInfo(session, conv).then((info) => {
			state.info = info;
			state.loading = false;
			if (info === null) {
				state.error = "Could not load room details.";
			} else {
				if (info.visibility === "public") {
					state.visibility = "public";
				}
				if (!state.descriptionSeeded) {
					state.description = info.rawDescription ?? "";
					state.descriptionSeeded = true;
				}
			}
			request();
		});
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const actions = useActions();
		const { attrs } = vnode;
		const state = vnode.state as RoomAdminDialogState;
		const conv = store.conversations[attrs.session]?.[convKey(attrs.conv)];
		const info = state.info;
		const title = convLabel(conv?.title ?? info?.title, attrs.conv.id);
		const link = sessionLink(title, attrs.conv.id);
		const isOwner = info?.selfRole === "owner";
		const owner = info?.owner ?? "";
		// The streamed conversation op set is the live mod list (the core includes
		// the owner in it); RoomInfo.Ops is the fallback before it lands. The owner
		// is shown separately, so it is filtered out here.
		const mods = (conv?.ops ?? info?.ops ?? []).filter((name) => name !== owner);
		// The shared moderation interface. No reporter: this dialog renders each
		// result inline. RoomInfo.owner lets a global moderator's deop on the owner
		// be offered (the accepted, server-rejected edge).
		const ops =
			conv !== undefined
				? roomOps(store, actions, attrs.session, conv, undefined, info?.owner)
				: null;

		/** refresh re-reads RoomInfo after an action whose effect is not streamed
		 * (owner change, bans). It never touches the visibility toggle, which is
		 * the user's own optimistic state. */
		const refresh = (): void => {
			void fetchRoomInfo(attrs.session, attrs.conv).then((next) => {
				if (next !== null) {
					state.info = next;
				}
				request();
			});
		};

		/** errOf reduces a roomOps result to the core's error message, so the
		 * dialog's runAction can treat every action uniformly. */
		const errOf = (result: Promise<RoomOpsResult>): Promise<string | null> =>
			result.then((r) => r.error);

		/** runAction fires one moderator/ban action, tags its status with the
		 * tab, and refreshes the pulled view on success. */
		const runAction = (
			tab: string,
			key: string,
			run: () => Promise<string | null>,
			notice: string,
			clear?: () => void,
		): void => {
			state.actionBusy = true;
			state.pendingRow = key;
			state.actionStatus = null;
			void run()
				.then((err) => {
					state.actionBusy = false;
					state.pendingRow = null;
					if (err !== null) {
						state.actionStatus = { tab, ok: false, text: err };
					} else {
						state.actionStatus = { tab, ok: true, text: notice };
						if (clear !== undefined) {
							clear();
						}
						refresh();
					}
					request();
				});
		};

		/** applyVisibility optimistically flips the toggle, then reverts it if
		 * the core rejects the change. */
		const applyVisibility = (makePublic: boolean): void => {
			const previous = state.visibility;
			const next = makePublic ? "public" : "private";
			state.visibility = next;
			state.busy = true;
			state.error = null;
			void actions
				.roomAdmin(attrs.session, attrs.conv, {
					action: "visibility",
					visibility: next,
				})
				.then((err) => {
					state.busy = false;
					if (err !== null) {
						state.visibility = previous;
						state.error = err;
					}
					request();
				});
		};

		/** sendInvite grants the typed character access and notifies them. It
		 * never force-joins them. */
		const sendInvite = (): void => {
			const name = state.inviteName.trim();
			if (name === "" || state.inviteBusy) {
				return;
			}
			state.inviteBusy = true;
			state.inviteError = null;
			state.inviteSent = null;
			void actions
				.roomAdmin(attrs.session, attrs.conv, {
					action: "invite",
					character: name,
				})
				.then((err) => {
					state.inviteBusy = false;
					if (err !== null) {
						state.inviteError = err;
					} else {
						state.inviteSent = name;
						state.inviteName = "";
					}
					request();
				});
		};

		const saveDescription = (): void => {
			if (state.descriptionBusy) {
				return;
			}
			state.descriptionBusy = true;
			state.descriptionStatus = null;
			void actions
				.roomAdmin(attrs.session, attrs.conv, {
					action: "describe",
					description: state.description,
				})
				.then((err) => {
					state.descriptionBusy = false;
					state.descriptionStatus =
						err !== null
							? { ok: false, text: err }
							: { ok: true, text: "Description saved." };
					request();
				});
		};

		const addMod = (): void => {
			const name = state.modName.trim();
			if (name === "" || state.actionBusy || ops === null) {
				return;
			}
			runAction(
				"mods",
				"mod-add",
				() => errOf(ops.op(name)),
				`Added ${name} as a moderator.`,
				() => {
					state.modName = "";
				},
			);
		};

		const removeMod = (name: string): void => {
			if (state.actionBusy || ops === null) {
				return;
			}
			runAction(
				"mods",
				`mod:${name}`,
				() => errOf(ops.deop(name)),
				`Removed ${name} as a moderator.`,
			);
		};

		const setOwner = (): void => {
			const name = state.ownerName.trim();
			if (name === "" || state.actionBusy) {
				return;
			}
			// Ownership transfer is room-management-only and stays on the raw flow.
			runAction(
				"mods",
				"owner",
				() =>
					actions.roomAdmin(attrs.session, attrs.conv, {
						action: "set_owner",
						character: name,
					}),
				`Transferred ownership to ${name}.`,
				() => {
					state.ownerName = "";
				},
			);
		};

		const ban = (): void => {
			const name = state.banName.trim();
			if (name === "" || state.actionBusy || ops === null) {
				return;
			}
			runAction(
				"bans",
				"ban-add",
				() => errOf(ops.ban(name)),
				`Banned ${name}.`,
				() => {
					state.banName = "";
				},
			);
		};

		const unban = (name: string): void => {
			if (state.actionBusy || ops === null) {
				return;
			}
			runAction(
				"bans",
				`ban:${name}`,
				() => errOf(ops.unban(name)),
				`Unbanned ${name}.`,
			);
		};

		const handlers: RoomAdminHandlers = {
			applyVisibility,
			sendInvite,
			saveDescription,
			addMod,
			removeMod,
			setOwner,
			ban,
			unban,
			copyLink: () => copyText(link),
			setInviteName: (value) => {
				state.inviteName = value;
			},
			setModName: (value) => {
				state.modName = value;
			},
			setOwnerName: (value) => {
				state.ownerName = value;
			},
			setBanName: (value) => {
				state.banName = value;
			},
			setDescription: (value) => {
				state.description = value;
			},
			onformat: (command, apply, selection, start) => {
				openCommand(view, command, apply, selection, start);
			},
		};

		const tabs: DialogTab[] = [
			{
				id: "general",
				label: "General",
				render: () =>
					m(RoomGeneralTab, { state, info, isOwner, link, handlers }),
			},
			{
				id: "mods",
				label: "Moderators",
				render: () =>
					m(RoomModsTab, { state, info, isOwner, owner, mods, handlers }),
			},
			{
				id: "bans",
				label: "Bans",
				render: () => m(RoomBansTab, { state, info, handlers }),
			},
		];

		return m(
			Dialog,
			{
				title: "Manage room",
				subtitle: title,
				class: "room-admin-dialog",
				onClose: () => closeModal(view),
			},
			m(DialogTabs, {
				active: state.tab,
				fill: true,
				onSelect: (id) => {
					state.tab = id;
				},
				tabs,
			}),
		);
	},
};
/** RoomAdminHandlers bundles the modal's actions for its presentational tab
 * children; the dialog owns the async orchestration. */
interface RoomAdminHandlers {
	applyVisibility: (makePublic: boolean) => void;
	sendInvite: () => void;
	saveDescription: () => void;
	addMod: () => void;
	removeMod: (name: string) => void;
	setOwner: () => void;
	ban: () => void;
	unban: (name: string) => void;
	copyLink: () => void;
	setInviteName: (value: string) => void;
	setModName: (value: string) => void;
	setOwnerName: (value: string) => void;
	setBanName: (value: string) => void;
	setDescription: (value: string) => void;
	/** onformat opens the composer's format palette in the palette slot, layered
	 * over this dialog. */
	onformat: (
		command: ComposerPalette,
		apply: ComposerFormat,
		selection: string,
		start?: string,
	) => void;
}

/** roomActionStatus renders the last moderator/ban result tagged with `tab`, so
 * a status shows only on the tab that produced it. */
function roomActionStatus(
	state: RoomAdminDialogState,
	tab: string,
): Mithril.Children {
	const status = state.actionStatus;
	if (status === null || status.tab !== tab) {
		return null;
	}
	return status.ok
		? m("p.field-note.room-admin-invite-ok", status.text)
		: m(FormError, { message: status.text });
}

/** RoomGeneralTab is the General tab: visibility toggle, the public link or the
 * private invite, and the description editor. */
interface RoomGeneralTabAttrs {
	state: RoomAdminDialogState;
	info: RoomInfo | null;
	isOwner: boolean;
	link: string;
	handlers: RoomAdminHandlers;
}

const RoomGeneralTab: Mithril.Component<RoomGeneralTabAttrs> = {
	view: ({ attrs }) => {
		const { state, info, handlers } = attrs;
		if (state.loading) {
			return m(Spinner, { label: "Loading room details…" });
		}
		const descStatus = state.descriptionStatus;
		const descOver = overLimit(state.description, info?.cdsMax ?? 0);
		return [
			attrs.isOwner ? m("p.room-admin-role", "You are the room owner.") : null,
			m("div.room-admin-section", [
				m(Checkbox, {
					label: "Public room",
					checked: state.visibility === "public",
					disabled: state.busy,
					onchange: handlers.applyVisibility,
				}),
				m(
					"p.field-note",
					state.visibility === "public"
						? "Anyone can find and join this room in the room list."
						: "Only invited characters can join this room.",
				),
				m(FormError, { message: state.error }),
			]),
			state.visibility === "public"
				? m("div.room-admin-section", [
						m("span.field-label", "Room link"),
						m("div.room-admin-link-row", [
							m("code.room-admin-link", attrs.link),
							m(
								"button.button.button-small.button-secondary",
								{
									type: "button",
									onclick: handlers.copyLink,
								},
								"Copy",
							),
						]),
						m(
							"p.field-note",
							"Share this tag in chat so others can open the room.",
						),
					])
				: m("div.room-admin-section", [
						m(TextField, {
							label: "Invite character",
							value: state.inviteName,
							disabled: state.inviteBusy,
							oninput: handlers.setInviteName,
							onsubmit: handlers.sendInvite,
						}),
						m("div.room-admin-form-actions", [
							m(Button, {
								label: "Send invite",
								busy: state.inviteBusy,
								disabled: state.inviteName.trim() === "",
								onclick: handlers.sendInvite,
							}),
						]),
						m(FormError, { message: state.inviteError }),
						state.inviteSent !== null
							? m(
									"p.field-note.room-admin-invite-ok",
									`Invited ${state.inviteSent}.`,
								)
							: null,
					]),
			m("div.room-admin-section", [
				m("span.field-label", "Description"),
				m(Composer, {
					value: state.description,
					placeholder: "Room description (BBCode allowed)",
					rows: 4,
					autoGrow: false,
					showSend: false,
					// The Dialog owns Escape; do not blur out of it.
					blurOnEscape: false,
					limit: info?.cdsMax,
					ariaLabel: "Room description",
					onformat: handlers.onformat,
					oninput: handlers.setDescription,
				}),
				m("div.room-admin-form-actions", [
					m(Button, {
						label: "Save description",
						busy: state.descriptionBusy,
						disabled: descOver,
						onclick: handlers.saveDescription,
					}),
				]),
				descStatus !== null && !descStatus.ok
					? m(FormError, { message: descStatus.text })
					: null,
				descStatus !== null && descStatus.ok
					? m("p.field-note.room-admin-invite-ok", descStatus.text)
					: null,
			]),
		];
	},
};

/** RoomModsTab is the Moderators tab: the owner, the live mod list with remove,
 * and (owner-only) add-mod and transfer-ownership forms. */
interface RoomModsTabAttrs {
	state: RoomAdminDialogState;
	info: RoomInfo | null;
	isOwner: boolean;
	owner: string;
	mods: string[];
	handlers: RoomAdminHandlers;
}

const RoomModsTab: Mithril.Component<RoomModsTabAttrs> = {
	view: ({ attrs }) => {
		const { state, info, isOwner, owner, mods, handlers } = attrs;
		if (state.loading) {
			return m(Spinner, { label: "Loading room details…" });
		}
		if (info === null) {
			return m(FormError, {
				message: state.error ?? "Room details unavailable.",
			});
		}
		return [
			m("div.room-admin-section", [
				m("span.field-label", "Owner"),
				m("p.room-admin-owner", owner !== "" ? owner : "No owner"),
			]),
			m("div.room-admin-section", [
				m("span.field-label", "Moderators"),
				mods.length === 0
					? m("p.muted", "No moderators.")
					: m(
							"ul.room-admin-mod-list",
							mods.map((name) =>
								m("li.room-admin-mod", { key: name }, [
									m("span.room-admin-mod-name", name),
									isOwner
										? m(Button, {
												label: "Remove",
												busy: state.pendingRow === `mod:${name}`,
												disabled: state.actionBusy,
												onclick: () => handlers.removeMod(name),
											})
										: null,
								]),
							),
						),
			]),
			isOwner
				? m("div.room-admin-section", [
						m(TextField, {
							label: "Add moderator",
							value: state.modName,
							disabled: state.actionBusy,
							oninput: handlers.setModName,
							onsubmit: handlers.addMod,
						}),
						m("div.room-admin-form-actions", [
							m(Button, {
								label: "Add moderator",
								busy: state.pendingRow === "mod-add",
								disabled: state.modName.trim() === "",
								onclick: handlers.addMod,
							}),
						]),
					])
				: null,
			isOwner
				? m("div.room-admin-section", [
						m(TextField, {
							label: "Transfer ownership",
							value: state.ownerName,
							disabled: state.actionBusy,
							oninput: handlers.setOwnerName,
							onsubmit: handlers.setOwner,
						}),
						m("div.room-admin-form-actions", [
							m(Button, {
								label: "Set owner",
								busy: state.pendingRow === "owner",
								disabled: state.ownerName.trim() === "",
								onclick: handlers.setOwner,
							}),
						]),
					])
				: null,
			roomActionStatus(state, "mods"),
		];
	},
};

/** RoomBansTab is the Bans tab: the observed ban list with unban, plus a ban
 * form. */
interface RoomBansTabAttrs {
	state: RoomAdminDialogState;
	info: RoomInfo | null;
	handlers: RoomAdminHandlers;
}

const RoomBansTab: Mithril.Component<RoomBansTabAttrs> = {
	view: ({ attrs }) => {
		const { state, info, handlers } = attrs;
		if (state.loading) {
			return m(Spinner, { label: "Loading room details…" });
		}
		if (info === null) {
			return m(FormError, {
				message: state.error ?? "Room details unavailable.",
			});
		}
		return [
			m("div.room-admin-section", [
				m("span.field-label", "Banned characters"),
				info.bans.length === 0
					? m("p.muted", "No banned characters.")
					: m(
							"ul.room-admin-ban-list",
							info.bans.map((entry) =>
								m("li.room-admin-ban", { key: entry.name }, [
									m("div.room-admin-ban-info", [
										m("span.room-admin-ban-name", entry.name),
										m("span.room-admin-ban-meta", banMeta(entry)),
									]),
									m(Button, {
										label: "Unban",
										busy: state.pendingRow === `ban:${entry.name}`,
										disabled: state.actionBusy,
										onclick: () => handlers.unban(entry.name),
									}),
								]),
							),
						),
			]),
			m("div.room-admin-section", [
				m(TextField, {
					label: "Ban character",
					value: state.banName,
					disabled: state.actionBusy,
					oninput: handlers.setBanName,
					onsubmit: handlers.ban,
				}),
				m("div.room-admin-form-actions", [
					m(Button, {
						label: "Ban",
						busy: state.pendingRow === "ban-add",
						disabled: state.banName.trim() === "",
						onclick: handlers.ban,
					}),
				]),
			]),
			roomActionStatus(state, "bans"),
		];
	},
};
