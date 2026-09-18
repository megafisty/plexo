import type { Dispatch } from "./context.js";
import { isEditable, isInteractive } from "./lib/dom.js";
import { request } from "./render.js";
import { cycleConversation } from "./store/commands.js";
import type { Store } from "./store/state.js";
import { cycleTab, dialogOpen, type View } from "./store/state.js";
// Shortcuts: global keyboard navigation for the signed-in shell.
//
//   Alt+Left / Alt+Right  switch session tabs
//   Alt+Up   / Alt+Down   step through the conversation sidebar
//   Enter                 focus the composer (unless a control has focus)
//
// The conversation order matches the sidebar exactly (channels then DMs, each
// sorted by title) because both read lib/convorder.
//
// The handler is platform-split, the way `clickHandlers` is split by
// pointer capability:
//
//   - macOS passes all four Option+arrows through to an editable element (its
//     native word/paragraph movement) and claims all four elsewhere. The
//     composer's Escape binding blurs it so navigation is reachable again.
//   - everywhere else all four arrows are claimed, text field focused or not.
//
// Navigation is ignored while a modal owns the screen, but it stays live while
// the composer has focus — drafts are per-conversation (View.drafts mirrored to
// localStorage), so switching away mid-post loses nothing.

/** IS_MAC selects the text-field-reserving variant: Option+arrows are native
 * while an editable element has focus. iPads report "MacIntel" and have the
 * same text-field behavior, so they take it too. */
const IS_MAC =
	typeof navigator !== "undefined" &&
	/(Mac|iPhone|iPad|iPod)/.test(navigator.platform || navigator.userAgent);

/** handleDefault claims all four Alt+arrows. Returns true when it consumed the
 * event. */
function handleDefault(
	store: Store,
	view: View,
	dispatch: Dispatch,
	e: KeyboardEvent,
): boolean {
	switch (e.key) {
		case "ArrowLeft":
			cycleTab(view, -1);
			return true;
		case "ArrowRight":
			cycleTab(view, 1);
			return true;
		case "ArrowUp":
			return stepConversation(store, view, dispatch, -1);
		case "ArrowDown":
			return stepConversation(store, view, dispatch, 1);
		default:
			return false;
	}
}

/** handleMac passes every Option+arrow through to an editable element (macOS
 * word/paragraph movement) and otherwise behaves like handleDefault. */
function handleMac(
	store: Store,
	view: View,
	dispatch: Dispatch,
	e: KeyboardEvent,
): boolean {
	if (isEditable(e.target)) {
		return false;
	}
	return handleDefault(store, view, dispatch, e);
}

/** stepConversation claims the key only when the sidebar is on screen; in the
 * Config view (which replaces the workspace) it passes through. */
function stepConversation(
	store: Store,
	view: View,
	dispatch: Dispatch,
	delta: number,
): boolean {
	if (view.settingsOpen || view.activeSession === null) {
		return false;
	}
	cycleConversation(store, view, dispatch, delta);
	return true;
}

/** installShortcuts attaches the platform-appropriate keydown listener and
 * returns its remover. */
export function installShortcuts(
	store: Store,
	view: View,
	dispatch: Dispatch,
): () => void {
	const handle = IS_MAC ? handleMac : handleDefault;
	const onKey = (e: KeyboardEvent): void => {
		if (view.phase !== "chatspace" || dialogOpen(view)) {
			return;
		}
		// A plain Enter anywhere that isn't already a control pulls focus back to
		// the composer. A focused control keeps its own Enter meaning.
		if (
			e.key === "Enter" &&
			!e.altKey &&
			!e.ctrlKey &&
			!e.metaKey &&
			!e.shiftKey
		) {
			if (isInteractive(document.activeElement)) {
				return;
			}
			const composer =
				document.querySelector<HTMLTextAreaElement>(".composer-input");
			if (composer === null || composer.disabled) {
				return;
			}
			e.preventDefault();
			composer.focus();
			return;
		}
		// Exact Alt-only chord: !ctrl excludes AltGr (reported as Ctrl+Alt).
		if (!e.altKey || e.ctrlKey || e.metaKey || e.shiftKey) {
			return;
		}
		if (!handle(store, view, dispatch, e)) {
			return;
		}
		e.preventDefault();
		request();
	};
	document.addEventListener("keydown", onKey);
	return () => document.removeEventListener("keydown", onKey);
}