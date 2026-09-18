import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useDispatch, useStore, useView } from "../../context.js";
import { countLabel, overLimit, utf8Bytes } from "../../lib/format.js";
import { sendDraft } from "../../store/commands.js";
import { deleteDraft, flushDraft, readDraft, writeDraft } from "../../store/persist.js";
import { draftKey, setEnterNewline } from "../../store/state.js";
import { noteInput, noteSent, type TypingTarget } from "../../store/typing.js";
import { parseConvKey } from "../../transport/protocol.js";
import { autosize, measureMetrics, NATIVE_AUTOSIZE, naturalHeight, settleNow, trackResize, untrackResize, type AutosizeState } from "./autosize.js";
// Composer: draft input for the active conversation. Enter sends and
// Shift+Enter inserts a newline by default; a device-local toggle flips Enter to
// newline and Ctrl/Cmd+Enter to send for long-form posts. Drafts live in View,
// keyed per conversation, and are mirrored to localStorage so a reload never
// loses a long post.
//
// The input auto-grows with its content up to half the viewport, so a
// multi-paragraph post stays editable while the timeline above it remains
// visible; past that it scrolls. A byte counter (and the send button's enabled
// state) track the server's chat_max/priv_max so a post is not silently
// rejected as too long.
//
// A compact BBCode toolbar (bold/italic/strike/sub/sup/color/url) wraps the
// selection in the matching tag. Bold and italic also respond to Ctrl/Cmd+B and
// Ctrl/Cmd+I; both paths share one wrap helper. The bar shares the action row
// below the input so it adds no vertical space; the parameter tags (color, url)
// leave the value empty and put the caret there. The client never parses or
// renders BBCode itself.
//
// Performance. The textarea is deliberately uncontrolled while typing: its
// value lives in the DOM and is mirrored into View on input, with the automatic
// Mithril redraw suppressed, so a keystroke never re-renders the timeline. The
// draft is restored on mount (the pane is keyed by conversation, so switching
// remounts the composer) and the send button/counter are updated imperatively
// from nodes cached at mount, so a keystroke issues no DOM query.
//
// Autosizing (the one place a keystroke can force layout) and its resize
// tracking live in autosize.ts, with the full measurement model documented
// there.
/** FORMAT_BUTTONS is the BBCode subset offered in the composer. Parameterized
 * tags (color, url) have no picker yet: they wrap the selection and leave the
 * value empty for the user to fill. */
const FORMAT_BUTTONS: {
	tag: string;
	label: string;
	title: string;
	param: boolean;
	/** key is the modifier+key shortcut that applies the tag (Ctrl/Cmd+key). */
	key?: string;
	cls?: string;
}[] = [
	{ tag: "b", label: "B", title: "Bold", param: false, key: "b", cls: "composer-fmt-b" },
	{ tag: "i", label: "I", title: "Italic", param: false, key: "i", cls: "composer-fmt-i" },
	{ tag: "s", label: "S", title: "Strikethrough", param: false, cls: "composer-fmt-s" },
	{ tag: "sub", label: "x₂", title: "Subscript", param: false },
	{ tag: "sup", label: "x²", title: "Superscript", param: false },
	{ tag: "color", label: "A", title: "Color", param: true, cls: "composer-fmt-color" },
	{ tag: "url", label: "URL", title: "Link", param: true },
];

/** FORMAT_KEYS maps a shortcut key to its tag so Ctrl/Cmd+B and Ctrl/Cmd+I
 * reuse the toolbar's wrap path. Derived from FORMAT_BUTTONS, so a shortcut and
 * its button cannot drift apart. */
const FORMAT_KEYS: Record<string, string> = {};
for (const b of FORMAT_BUTTONS) {
	if (b.key !== undefined) {
		FORMAT_KEYS[b.key] = b.tag;
	}
}

interface ComposerState extends AutosizeState {
	/** sendBtn/count are the action-row nodes syncComposer updates. Cached at
	 * mount so a keystroke does not query the DOM. */
	sendBtn?: HTMLButtonElement;
	count?: HTMLElement;
	/** label/near/over memo the rendered counter so syncComposer only writes
	 * the DOM when what it displays actually changes. */
	label?: string;
	near?: boolean;
	over?: boolean;
}

export const Composer: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as ComposerState;
		state.border = 0;
		state.minH = 0;
		state.maxH = Infinity;
		state.floor = 0;
	},
	view: (vnode) => {
		const state = vnode.state as ComposerState;
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		// Resize hooks run outside a render; keep them pointed at the live View.
		state.refView = view;

		const session = view.activeSession;
		const key = session === null ? undefined : view.activeConv[session];
		if (session === null || key === undefined) {
			return null;
		}
		const conv = parseConvKey(key);
		const snap = store.sessions[session];
		const live = snap?.state === "live";
		const dkey = draftKey(session, conv);
		const value = view.drafts[dkey] ?? "";
		const typingTarget: TypingTarget = { session, key, conv, dispatch };
		// The message byte limit for this conversation kind; 0 until the server
		// reports it (VAR), in which case the counter shows bytes only.
		const limit = (conv.kind === "dm" ? snap?.privMax : snap?.chatMax) ?? 0;

		const reset = (el: HTMLTextAreaElement): void => {
			el.value = "";
			view.drafts[dkey] = "";
			deleteDraft(dkey);
			autosize(el, state);
			// Sending collapses a long draft at once rather than on the settle
			// timer, so the composer does not linger at full height behind the
			// just-sent message.
			settleNow(state);
			syncComposer(el, state, live, limit);
		};

		const send = (el: HTMLTextAreaElement | undefined): void => {
			if (!sendDraft(store, view, dispatch, session, key)) {
				return;
			}
			noteSent(typingTarget);
			if (el !== undefined) {
				reset(el);
			}
		};

		const multiline = view.composerEnterNewline;

		// applyTag wraps the current selection with a BBCode tag. Parameterized
		// tags leave the value empty and drop the caret there. The draft mirror is
		// updated here and the counter/send button are refreshed imperatively;
		// only the textarea's size and caret are touched otherwise.
		const applyTag = (tag: string, param: boolean): void => {
			const el = state.el;
			if (el === undefined) {
				return;
			}
			const caret = wrapTag(el, tag, param);
			const next = el.value;
			view.drafts[dkey] = next;
			writeDraft(dkey, next);
			noteInput(typingTarget, next);
			autosize(el, state);
			el.focus();
			el.setSelectionRange(caret, caret);
		};

		return m("div.composer", [
			m("textarea.composer-input", {
				disabled: !live,
				placeholder: live
					? conv.kind === "dm"
						? `Message ${conv.id}…`
						: `Message ${keyLabel(conv.id)}…`
					: "Session is not connected",
				rows: 2,
				oncreate: (vnode) => {
					const el = vnode.dom as HTMLTextAreaElement;
					state.el = el;
					// The whole composer subtree exists by the time oncreate runs,
					// so the action row can be cached here for syncComposer.
					const scope = el.closest(".composer") as HTMLElement | null;
					state.sendBtn =
						scope?.querySelector<HTMLButtonElement>(".composer-send") ??
						undefined;
					state.count =
						scope?.querySelector<HTMLElement>(".composer-count") ??
						undefined;
					// Measure the metrics and the empty-box floor before the draft is
					// restored, so the floor reflects the rows=2 intrinsic height.
					measureMetrics(el, state);
					if (NATIVE_AUTOSIZE) {
						// The engine owns the height; drop any stale inline value.
						el.style.height = "";
						state.floor = state.minH;
					} else {
						el.value = "";
						state.floor = naturalHeight(el, state);
					}
					// Restore the View copy first, else the persisted draft, so
					// the send path (which reads View) sees it immediately.
					let text = view.drafts[dkey];
					if (text === undefined) {
						text = readDraft(dkey) ?? "";
						view.drafts[dkey] = text;
					}
					el.value = text;
					if (!NATIVE_AUTOSIZE) {
						autosize(el, state);
					}
					syncComposer(el, state, live, limit);
					trackResize(el, state);
				},
				oninput: (e: Event) => {
					const el = e.target as HTMLTextAreaElement;
					const next = el.value;
					view.drafts[dkey] = next;
					writeDraft(dkey, next);
					noteInput(typingTarget, next);
					autosize(el, state);
					syncComposer(el, state, live, limit);
					// Keep the input responsive without a global redraw; the
					// debounce-free send path reads View.
					(e as Event & { redraw?: boolean }).redraw = false;
				},
				onblur: () => {
					// Settle now so a paused draft is never left taller than it is.
					settleNow(state);
					flushDraft(dkey);
				},
				onremove: () => {
					untrackResize(state);
					flushDraft(dkey);
				},
				onkeydown: (e: KeyboardEvent) => {
					// Escape drops focus so the global Alt+arrow shortcuts work again on
					// macOS, where they are otherwise left to the text field. Blur
					// flushes the draft.
					if (e.key === "Escape") {
						e.preventDefault();
						(e.target as HTMLTextAreaElement).blur();
						return;
					}
					const mod = e.ctrlKey || e.metaKey;
					// Ctrl/Cmd+B and Ctrl/Cmd+I wrap the selection, the same path as
					// the toolbar buttons. Alt is excluded so AltGr combos are not
					// hijacked.
					if (mod && !e.shiftKey && !e.altKey) {
						const tag = FORMAT_KEYS[e.key.toLowerCase()];
						if (tag !== undefined) {
							e.preventDefault();
							applyTag(tag, false);
							return;
						}
					}
					if (e.key !== "Enter") {
						return;
					}
					// In newline mode only Ctrl/Cmd+Enter sends; otherwise Enter
					// sends and Shift+Enter newlines.
					if (view.composerEnterNewline ? !mod : e.shiftKey) {
						return;
					}
					e.preventDefault();
					send(e.target as HTMLTextAreaElement);
				},
			}),
			m("div.composer-actions", [
				m(
					"div.composer-format",
					FORMAT_BUTTONS.map((b) => {
						const tip =
							b.key === undefined
								? b.title
								: `${b.title} (Ctrl/Cmd+${b.key.toUpperCase()})`;
						return m(
							"button",
							{
								class:
									"button button-small button-secondary composer-fmt-btn" +
									(b.cls === undefined ? "" : " " + b.cls),
								type: "button",
								title: tip,
								"aria-label": tip,
								"aria-keyshortcuts":
									b.key === undefined
										? undefined
										: `Control+${b.key.toUpperCase()} Meta+${b.key.toUpperCase()}`,
								disabled: !live,
								// Keep the textarea's selection: letting the button take
								// focus can drop the caret to the start.
								onmousedown: (e: Event) => e.preventDefault(),
								onclick: () => applyTag(b.tag, b.param),
							},
							b.label,
						);
					}),
				),
				m(
					"button.button.button-small.button-secondary.composer-mode",
					{
						type: "button",
						title: multiline
							? "Enter inserts a newline; Ctrl/Cmd+Enter sends"
							: "Enter sends; Shift+Enter inserts a newline. Click for newline mode.",
						onclick: () => {
							setEnterNewline(view, !view.composerEnterNewline);
						},
					},
					multiline ? "⏎ newline" : "⏎ sends",
				),
				m(
					"span.composer-count",
					countLabel(utf8Bytes(value.trim()), limit),
				),
				m(
					"button.button.composer-send",
					{
						type: "button",
						disabled: !live || value.trim() === "" || overLimit(value, limit),
						onclick: () => send(state.el),
					},
					"Send",
				),
			]),
		]);
	},
};

/** wrapTag wraps the textarea's selection in `[tag]…[/tag]` and returns the
 * caret position: the empty parameter for parameterized tags, else the empty
 * content when nothing was selected, else just past the closing tag. */
function wrapTag(el: HTMLTextAreaElement, tag: string, param: boolean): number {
	const start = el.selectionStart;
	const end = el.selectionEnd;
	const value = el.value;
	const selected = value.slice(start, end);
	const open = param ? `[${tag}=]` : `[${tag}]`;
	const insert = open + selected + `[/${tag}]`;
	el.value = value.slice(0, start) + insert + value.slice(end);
	if (param) {
		return start + open.length - 1; // between "=" and "]"
	}
	if (selected === "") {
		return start + open.length; // between the opening and closing tags
	}
	return start + insert.length; // just past the closing tag
}

/** syncComposer mirrors the draft's emptiness and byte count onto the send
 * button and counter without a redraw. The nodes are cached, and each write is
 * guarded by a memo, so a keystroke only touches the DOM where the rendered
 * result actually changes. */
function syncComposer(
	el: HTMLTextAreaElement,
	state: ComposerState,
	live: boolean,
	limit: number,
): void {
	const text = el.value.trim();
	const bytes = utf8Bytes(text);
	const over = limit > 0 && bytes > limit;

	const btn = state.sendBtn;
	if (btn !== undefined) {
		const disabled = !live || text === "" || over;
		if (btn.disabled !== disabled) {
			btn.disabled = disabled;
		}
	}

	const count = state.count;
	if (count === undefined) {
		return;
	}
	const label = countLabel(bytes, limit);
	if (label !== state.label) {
		count.textContent = label;
		state.label = label;
	}
	const near = limit > 0 && bytes >= limit * 0.8 && !over;
	if (near !== state.near) {
		toggleClass(count, "is-near", near);
		state.near = near;
	}
	if (over !== state.over) {
		toggleClass(count, "is-over", over);
		state.over = over;
	}
}

function toggleClass(el: Element, name: string, on: boolean): void {
	if (on) {
		el.classList.add(name);
	} else {
		el.classList.remove(name);
	}
}

function keyLabel(id: string): string {
	return id.startsWith("#") ? id : `# ${id}`;
}