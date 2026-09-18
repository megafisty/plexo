import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useDispatch, useStore, useView, type Dispatch } from "../../context.js";
import { sendDraft } from "../../store/commands.js";
import { deleteDraft, flushDraft, readDraft, writeDraft } from "../../store/persist.js";
import { draftKey, isMsgPinned, setEnterNewline, type Store, type View } from "../../store/state.js";
import { noteInput, noteSent, type TypingTarget } from "../../store/typing.js";
import { parseConvKey } from "../../transport/protocol.js";
import { Composer } from "../composer/composer.js";
// MessageEditor: the chat-side container for the generic Composer. It owns
// everything the Composer deliberately does not: the active conversation's
// draft (View + localStorage), the DM typing signal, sending, the byte limit,
// live gating, the device send-key preference, and timeline re-pinning. The
// Composer itself is presentational and reusable; a dialog wires the same
// component to its own state instead of through here.
//
// Performance. The Composer is rendered with `suppressInputRedraw`, so a
// keystroke never redraws the app. That means this container must not depend on
// a redraw per keystroke: drafting, persistence and typing all happen in the
// Composer's oninput, and the counter/send button are updated inside the
// Composer. The container's callbacks are created once in oninit and read the
// live store/view/session out of `state` (refreshed each render), so the
// Composer's render.pure can keep its callback references stable.

interface EditorState {
	/** Refs are `ref`-prefixed because Mithril reserves `view` on a component's
	 * state object (it is the render method): writing `state.view` shadows it and
	 * breaks every subsequent redraw. Same convention as MessageList. */
	refStore?: Store;
	refView?: View;
	refDispatch?: Dispatch;
	session: string | null;
	key?: string;
	dkey?: string;
	typing?: TypingTarget;
	/** The callback identities are stable for the component's lifetime; they
	 * read the current values from the fields above. */
	oninput: (value: string) => void;
	onsend: (value: string) => void;
	onmodechange: (enterNewline: boolean) => void;
	onblur: (value: string) => void;
	onresize: (el: HTMLTextAreaElement) => void;
}

export const MessageEditor: Mithril.Component<Record<string, never>, EditorState> = {
	oninit: (vnode) => {
		const state = vnode.state as EditorState;
		state.session = null;

		state.oninput = (value: string): void => {
			const { refView, dkey, typing } = state;
			if (refView === undefined || dkey === undefined) {
				return;
			}
			refView.drafts[dkey] = value;
			writeDraft(dkey, value);
			if (typing !== undefined) {
				noteInput(typing, value);
			}
		};

		state.onsend = (): void => {
			const { refStore, refView, refDispatch, session, key, typing, dkey } = state;
			if (
				refStore === undefined ||
				refView === undefined ||
				refDispatch === undefined ||
				session === null ||
				key === undefined ||
				dkey === undefined
			) {
				return;
			}
			if (!sendDraft(refStore, refView, refDispatch, session, key)) {
				return;
			}
			if (typing !== undefined) {
				noteSent(typing);
			}
			// sendDraft drops the View copy; also drop the persisted one so the
			// just-sent text cannot be restored by the lazy draft read.
			deleteDraft(dkey);
		};

		state.onmodechange = (enterNewline: boolean): void => {
			if (state.refView !== undefined) {
				setEnterNewline(state.refView, enterNewline);
			}
		};

		state.onblur = (): void => {
			if (state.dkey !== undefined) {
				flushDraft(state.dkey);
			}
		};

		// The autosizer calls this after the box changes height. It only does
		// anything when the timeline behind it is pinned to the live edge.
		state.onresize = (el: HTMLTextAreaElement): void => {
			const { refView, session, key } = state;
			if (refView === undefined || session === null || key === undefined) {
				return;
			}
			if (!isMsgPinned(refView, session, key)) {
				return;
			}
			const list = el
				.closest(".conversation-pane")
				?.querySelector(".message-list");
			if (list !== null && list !== undefined) {
				(list as HTMLElement).scrollTop = (list as HTMLElement).scrollHeight;
			}
		};
	},
	onremove: (vnode) => {
		// Persist a draft's trailing keystrokes before the pane switches away
		// (the write throttle may not have flushed them yet).
		const state = vnode.state as EditorState;
		if (state.dkey !== undefined) {
			flushDraft(state.dkey);
		}
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const state = vnode.state as EditorState;
		state.refStore = store;
		state.refView = view;
		state.refDispatch = dispatch;

		const session = view.activeSession;
		const key = session === null ? undefined : view.activeConv[session];
		if (session === null || key === undefined) {
			return null;
		}
		const conv = parseConvKey(key);
		const snap = store.sessions[session];
		const live = snap?.state === "live";
		const dkey = draftKey(session, conv);
		state.session = session;
		state.key = key;
		state.dkey = dkey;
		state.typing = { session, key, conv, dispatch };
		// The message byte limit for this conversation kind; 0 until the server
		// reports it (VAR), in which case the counter shows bytes only.
		const limit = (conv.kind === "dm" ? snap?.privMax : snap?.chatMax) ?? 0;

		// Restore the View copy first, else the persisted draft, so the send
		// path (which reads View) sees it immediately. The Composer seeds its
		// controlled value from this on mount.
		let text = view.drafts[dkey];
		if (text === undefined) {
			text = readDraft(dkey) ?? "";
			view.drafts[dkey] = text;
		}

		return m(Composer, {
			value: text,
			disabled: !live,
			placeholder: live
				? conv.kind === "dm"
					? `Message ${conv.id}…`
					: `Message ${keyLabel(conv.id)}…`
				: "Session is not connected",
			limit,
			enterNewline: view.composerEnterNewline,
			suppressInputRedraw: true,
			class: "composer--docked",
			oninput: state.oninput,
			onsend: state.onsend,
			onmodechange: state.onmodechange,
			onblur: state.onblur,
			onresize: state.onresize,
		});
	},
};

function keyLabel(id: string): string {
	return id.startsWith("#") ? id : `# ${id}`;
}