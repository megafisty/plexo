// app.ts — the composition shell: gate selection, Chatspace mount, and the
// document-title side effect. Absorbs the former title.ts.

import m from "./mithril.js";
import type * as Mithril from "mithril";
import { useStore, useView } from "./context.js";
import { anyElevated } from "./store/unread.js";
import { CoreLoginGate } from "./components/auth/gates.js";
import { CredentialsGate } from "./components/auth/gates.js";
import { Chatspace } from "./components/chatspace/shell.js";
import { Spinner } from "./components/primitives/form.js";


// ==========================================================================
// app.ts
// ==========================================================================
// App is the shell: it selects the onboarding gate from View.phase and mounts
// the signed-in Chatspace. It owns no data of its own.
interface AppState {
	/** unreadRev is the store revision the title was last computed for. */
	unreadRev: number;
}

export const App: Mithril.Component = {
	oninit: (vnode) => {
		(vnode.state as AppState).unreadRev = -1;
	},
	// The document title reflects elevated unreads. Recompute only when the unread
	// revision changed (not every redraw), and updateTitle writes only on change.
	oncreate: (vnode) => syncTitle(vnode.state as AppState),
	onupdate: (vnode) => syncTitle(vnode.state as AppState),
	view: () => {
		const view = useView();
		switch (view.phase) {
			case "core-auth":
				return m("div.centered", m(CoreLoginGate));
			case "credentials":
				return m("div.centered", m(CredentialsGate));
			case "chatspace":
				return m(Chatspace);
			default:
				return m("div.centered", m(Spinner, { label: "Starting Plexo…" }));
		}
	},
};

/** syncTitle recomputes the elevated-unread title marker when the store's unread
 * revision changed since the last check. */
function syncTitle(state: AppState): void {
	const store = useStore();
	if (store.unreadRev === state.unreadRev) {
		return;
	}
	state.unreadRev = store.unreadRev;
	updateTitle(anyElevated(store));
}

// ==========================================================================
// title.ts
// ==========================================================================
// The document title carries a bubble while a severe unread (an unread DM or a
// channel highlight) exists anywhere. This is a DOM side effect, so it is
// driven once per redraw from the root component rather than from a view
// function. Assignment is guarded so the title is only written on change.

const BASE_TITLE = document.title !== "" ? document.title : "Plexo";
let current = BASE_TITLE;

/** updateTitle shows or clears the severe-unread marker in the document title. */
export function updateTitle(elevated: boolean): void {
	const next = elevated ? `💬 ${BASE_TITLE}` : BASE_TITLE;
	if (next === current) {
		return;
	}
	current = next;
	document.title = next;
}
