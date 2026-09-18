// Avatar: a character's F-List avatar image. Resizable via the size attr;
// the URL is derived from the character name.
import m from "../../mithril.js";
import type * as Mithril from "mithril";

export interface AvatarAttrs {
	name: string;
	/** size in CSS pixels (square); defaults to 32. */
	size?: number;
}

/** avatarURL builds the static F-List avatar URL for a character. URLs are
 * cached by lowercased name: rosters redraw often and the mapping is fixed. */
export function avatarURL(name: string): string {
	const key = name.toLowerCase();
	let url = avatarURLs.get(key);
	if (url === undefined) {
		url = `https://static.f-list.net/images/avatar/${encodeURIComponent(key)}.png`;
		avatarURLs.set(key, url);
	}
	return url;
}

const avatarURLs = new Map<string, string>();

export const Avatar: Mithril.Component<AvatarAttrs> = {
	view: ({ attrs }) =>
		m("img.avatar", {
			src: avatarURL(attrs.name),
			// The avatar always sits beside the character's visible name, so it is
			// decorative: an empty alt keeps AT from announcing the name twice.
			alt: "",
			width: attrs.size ?? 32,
			height: attrs.size ?? 32,
			loading: "lazy",
		}),
};
