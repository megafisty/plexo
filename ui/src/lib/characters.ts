// Character identity helpers: the canonical F-List profile URL and the gender
// to name-color mapping. Pure and dependency-free, shared by the roster, the
// character menu, and the sidebar/self widgets.

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