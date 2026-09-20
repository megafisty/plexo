// Mithril is vendored as a UMD bundle (ui/vendor/mithril.min.js) and loaded by
// a classic <script> tag in index.html, which attaches it to the global scope
// as `m`. Re-export it as a typed ES module so the rest of the app imports it
// normally instead of reaching for a global.
import type * as Mithril from "mithril";

const m: Mithril.Static = (globalThis as typeof globalThis & { m: Mithril.Static }).m;

export default m;
