import { test } from "node:test";
import assert from "node:assert/strict";

import { devicePrefs, saveDevicePrefs } from "../app/store/persist.js";

// Seed the document before the first devicePrefs() call (module imports do not
// read it) so the parse path is exercised instead of the defaults.
window.localStorage.setItem(
	"plexo:device",
	JSON.stringify({ limitMessageWidth: true }),
);

test("device prefs read a known key and keep defaults for absent ones", () => {
	const prefs = devicePrefs();
	assert.equal(prefs.limitMessageWidth, true);
	// Keys missing from the stored document fall back to their defaults.
	assert.equal(prefs.soundEnabled, true);
	assert.equal(prefs.composerEnterNewline, false);
});

test("saveDevicePrefs writes the merged document", () => {
	saveDevicePrefs({ limitMessageWidth: false });
	assert.equal(devicePrefs().limitMessageWidth, false);
	assert.deepEqual(JSON.parse(window.localStorage.getItem("plexo:device")), {
		soundEnabled: true,
		composerEnterNewline: false,
		limitMessageWidth: false,
	});
});