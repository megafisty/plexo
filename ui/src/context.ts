import type { Store } from "./store/state.js";
import type { View } from "./store/state.js";
import type { CommandInput } from "./transport/protocol.js";
// The composition root's context: the one sanctioned place a component may
// reach for shared state. It is provided once by main.ts and read through the
// use* hooks below. No other globals are allowed in components.

/** A command without a CID; the dispatcher assigns one. */
export type { CommandInput };

/** Dispatch sends a command to the core and returns its cid. */
export type Dispatch = (cmd: CommandInput) => string;

export interface AppActions {
	/** authenticate signs in to the core with the shared Plexo password. */
	authenticate(password: string): Promise<boolean>;
	/** logout clears the core session and closes the socket. */
	logout(): Promise<void>;
	/** joinChannel sends a join command and resolves once the core acks or
	 * rejects it. Resolves with an error message on rejection. */
	joinChannel(session: string, kind: "official" | "room", id: string): Promise<string | null>;
	/** createRoom creates a closed, invite-only private room with the given
	 * title via the room_admin op; resolves with an error message on rejection.
	 * The new room's ADH id arrives with the server's self JCH. */
	createRoom(session: string, title: string): Promise<string | null>;
	/** loginCharacter starts a session for a character; resolves with an error
	 * message on rejection. */
	loginCharacter(character: string): Promise<string | null>;
	/** logoutCharacter stops a session; resolves with an error message on
	 * rejection. */
	logoutCharacter(character: string): Promise<string | null>;
	/** setCredentials validates F-Chat credentials and optionally remembers
	 * them; resolves with an error message on rejection. */
	setCredentials(account: string, password: string, remember: boolean): Promise<string | null>;
	/** purgeCredentials deletes stored F-Chat credentials; resolves with an
	 * error message on rejection. Running sessions are unaffected. */
	purgeCredentials(): Promise<string | null>;
}

export interface AppContext {
	store: Store;
	view: View;
	dispatch: Dispatch;
	actions: AppActions;
}

let current: AppContext | null = null;

/** provideApp installs the context. Called once, from main.ts. */
export function provideApp(ctx: AppContext): void {
	current = ctx;
}

function useApp(): AppContext {
	if (current === null) {
		throw new Error("plexo: app context was not provided");
	}
	return current;
}

export function useStore(): Store {
	return useApp().store;
}

export function useView(): View {
	return useApp().view;
}

export function useDispatch(): Dispatch {
	return useApp().dispatch;
}

export function useActions(): AppActions {
	return useApp().actions;
}