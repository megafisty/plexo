// Preloaded by `node --import ./test/setup.mjs` before the tests and their
// imports are evaluated. The store and transport modules touch a browser at
// module-evaluation time in exactly two places -- mithril.ts reads
// `globalThis.m`, and persist.ts registers a window listener -- so these stubs
// make the real compiled modules importable in Node with no source changes.
// Nothing here renders or networks.
const noop = () => {};

// mithril.ts re-exports the UMD global; render.ts's request() calls m.redraw().
// The real m is callable and carries these helpers, so the stub is too; tests
// that exercise list builders render rows with m(Component, attrs).
const mStub = (tag, attrs, children) => ({ tag, attrs, children });
mStub.redraw = noop;
mStub.render = noop;
mStub.mount = noop;
mStub.trust = (html) => html;
globalThis.m = mStub;

const memory = new Map();
globalThis.window = {
	location: { href: "http://localhost/" },
	addEventListener: noop,
	removeEventListener: noop,
	// Real timers so debounce/typing code behaves; tests here never rely on them.
	setTimeout: (fn, ms) => setTimeout(fn, ms),
	clearTimeout: (id) => clearTimeout(id),
	localStorage: {
		getItem: (k) => (memory.has(k) ? memory.get(k) : null),
		setItem: (k, v) => void memory.set(k, String(v)),
		removeItem: (k) => void memory.delete(k),
	},
};

globalThis.document = {
	addEventListener: noop,
	removeEventListener: noop,
};