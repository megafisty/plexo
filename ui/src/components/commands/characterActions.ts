// characterActions.ts — the shared per-character action list. One CommandList
// object serves every surface that opens a character's actions (the DM menu,
// the friends & bookmarks list, the channel roster, and the seen list), so the
// action set and its gating cannot drift between them.
//
// The list is a subcommand of each of those surfaces, so it carries no
// per-character state of its own: the drilling rows put the character and the
// surface they came from on their `value`, and this list turns that into the
// context the palette shows and the action reads (see `transformPrevious`). The
// shells only forward the transformed item as `previousItem`, so nothing has to
// live in shell state.
//
// Open DM and Open Profile are always present (Open DM is suppressed when the
// active conversation already is that DM). Bookmark/Unbookmark is both ways,
// except in the friends & bookmarks list, which only removes a bookmark from a
// bookmark-only entry. Moderator Actions appear only from a channel roster and
// only when the authority snapshot authorizes a verb.

import { openProfile } from "../../lib/characters.js";
import { isMemberConv } from "../../lib/conversations.js";
import { isBookmarked, isFriend } from "../../lib/friends.js";
import {
	memberActionNotice,
	roomOps,
	type MemberCapabilities,
	type RoomOps,
} from "../../lib/moderation.js";
import { activateConv, setBookmark } from "../../store/commands.js";
import { pushToast } from "../../store/state.js";
import { type CommandItem, type CommandList } from "./list.js";

/** CharacterActionSource names the surface a character's actions were opened
 * from. It gates the options that are not derivable from the live context:
 * channel moderation is offered only from a channel roster, and the friends &
 * bookmarks list only removes a bookmark from a bookmark-only entry. */
export type CharacterActionSource = "dm" | "roster" | "seen" | "friends";

/** CharacterActionTarget is the data a drilling row carries to the action list:
 * the character and the surface it came from. */
export interface CharacterActionTarget {
	name: string;
	source: CharacterActionSource;
}

/** characterActionPrevious builds CharacterActionsList's previous item from a
 * drilling row's payload: the action list renders it as its header and reads
 * the target back off `value`. */
export function characterActionPrevious(
	target: CharacterActionTarget,
): CommandItem<CharacterActionTarget> {
	return {
		id: `character-actions:${target.name}`,
		title: target.name,
		description: "Choose an action.",
		filterable: "",
		value: target,
	};
}

/** moderatorItems is the Moderator Actions sub-list: the member verbs the
 * capability snapshot authorized, in menu order. Empty when none is allowed. */
function moderatorItems(
	name: string,
	caps: MemberCapabilities,
): CommandItem<CharacterActionTarget>[] {
	const items: CommandItem<CharacterActionTarget>[] = [];
	if (caps.op) {
		items.push({
			id: "op",
			title: "Make moderator",
			description: `Add ${name} as a room moderator.`,
			filterable: "Make moderator",
		});
	}
	if (caps.deop) {
		items.push({
			id: "deop",
			title: "Remove moderator",
			description: `Remove ${name} as a room moderator.`,
			filterable: "Remove moderator",
		});
	}
	if (caps.kick) {
		items.push({
			id: "kick",
			title: "Kick",
			description: `Kick ${name} from this channel.`,
			filterable: "Kick",
		});
	}
	if (caps.ban) {
		items.push({
			id: "ban",
			title: "Ban",
			description: `Ban ${name} from this channel.`,
			filterable: "Ban",
		});
	}
	return items;
}

/** moderatorPrevious normalizes the header the Moderator Actions sub-list shows.
 * Its rows do not read the previous item (the sub-list closes over the name), so
 * this is a header only. */
function moderatorPrevious(name: string): CommandItem<CharacterActionTarget> {
	return {
		id: `moderator:${name}`,
		title: name,
		description: "Moderator actions.",
		filterable: "",
	};
}

/** moderatorActionList is the Moderator Actions sub-list: the verbs the
 * capability snapshot authorized, carrying the ops interface that runs them.
 * It is generated per character because it closes over that snapshot; it is
 * reached only from CharacterActionsList. */
function moderatorActionList(
	name: string,
	items: CommandItem<CharacterActionTarget>[],
	ops: RoomOps,
): CommandList<CharacterActionTarget> {
	return {
		id: `moderator:${name}`,
		placeholder: "Moderator actions",
		emptyText: "No actions",
		transformPrevious: () => moderatorPrevious(name),
		list: () => items,
		onSelect: (item) => {
			switch (item.id) {
				case "op":
					void ops.op(name);
					break;
				case "deop":
					void ops.deop(name);
					break;
				case "kick":
					void ops.kick(name);
					break;
				case "ban":
					void ops.ban(name);
					break;
				default:
					break;
			}
		},
	};
}

/** CharacterActionsList is the per-character action list shared by the DM menu,
 * the friends & bookmarks list, the channel roster, and the seen list. It
 * gates its options on the target's surface and the live conversation; the
 * snapshot is taken when the list materializes (on drilling in), so every row
 * is decided before it is shown. */
export const CharacterActionsList: CommandList<CharacterActionTarget> = {
	id: "character-actions",
	placeholder: "Choose an action",
	emptyText: "No actions",
	transformPrevious: (item) =>
		characterActionPrevious(item.value as CharacterActionTarget),
	list: (context, previous) => {
		const target = previous?.value as CharacterActionTarget | undefined;
		if (target === undefined) {
			return [];
		}
		const { name, source } = target;
		const items: CommandItem<CharacterActionTarget>[] = [];
		const conv = context.currentConv;
		const inThisDm =
			conv !== undefined &&
			conv.conv.kind === "dm" &&
			conv.conv.id.toLowerCase() === name.toLowerCase();
		if (!inThisDm) {
			items.push({
				id: "open-dm",
				title: "Open DM",
				description: `Start or reopen the direct message with ${name}.`,
				filterable: "Open DM",
			});
		}
		items.push({
			id: "open-profile",
			title: "Open Profile",
			description: `Open ${name}'s F-List profile in a new tab.`,
			filterable: "Open Profile",
		});
		const bookmarked = isBookmarked(context.store, name);
		if (source === "friends") {
			// The friends & bookmarks list only removes a bookmark from a
			// bookmark-only entry; adding or removing a friend's bookmark is left
			// to the character menu and Ctrl-K picker.
			if (bookmarked && !isFriend(context.store, name)) {
				items.push({
					id: "unbookmark",
					title: "Unbookmark",
					description: `Remove ${name} from your bookmarks.`,
					filterable: "Unbookmark",
				});
			}
		} else {
			items.push({
				id: bookmarked ? "unbookmark" : "bookmark",
				title: bookmarked ? "Unbookmark" : "Bookmark",
				description: bookmarked
					? `Remove ${name} from your bookmarks.`
					: `Add ${name} to your bookmarks.`,
				filterable: bookmarked ? "Unbookmark" : "Bookmark",
			});
		}
		if (source === "roster" && conv !== undefined && isMemberConv(conv)) {
			const ops = roomOps(
				context.store,
				context.actions,
				context.session,
				conv,
				(action, who, error) => {
					pushToast(
						context.view,
						error !== null ? error : memberActionNotice(action, who),
					);
				},
			);
			const mods = moderatorItems(
				name,
				ops.capabilities({
					name,
					admin: context.store.characters[name]?.admin === true,
				}),
			);
			if (mods.length > 0) {
				items.push({
					id: "moderator-actions",
					title: "Moderator Actions",
					description: `Moderate ${name} in this channel.`,
					filterable: "Moderator Actions",
					next: moderatorActionList(name, mods, ops),
				});
			}
		}
		return items;
	},
	onSelect: (item, context, previous) => {
		const target = previous?.value as CharacterActionTarget | undefined;
		if (target === undefined) {
			return;
		}
		const name = target.name;
		switch (item.id) {
			case "open-dm":
				activateConv(
					context.store,
					context.view,
					context.dispatch,
					context.session,
					`dm:${name}`,
				);
				break;
			case "open-profile":
				openProfile(name);
				break;
			case "bookmark":
				setBookmark(context.dispatch, name, true);
				break;
			case "unbookmark":
				setBookmark(context.dispatch, name, false);
				break;
			default:
				break;
		}
	},
};