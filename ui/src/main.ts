import m from "./mithril.js";
import { loginSession, logoutSession, probeSession } from "./api.js";
import { App } from "./app.js";
import { provideApp, type Dispatch } from "./context.js";
import { request } from "./render.js";
import { installShortcuts } from "./shortcuts.js";
import { primeAudio } from "./sound.js";
import { markConvRead } from "./store/commands.js";
import { resubscribeActive } from "./store/interest.js";
import { createStore } from "./store/state.js";
import { createView, pushToast } from "./store/state.js";
import { type CommandInput, OPS } from "./transport/protocol.js";
import { createBroker } from "./transport/broker.js";
import { connect, type CommandResult, type Transport } from "./transport/ws.js";

const root = document.getElementById("app");
if (root === null) {
	throw new Error("plexo: #app mount point not found");
}

const store = createStore();
const view = createView();
view.focused = !document.hidden && document.hasFocus();

// Focus tracking: unread is only cleared when the tab itself is focused, so
// switching to a conversation in a background tab does not mark it read.
function setUiFocused(next: boolean): void {
	if (next === view.focused) {
		return;
	}
	view.focused = next;
	if (next) {
		const session = view.activeSession;
		const key = session === null ? undefined : view.activeConv[session];
		if (session !== null && key !== undefined) {
			markConvRead(store, view, session, key);
		}
	}
	request();
}
window.addEventListener("focus", () => setUiFocused(true));
window.addEventListener("blur", () => setUiFocused(false));
document.addEventListener("visibilitychange", () =>
	setUiFocused(!document.hidden),
);

// Unlock audio on the first user gesture; browsers block programmatic playback
// until then, and an elevated message can arrive before the user interacts.
function primeOnce(): void {
	primeAudio();
	window.removeEventListener("pointerdown", primeOnce);
	window.removeEventListener("keydown", primeOnce);
}
window.addEventListener("pointerdown", primeOnce);
window.addEventListener("keydown", primeOnce);

let transport: Transport | null = null;
// The request/response commands (join, login, credentials) are the only ones
// whose result the UI awaits; the broker bounds and flushes them so none can
// hang. Fire-and-forget sends still go through Dispatch.
const broker = createBroker((cmd) => transport?.send(cmd) ?? "");

/** actionError maps a command result to the error string an AppAction returns,
 * or null on success. */
function actionError(r: CommandResult, fallback: string): string | null {
	return r.accepted ? null : (r.errorMsg ?? fallback);
}

function openTransport(): void {
	if (transport !== null) {
		return;
	}
	transport = connect(store, view, {
		onResult: (cid, result) => broker.resolve(cid, result),
		// The core keeps interest on the durable subscription for this Transport's
		// subscribe id, and reports whether this connection resumed it. Re-assert
		// only on a fresh subscription: a resume already has the interest, and
		// re-asserting it would force a needless re-materialization.
		onHello: (resumed) => {
			if (!resumed) {
				resubscribeActive(store, view, dispatch);
			}
		},
		// A dropped socket can never deliver the ack; fail the waiters now rather
		// than leave the dialogs that await them spinning until the timeout.
		onClose: () => broker.abort("Connection lost."),
	});
}

/** RESTORE_TIMEOUT_MS bounds how long the boot spinner waits for the core's
 * first account_state before falling back to the credentials gate, so a core
 * that never answers cannot strand the client on the spinner. */
const RESTORE_TIMEOUT_MS = 8000;

/** openTransportRestoring opens the core socket while keeping the boot spinner
 * up. The core reports its account state immediately on connect, so a warm core
 * that already holds credentials goes straight to the chatspace instead of
 * flashing the credentials gate first. */
function openTransportRestoring(): void {
	openTransport();
	window.setTimeout(() => {
		if (view.phase === "boot") {
			view.phase = "credentials";
			request();
		}
	}, RESTORE_TIMEOUT_MS);
}

const dispatch: Dispatch = (cmd: CommandInput) => transport?.send(cmd) ?? "";

provideApp({
	store,
	view,
	dispatch,
	actions: {
		authenticate: async (password) => {
			const ok = await loginSession(password);
			if (!ok) {
				return false;
			}
			store.core.authenticated = true;
			view.phase = "boot";
			openTransportRestoring();
			request();
			return true;
		},
		logout: async () => {
			await logoutSession();
			transport?.close();
			transport = null;
			broker.abort("Signed out.");
			store.core.authenticated = false;
			view.phase = "core-auth";
			request();
		},
		joinChannel: (session, kind, id) =>
			broker
				.ask({ op: OPS.join, session, conv: { kind, id } })
				.then((r) => actionError(r, "Join failed.")),
		createRoom: (session, title) =>
			broker
				.ask({ op: OPS.roomAdmin, session, room: { action: "create", title } })
				.then((r) => actionError(r, "Could not create room.")),
		roomAdmin: (session, conv, room) =>
			broker
				.ask({ op: OPS.roomAdmin, session, conv, room })
				.then((r) => actionError(r, "Room action failed.")),
		loginCharacter: (character) =>
			broker
				.ask({ op: OPS.login, character })
				.then((r) => actionError(r, "Login failed.")),
		logoutCharacter: (character) =>
			broker
				.ask({ op: OPS.logout, session: character })
				.then((r) => actionError(r, "Logout failed.")),
		setCredentials: async (account, password, remember) => {
			const r = await broker.ask({
				op: OPS.setCredentials,
				account,
				password,
				remember,
			});
			if (r.accepted) {
				return null;
			}
			// A valid pair that could not be stored is not a sign-in failure: the
			// session works, only the restart convenience is missing.
			if (r.errorCode === "persist_failed") {
				pushToast(view, "Credentials accepted but could not be remembered.");
				return null;
			}
			return r.errorMsg ?? "Sign-in failed.";
		},
		purgeCredentials: () =>
			broker
				.ask({ op: OPS.purgeCredentials })
				.then((r) => actionError(r, "Could not forget credentials.")),
	},
});

installShortcuts(store, view, dispatch);

async function boot(): Promise<void> {
	const probe = await probeSession();
	store.core.authRequired = probe.authRequired;
	store.core.authenticated = probe.authenticated;
	if (probe.authRequired && !probe.authenticated) {
		view.phase = "core-auth";
	} else {
		// Stay on the spinner: applyAccount moves to the credentials gate only
		// when the core actually reports missing/bad credentials.
		openTransportRestoring();
	}
	request();
}

m.mount(root, App);
void boot();
