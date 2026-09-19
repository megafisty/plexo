// Character identity helpers: the canonical F-List profile URL, the gender
// to name-color mapping, and the "seen" online-roster projection. Pure (no
// store, view, or network access), shared by the roster, the character menu,
// and the sidebar/self widgets.

import type { Character } from "../store/state.js";
import { compareText } from "./order.js";

/** GENDER_CLASS maps an F-List gender to the name-color class. The key set
 * mirrors the genders from the F-List mapping-list API
 * (POST /json/api/mapping-list.php, `listitems` named "gender"); anything
 * else falls through to gender-unknown. */
const GENDER_CLASS: Record<string, string> = {
	male: "gender-male",
	female: "gender-female",
	transgender: "gender-transgender",
	herm: "gender-herm",
	shemale: "gender-shemale",
	// No dedicated color yet; render neutral. Revisit later.
	"male-herm": "gender-unknown",
	"cunt-boy": "gender-unknown",
	none: "gender-unknown",
};

// Gender strings are a tiny, repeated set; normalize once and reuse the result
// so long lists do not allocate per render.
const genderClasses = new Map<string, string>();

/** genderClass maps an F-List gender to the name-color class. */
export function genderClass(gender?: string): string {
	const key = gender ?? "";
	let cls = genderClasses.get(key);
	if (cls === undefined) {
		cls = GENDER_CLASS[key.trim().toLowerCase()] ?? "gender-unknown";
		genderClasses.set(key, cls);
	}
	return cls;
}

/** profileURL is the canonical F-List profile page for a character. F-List
 * resolves any case, so the name is lower-cased and percent-encoded (spaces
 * included) for a stable, shareable link. */
export function profileURL(name: string): string {
	return `https://www.f-list.net/c/${encodeURIComponent(name.toLowerCase())}`;
}

/** seenOnlineNames returns, alphabetically, the names of every character the
 * client holds a live online presence record for. It is the "seen" roster the
 * character picker searches: exactly the characters the core has told this
 * client about (channel/room co-members, friends, DM partners), never a
 * network search. `exclude` drops names that must not be offered, such as the
 * user's own logged-in characters. */
export function seenOnlineNames(
	characters: Readonly<Record<string, Character>>,
	exclude?: ReadonlySet<string>,
): string[] {
	const names: string[] = [];
	for (const name in characters) {
		const record = characters[name];
		if (record === undefined || !record.online) {
			continue;
		}
		if (exclude !== undefined && exclude.has(name)) {
			continue;
		}
		names.push(name);
	}
	return names.sort(compareText);
}