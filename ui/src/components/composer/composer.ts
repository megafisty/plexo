import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { countLabel, pastedUrl, utf8Bytes } from "../../lib/format.js";
import { pure } from "../../render.js";
import { autosize, measureMetrics, NATIVE_AUTOSIZE, naturalHeight, settleNow, trackResize, untrackResize, type AutosizeState } from "./autosize.js";
// Composer: a reusable text editor for a BBCode message, shared by the chat
// conversation editor and (in fixed-height mode) by dialogs. It is deliberately
// generic: it never reads the store or the View and never sends anything. The
// caller owns the value and supplies the intents.
//
// Controlled value. `value` is a normal Mithril controlled attribute (the same
// shape as the TextField primitive): the caller updates its state in `oninput`
// and rerenders. Mithril writes the DOM only when the value differs and, for a
// textarea, skips an identical value so the caret never jumps. `oninput` must
// therefore update the caller's value synchronously and must pass the text back
// verbatim -- no trimming or normalization -- or every rerender would rewrite
// the field.
//
// The input stays responsive without a redraw: a caller that does not need its
// own UI to update per keystroke can set `suppressInputRedraw`, and the Composer
// opts the input event out of Mithril's automatic redraw (see the text editor's
// `MessageEditor`). The counter and send button are then updated imperatively
// from nodes cached at mount, so a keystroke costs no render. A caller that does
// want a redraw (a dialog enabling its own action button) leaves it off.
//
// The component instance is wrapped in render.pure and compares every attr the
// view reads except the callbacks; callers must therefore pass reference-stable
// callbacks. The editor passes callbacks that read its current state, so a
// skipped render still calls into live state. Without this the counter's byte
// scan would run on every unrelated redraw of the app (the composer sits in the
// conversation pane, which rerenders on every live event).
//
// Autosizing and its resize tracking live in autosize.ts, which documents the
// measurement model. Fixed-height mode (`autoGrow: false`) skips that machinery
// entirely and lets the textarea keep its `rows` height and scroll.
/** FORMAT_BUTTONS is the BBCode subset offered by the toolbar. A button with a
 * `start` opens the advanced palette pre-loaded to a sub-list; the others wrap
 * the selection directly. Parameterized tags without a picker (only when no
 * palette is available) wrap the selection and leave the value empty. */
const FORMAT_BUTTONS: {
	tag: string;
	label: string;
	title: string;
	param: boolean;
	/** key is the modifier+key shortcut that applies the tag directly
	 * (Ctrl/Cmd+key). */
	key?: string;
	/** palette is the chord letter of the composer palette that offers this tag
	 * (Ctrl/Cmd+palette). It is a tooltip hint only; `key` still owns direct
	 * application. */
	palette?: string;
	/** start opens the advanced palette on this sub-list instead of applying the
	 * tag directly (used when `onformat` is available). */
	start?: string;
	cls?: string;
}[] = [
	{ tag: "b", label: "B", title: "Bold", param: false, key: "b", cls: "composer-fmt-b" },
	{ tag: "i", label: "I", title: "Italic", param: false, key: "i", cls: "composer-fmt-i" },
	{ tag: "u", label: "U", title: "Underline", param: false, palette: "s", cls: "composer-fmt-u" },
	{ tag: "s", label: "S", title: "Strikethrough", param: false, palette: "s", cls: "composer-fmt-s" },
	{ tag: "sub", label: "x₂", title: "Subscript", param: false, palette: "s" },
	{ tag: "sup", label: "x²", title: "Superscript", param: false, palette: "s" },
	{ tag: "color", label: "A", title: "Color", param: true, palette: "d", start: "format-colors", cls: "composer-fmt-color" },
	{ tag: "url", label: "URL", title: "Link", param: true, palette: "u", start: "format-url" },
	{ tag: "user", label: "@", title: "Link Character", param: false, palette: "d", start: "format-character-source" },
	{ tag: "spoiler", label: "▓", title: "Spoiler", param: false, palette: "d" },
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

/** ComposerFormat applies a BBCode tag to the composer's current selection.
 * `value` fills a parameterized tag's parameter (`[color=value]`); `content`
 * replaces the selected text as the tag's body (`[url=value]content[/url]`),
 * which a palette uses when the body is not the selection. The composer creates
 * it when the caller opens the format palette and hands it over, so the palette
 * can format without holding the textarea itself. */
export type ComposerFormat = (
	tag: string,
	param: boolean,
	value?: string,
	content?: string,
) => void;

/** ComposerPalette names a command palette the composer can open itself, with
 * the apply closure for its own selection. */
export type ComposerPalette = "format-marks" | "format-advanced";

export interface ComposerAttrs {
	/** value is the caller-owned text. Read on every render; update it
	 * synchronously from `oninput`. */
	value: string;
	placeholder?: string;
	disabled?: boolean;
	/** rows is the textarea's intrinsic row count (default 2). */
	rows?: number;
	/** autoGrow sizes the box to its content up to max-height (default true).
	 * When false the box keeps its `rows` height and scrolls. */
	autoGrow?: boolean;
	/** class is appended to the root, for caller-specific layout (the chat
	 * editor's docked variant). */
	class?: string;
	/** limit is the byte ceiling; 0 or omitted shows a bare byte count. */
	limit?: number;
	/** showFormat toggles the BBCode toolbar and its Ctrl/Cmd+B/I shortcuts,
	 * plus the composer palettes (Ctrl/Cmd+S marks; Ctrl/Cmd+D/U advanced) when
	 * `onformat` is supplied. */
	showFormat?: boolean;
	/** showModeToggle toggles the Enter/newline-mode button. */
	showModeToggle?: boolean;
	/** showCount toggles the byte counter. */
	showCount?: boolean;
	/** showSend toggles the built-in Send button; a dialog with its own action
	 * row sets this false. */
	showSend?: boolean;
	sendLabel?: string;
	ariaLabel?: string;
	autofocus?: boolean;
	/** blurOnEscape blurs the field on Escape (default true). A dialog sets it
	 * false so Escape reaches its own close handler instead. */
	blurOnEscape?: boolean;
	/** enterNewline makes Enter insert a newline and Ctrl/Cmd+Enter send. */
	enterNewline?: boolean;
	/** suppressInputRedraw opts the input event out of Mithril's automatic
	 * redraw; the caller must then keep any derived UI updated itself. */
	suppressInputRedraw?: boolean;
	/** onformat asks the caller to open a composer palette. The composer hands
	 * over the command to mount, a closure that applies a chosen tag to the
	 * current selection, the selected text (or, for a pasted URL, that URL in its
	 * place), and an optional sub-list to open on, so the caller can carry all of
	 * them to the modal shell. */
	onformat?: (
		command: ComposerPalette,
		apply: ComposerFormat,
		selection: string,
		start?: string,
	) => void;

	/** oninput reports the field text on every edit. Must update the caller's
	 * value synchronously. */
	oninput: (value: string) => void;
	/** onsend reports a send intent (Enter, Ctrl/Cmd+Enter, or Send). The
	 * caller clears its own value if the send is accepted. */
	onsend?: (value: string) => void;
	onmodechange?: (enterNewline: boolean) => void;
	onblur?: (value: string) => void;
	/** onresize reports that the box changed height (autogrow only), so the
	 * caller can re-pin a list below it. */
	onresize?: (el: HTMLTextAreaElement) => void;
}

interface ComposerState extends AutosizeState {
	/** value is the last text the component saw, used to detect an external
	 * change in onupdate (its own edits update it first). */
	value: string;
	/** autoGrow clips the autosize paths for the instance's lifetime. */
	autoGrow: boolean;
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

const RawComposer: Mithril.Component<ComposerAttrs, ComposerState> = {
	oninit: (vnode) => {
		const state = vnode.state as ComposerState;
		state.border = 0;
		state.minH = 0;
		state.maxH = Infinity;
		state.floor = 0;
		state.value = vnode.attrs.value;
		state.autoGrow = vnode.attrs.autoGrow !== false;
	},
	view: (vnode) => {
		const attrs = vnode.attrs;
		const state = vnode.state as ComposerState;
		const autoGrow = attrs.autoGrow !== false;
		const showFormat = attrs.showFormat !== false;
		const showModeToggle = attrs.showModeToggle !== false;
		const showCount = attrs.showCount !== false;
		const showSend = attrs.showSend !== false;
		const multiline = attrs.enterNewline === true;
		state.autoGrow = autoGrow;
		// Keep the resize hook pointed at the caller's callback; the observer
		// fires outside a render and reads this field.
		state.onResize = attrs.onresize;

		// The counter text and the send button's disabled state are owned
		// imperatively by syncComposer, not rendered here: writing a text node
		// imperatively would detach the node Mithril keeps for a rendered text
		// child, so Mithril would then update the detached node and the visible
		// counter would go stale.
		return m("div.composer", { class: attrs.class }, [
			m("textarea.composer-input", {
				class: autoGrow ? undefined : "composer-input--fixed",
				name: "composer",
				value: attrs.value,
				rows: attrs.rows ?? 2,
				disabled: attrs.disabled === true,
				placeholder: attrs.placeholder,
				"aria-label": attrs.ariaLabel,
				oncreate: (vnode) => mountTextarea(vnode, state, attrs),
				onupdate: (vnode) => updateTextarea(vnode, state, attrs),
				oninput: (e: Event) => inputTextarea(e, state, attrs),
				onpaste: (e: Event) => onpasteTextarea(e, state, attrs, showFormat),
				onblur: (e: Event) => {
					if (state.autoGrow) {
						settleNow(state);
					}
					attrs.onblur?.((e.target as HTMLTextAreaElement).value);
				},
				onremove: () => {
					if (state.autoGrow) {
						untrackResize(state);
					}
				},
				onkeydown: (e: KeyboardEvent) =>
					keydownTextarea(e, state, attrs, showFormat, multiline),
			}),
			m("div.composer-actions", [
				showFormat
					? m(
							"div.composer-format",
							FORMAT_BUTTONS.map((b) => {
								// A direct shortcut wins; otherwise advertise the palette chord
								// that offers this tag.
								const shortcut = b.key ?? b.palette;
								const tip =
									shortcut === undefined
										? b.title
										: `${b.title} (Ctrl/Cmd+${shortcut.toUpperCase()})`;
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
										disabled: attrs.disabled === true,
										// Keep the textarea's selection: letting the
										// button take focus can drop the caret.
										onmousedown: (e: Event) => e.preventDefault(),
										// A button that opens a palette routes there when the caller
										// can carry a palette; otherwise it wraps directly.
										onclick: () => {
											if (b.start !== undefined && attrs.onformat !== undefined) {
												openFormat(state, attrs, "format-advanced", b.start);
											} else {
												applyTag(state, attrs, b.tag, b.param);
											}
										},
									},
									b.label,
								);
							}),
						)
					: null,
				showModeToggle
					? m(
							"button.button.button-small.button-secondary.composer-mode",
							{
								type: "button",
								disabled: attrs.disabled === true,
								title: multiline
									? "Enter inserts a newline; Ctrl/Cmd+Enter sends"
									: "Enter sends; Shift+Enter inserts a newline. Click for newline mode.",
								onclick: () =>
									attrs.onmodechange?.(!multiline),
							},
							multiline ? "⏎ newline" : "⏎ sends",
						)
					: null,
				showCount ? m("span.composer-count") : null,
				showSend && attrs.onsend !== undefined
					? m(
							"button.button.composer-send",
							{
								type: "button",
								onclick: () => send(state, attrs),
							},
							attrs.sendLabel ?? "Send",
						)
					: null,
			]),
		]);
	},
};

/** Composer is the controlled editor. See the module header for the caller
 * contracts (synchronous `value`, verbatim text, stable callbacks). */
export const Composer: Mithril.Component<ComposerAttrs, ComposerState> = pure(
	RawComposer,
	sameComposerAttrs,
);

/** sameComposerAttrs is the pure() equality check. It compares every attr the
 * view and its lifecycle hooks read. Callbacks are intentionally excluded:
 * callers must pass reference-stable callbacks (the chat editor's read its own
 * live state), and comparing fresh inline closures here would defeat the
 * skip. */
function sameComposerAttrs(next: ComposerAttrs, prev: ComposerAttrs): boolean {
	return (
		next.value === prev.value &&
		next.placeholder === prev.placeholder &&
		next.disabled === prev.disabled &&
		next.rows === prev.rows &&
		next.autoGrow === prev.autoGrow &&
		next.class === prev.class &&
		next.limit === prev.limit &&
		next.showFormat === prev.showFormat &&
		next.showModeToggle === prev.showModeToggle &&
		next.showCount === prev.showCount &&
		next.showSend === prev.showSend &&
		next.sendLabel === prev.sendLabel &&
		next.ariaLabel === prev.ariaLabel &&
		next.autofocus === prev.autofocus &&
		next.blurOnEscape === prev.blurOnEscape &&
		next.enterNewline === prev.enterNewline &&
		next.suppressInputRedraw === prev.suppressInputRedraw
	);
}

/** mountTextarea caches the nodes syncComposer writes, measures the autosize
 * floor, restores nothing (the controlled value is already in the DOM -- attrs
 * are applied before oncreate), and starts resize tracking. */
function mountTextarea(
	vnode: Mithril.VnodeDOM<ComposerAttrs, ComposerState>,
	state: ComposerState,
	attrs: ComposerAttrs,
): void {
	const el = vnode.dom as HTMLTextAreaElement;
	state.el = el;
	state.value = attrs.value;
	// The whole composer subtree exists by the time oncreate runs, so the
	// action row can be cached here for syncComposer.
	const scope = el.closest(".composer") as HTMLElement | null;
	state.sendBtn =
		scope?.querySelector<HTMLButtonElement>(".composer-send") ?? undefined;
	state.count =
		scope?.querySelector<HTMLElement>(".composer-count") ?? undefined;

	if (state.autoGrow) {
		// Measure the metrics and the empty-box floor before sizing. The floor
		// reflects the rows intrinsic height.
		measureMetrics(el, state);
		if (NATIVE_AUTOSIZE) {
			// The engine owns the height; drop any stale inline value.
			el.style.height = "";
			state.floor = state.minH;
		} else {
			const text = el.value;
			el.value = "";
			state.floor = naturalHeight(el, state);
			el.value = text;
			autosize(el, state);
		}
		trackResize(el, state);
	}
	syncComposer(el, state, attrs);
	if (attrs.autofocus === true && attrs.disabled !== true) {
		el.focus();
	}
}

/** updateTextarea runs on every component update. It re-measures the box for an
 * external value change (Mithril has already written the DOM) and always
 * refreshes the counter/send button, so a late limit or a disabled session is
 * reflected. A change the component made itself (typing, a toolbar wrap) has
 * already updated `state.value`, so a suppressed keystroke never pays for a
 * measure on the following redraw. */
function updateTextarea(
	vnode: Mithril.VnodeDOM<ComposerAttrs, ComposerState>,
	state: ComposerState,
	attrs: ComposerAttrs,
): void {
	const el = vnode.dom as HTMLTextAreaElement;
	if (state.value !== attrs.value) {
		state.value = attrs.value;
		if (state.autoGrow) {
			autosize(el, state);
		}
	}
	// Refresh the counter and send button for external changes: a new value, a
	// limit that arrived late, or a disabled session.
	syncComposer(el, state, attrs);
}

/** inputTextarea mirrors the field into the caller and keeps the counter and
 * send button current imperatively. When the caller opted out of redraws it
 * also cancels Mithril's automatic redraw for the event. */
function inputTextarea(
	e: Event,
	state: ComposerState,
	attrs: ComposerAttrs,
): void {
	const el = e.target as HTMLTextAreaElement;
	const next = el.value;
	state.value = next;
	if (state.autoGrow) {
		autosize(el, state);
	}
	syncComposer(el, state, attrs);
	if (attrs.suppressInputRedraw === true) {
		(e as Event & { redraw?: boolean }).redraw = false;
	}
	attrs.oninput(next);
}

/** onpasteTextarea intercepts a paste that is exactly one http(s) URL and,
 * instead of inserting it, opens the advanced palette's URL list in link-text
 * mode with the pasted URL as the link target. The URL rides the palette's
 * `selection` slot, which is what the URL list branches on, so nothing is
 * inserted and closing the palette leaves the composer untouched. A paste over
 * selected text falls through to the browser's normal paste, as does a clipboard
 * that is not a single bare URL. */
function onpasteTextarea(
	e: Event,
	state: ComposerState,
	attrs: ComposerAttrs,
	showFormat: boolean,
): void {
	const el = state.el;
	if (
		!showFormat ||
		attrs.onformat === undefined ||
		el === undefined ||
		el.selectionStart !== el.selectionEnd
	) {
		return;
	}
	const text =
		(e as ClipboardEvent).clipboardData?.getData("text/plain") ?? "";
	const url = pastedUrl(text);
	if (url === undefined) {
		return;
	}
	e.preventDefault();
	openFormat(state, attrs, "format-advanced", "format-url", url);
}

/** keydownTextarea implements Escape, the BBCode shortcuts, and the send-key
 * routing. */
function keydownTextarea(
	e: KeyboardEvent,
	state: ComposerState,
	attrs: ComposerAttrs,
	showFormat: boolean,
	enterNewline: boolean,
): void {
	const el = e.target as HTMLTextAreaElement;
	// Escape drops focus so the global Alt+arrow shortcuts work again on macOS,
	// where they are otherwise left to the text field. A dialog turns this off
	// so Escape reaches its own close handler.
	if (e.key === "Escape") {
		if (attrs.blurOnEscape !== false) {
			e.preventDefault();
			el.blur();
		}
		return;
	}
	const mod = e.ctrlKey || e.metaKey;
	// Ctrl/Cmd+B and Ctrl/Cmd+I wrap the selection, the same path as the toolbar
	// buttons. Alt is excluded so AltGr combos are not hijacked.
	if (showFormat && mod && !e.shiftKey && !e.altKey) {
		const key = e.key.toLowerCase();
		// The composer owns these chords (not shortcuts.ts) because it is the only
		// place that can hand the palette the closure that applies a chosen tag to
		// the current selection. Ctrl/Cmd+S is the marks palette; Ctrl/Cmd+D (the
		// color mnemonic) and the Ctrl/Cmd+U backup (the url mnemonic) are the
		// advanced, parameterized palette.
		const command: ComposerPalette | null =
			key === "s"
				? "format-marks"
				: key === "d" || key === "u"
					? "format-advanced"
					: null;
		if (command !== null && attrs.onformat !== undefined) {
			e.preventDefault();
			openFormat(state, attrs, command);
			return;
		}
		const tag = FORMAT_KEYS[key];
		if (tag !== undefined) {
			e.preventDefault();
			applyTag(state, attrs, tag, false);
			return;
		}
	}
	// Without a send intent (a plain dialog editor), Enter keeps its native
	// newline behavior instead of being swallowed.
	if (attrs.onsend === undefined) {
		return;
	}
	if (!isSendKey(e, enterNewline)) {
		return;
	}
	e.preventDefault();
	send(state, attrs);
}

/** isSendKey reports whether a keydown should send: Enter, or Ctrl/Cmd+Enter in
 * newline mode. Shift+Enter inserts a newline in send mode. Pure, so the
 * routing is unit-testable. */
export function isSendKey(
	e: Pick<KeyboardEvent, "key" | "shiftKey" | "ctrlKey" | "metaKey">,
	enterNewline: boolean,
): boolean {
	if (e.key !== "Enter") {
		return false;
	}
	const mod = e.ctrlKey || e.metaKey;
	return enterNewline ? mod : !e.shiftKey;
}

/** applyTag wraps the current selection with a BBCode tag. Parameterized tags
 * leave the value empty and drop the caret there. Otherwise the entire
 * wrapped tag stays selected, so a following tag nests around it. The DOM and
 * the caller's value are updated here; the caller's redraw (or the imperative
 * sync when it suppresses redraws) refreshes the counter. */
function applyTag(
	state: ComposerState,
	attrs: ComposerAttrs,
	tag: string,
	param: boolean,
	paramValue?: string,
	content?: string,
): void {
	const el = state.el;
	if (el === undefined) {
		return;
	}
	const wrapped = wrapSelection(
		el.value,
		el.selectionStart,
		el.selectionEnd,
		tag,
		param,
		paramValue,
		content,
	);
	el.value = wrapped.value;
	state.value = wrapped.value;
	if (state.autoGrow) {
		autosize(el, state);
	}
	el.focus();
	el.setSelectionRange(wrapped.start, wrapped.end);
	attrs.oninput(wrapped.value);
}

/** openFormat asks the caller to open a composer palette, handing over the apply
 * closure and the text selected now (the palette branches on it, e.g. whether
 * the selection is a URL). `start` names a sub-list the advanced palette opens
 * on. `selectionOverride` replaces the snapshot with text that is not in the
 * field yet (the paste hook passes the pasted URL so the URL list treats it as
 * the selection). Snapshotting is safe: moving focus into the palette does not
 * disturb the textarea's selection. */
function openFormat(
	state: ComposerState,
	attrs: ComposerAttrs,
	command: ComposerPalette,
	start?: string,
	selectionOverride?: string,
): void {
	if (attrs.onformat === undefined) {
		return;
	}
	const el = state.el;
	const selection =
		selectionOverride ??
		(el === undefined
			? ""
			: el.value.slice(el.selectionStart, el.selectionEnd));
	attrs.onformat(
		command,
		(tag, param, value, content) =>
			applyTag(state, attrs, tag, param, value, content),
		selection,
		start,
	);
}

/** wrapSelection wraps `value[start:end]` in `[tag]…[/tag]` and returns the new
 * value and the selection to restore: the empty parameter for parameterized
 * tags, the empty content when nothing was selected, else the entire wrapped
 * tag so it can be wrapped again. `content` overrides the selected text as the
 * tag body. Pure, so it is unit-testable. */
export function wrapSelection(
	value: string,
	start: number,
	end: number,
	tag: string,
	param: boolean,
	paramValue?: string,
	content?: string,
): { value: string; start: number; end: number } {
	const selected = value.slice(start, end);
	const body = content === undefined ? selected : content;
	const open = param
		? paramValue === undefined
			? `[${tag}=]`
			: `[${tag}=${paramValue}]`
		: `[${tag}]`;
	const insert = open + body + `[/${tag}]`;
	const next = value.slice(0, start) + insert + value.slice(end);
	let selStart: number;
	let selEnd: number;
	if (param && paramValue === undefined) {
		selStart = selEnd = start + open.length - 1; // between "=" and "]"
	} else if (body === "") {
		selStart = selEnd = start + open.length; // between the opening and closing tags
	} else {
		selStart = start; // the whole wrapped tag
		selEnd = start + insert.length;
	}
	return { value: next, start: selStart, end: selEnd };
}

/** send reports a send intent; the caller clears its own value if accepted. */
function send(state: ComposerState, attrs: ComposerAttrs): void {
	const el = state.el;
	if (el === undefined) {
		return;
	}
	attrs.onsend?.(el.value);
}

/** syncComposer mirrors the field's emptiness and byte count onto the send
 * button and counter without a redraw. The nodes are cached, and each write is
 * guarded by a memo, so a keystroke only touches the DOM where the rendered
 * result actually changes. */
function syncComposer(
	el: HTMLTextAreaElement,
	state: ComposerState,
	attrs: ComposerAttrs,
): void {
	const text = el.value.trim();
	const bytes = utf8Bytes(text);
	const over = (attrs.limit ?? 0) > 0 && bytes > (attrs.limit ?? 0);

	if (attrs.showSend !== false) {
		const btn = state.sendBtn;
		if (btn !== undefined) {
			const disabled = attrs.disabled === true || text === "" || over;
			if (btn.disabled !== disabled) {
				btn.disabled = disabled;
			}
		}
	}

	if (attrs.showCount === false) {
		return;
	}
	const count = state.count;
	if (count === undefined) {
		return;
	}
	const limit = attrs.limit ?? 0;
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