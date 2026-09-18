// DOM helpers shared by the global shortcut wiring and components that must
// yield to native key behavior.

/** isEditable reports whether an event target is a text entry (input, text
 * area, select, or contenteditable). Text entries own keys such as the arrows
 * and PageUp/PageDown, so global handlers must leave them alone. */
export function isEditable(target: EventTarget | null): boolean {
	if (!(target instanceof HTMLElement)) {
		return false;
	}
	if (target.isContentEditable) {
		return true;
	}
	const tag = target.tagName;
	return tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT";
}

/** isInteractive reports whether a target is a control the user can activate or
 * type into, where Enter has a native meaning (button, link, text entry). Used
 * to decide whether a global Enter may pull focus back to the composer. */
export function isInteractive(target: EventTarget | null): boolean {
	if (isEditable(target)) {
		return true;
	}
	if (!(target instanceof HTMLElement)) {
		return false;
	}
	switch (target.tagName) {
		case "BUTTON":
		case "A":
		case "SUMMARY":
			return true;
		default:
			break;
	}
	const role = target.getAttribute("role");
	return role === "button" || role === "link";
}