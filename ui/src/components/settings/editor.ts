// editor.ts — the settings editor: the view plus the global, character, and
// this-device cards and their shared draft/save bookkeeping. Absorbs
// SettingsView.ts, GlobalSettingsCard.ts, CharacterSettingsCard.ts,
// ThisDeviceCard.ts, and document.ts.

import { request } from "../../render.js";
import type { SaveResult } from "../../api.js";
import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { fetchSettings, putGlobalSettings, resetGlobalSettings, type AutoStatus, type JoinTarget, putCharacterSettings, resetCharacterSettings } from "../../api.js";
import { Checkbox, FormError, Spinner, TextField } from "../primitives/form.js";
import { SettingsActions, AutoJoinList, HighlightList } from "./fields.js";
import { useStore, useView, useActions, type AppActions } from "../../context.js";
import { compareText } from "../../lib/order.js";
import { joinLabel } from "../../lib/format.js";
import type { Store } from "../../store/state.js";
import { setEnterNewline, setSoundEnabled } from "../../store/state.js";


// ==========================================================================
// document.ts
// ==========================================================================
// Shared draft/save bookkeeping for the settings cards. Each card owns its own
// fields and its own load (the documents differ), but the busy/status/dirty
// state and the save-or-reset write flow are identical, so they live here.

/** DocumentState is the common state slice of a settings card. */
export interface DocumentState {
	loading: boolean;
	error: string | null;
	busy: boolean;
	dirty: boolean;
	/** status is the transient "Saved."/"Reset…" line beside the title. */
	status: string | null;
	statusError: boolean;
}

/** initDocument seeds the shared state. Call from oninit before the load. */
export function initDocument(state: DocumentState): void {
	state.loading = true;
	state.error = null;
	state.busy = false;
	state.dirty = false;
	state.status = null;
	state.statusError = false;
}

/** writeDocument runs one save or reset: it raises the busy flag, clears the
 * status line, applies the result, and, on success, clears the dirty flag and
 * re-reads (`reload` should re-read without the spinner). */
export function writeDocument(
	state: DocumentState,
	write: () => Promise<SaveResult>,
	okMessage: string,
	reload: () => void,
): void {
	state.busy = true;
	state.status = null;
	void write().then((r) => {
		state.busy = false;
		state.statusError = !r.ok;
		state.status = r.ok ? okMessage : (r.error ?? "Request failed.");
		if (r.ok) {
			state.dirty = false;
			reload();
		}
		request();
	});
}

// ==========================================================================
// GlobalSettingsCard.ts
// ==========================================================================
// GlobalSettingsCard: the account-wide configuration scope. It owns its own
// draft: load on mount, edit locally, and PUT the whole document on Save. After
// a successful write it re-reads, because the core normalizes the document and
// the stored form may differ from what was typed.

interface GlobalState extends DocumentState {
	password: string;
	forgetBusy: boolean;
}

export const GlobalSettingsCard: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as GlobalState;
		initDocument(state);
		state.password = "";
		state.forgetBusy = false;
		reloadGlobal(state, true);
	},
	view: (vnode) => {
		const state = vnode.state as GlobalState;
		const store = useStore();
		const actions = useActions();
		return m("section.settings-card", [
			m("div.settings-card-head", [
				m("h3.settings-card-title", "Global"),
				m("span.settings-status", {
					class: state.statusError ? "is-error" : "",
				}, state.status ?? ""),
			]),
			state.loading
				? m(Spinner, { label: "Loading global settings…" })
				: state.error !== null
					? m(FormError, { message: state.error })
					: [
							m(TextField, {
								label: "Shared password",
								type: "password",
								value: state.password,
								autocomplete: "new-password",
								disabled: state.busy,
								oninput: (value) => {
									state.password = value;
									state.dirty = true;
									state.status = null;
								},
							}),
							m(
								"p.settings-field-note",
								"Empty disables the browser password gate. A change applies when the core restarts.",
							),
							m(SettingsActions, {
								busy: state.busy,
								dirty: state.dirty,
								onSave: () => saveGlobal(state),
								onReset: () => resetGlobal(state),
							}),
							store.account.persisted
								? m("div.settings-subsection", [
										m("span.settings-section-label", "Stored F-Chat credentials"),
										m(
											"p.settings-field-note",
											"The core will ask for your F-Chat credentials again on the next restart.",
										),
										m(
											"button.button.button-secondary",
											{
												type: "button",
												disabled: state.forgetBusy || state.busy,
												onclick: () => forgetStoredCredentials(state, actions),
											},
											state.forgetBusy ? "Forgetting…" : "Forget credentials",
										),
								  ])
								: null,
						],
		]);
	},
};

/** reload re-reads the global document. `spinner` is false when refreshing in
 * place after a write, so the form does not flicker. */
function reloadGlobal(state: GlobalState, spinner: boolean): void {
	if (spinner) {
		state.loading = true;
	}
	void fetchSettings().then((v) => {
		state.loading = false;
		if (v === null) {
			state.error = "Could not load the global configuration.";
			request();
			return;
		}
		state.error = null;
		state.password = v.global.password ?? "";
		if (spinner) {
			state.dirty = false;
		}
		request();
	});
}

function saveGlobal(state: GlobalState): void {
	writeDocument(
		state,
		() => putGlobalSettings({ password: state.password }),
		"Saved.",
		() => reloadGlobal(state, false),
	);
}

function resetGlobal(state: GlobalState): void {
	writeDocument(
		state,
		() => resetGlobalSettings(),
		"Reset to defaults.",
		() => reloadGlobal(state, false),
	);
}

/** forgetStoredCredentials deletes the persisted F-Chat pair. Running sessions
 * are unaffected; the credentials gate returns on the next core restart. */
function forgetStoredCredentials(state: GlobalState, actions: AppActions): void {
	state.forgetBusy = true;
	state.status = null;
	void actions.purgeCredentials().then((err) => {
		state.forgetBusy = false;
		state.statusError = err !== null;
		state.status = err ?? "Credentials will no longer be remembered.";
		request();
	});
}

// ==========================================================================
// CharacterSettingsCard.ts
// ==========================================================================
// CharacterSettingsCard: one connected character's configuration scope. Like
// the global card it owns a local draft, loaded on mount and saved whole. The
// auto-join list is managed: entries are pruned with the X, or the whole list
// is replaced with the channels the character is currently in.

interface CharacterAttrs {
	session: string;
}

interface CharacterState extends DocumentState {
	highlights: string[];
	autoJoin: JoinTarget[];
	autoStatus: AutoStatus | undefined;
	highlightDraft: string;
}

export const CharacterSettingsCard: Mithril.Component<CharacterAttrs> = {
	oninit: (vnode) => {
		const state = vnode.state as CharacterState;
		initDocument(state);
		state.highlights = [];
		state.autoJoin = [];
		state.autoStatus = undefined;
		state.highlightDraft = "";
		reloadCharacter(state, vnode.attrs.session, true);
	},
	view: (vnode) => {
		const state = vnode.state as CharacterState;
		const store = useStore();
		const session = vnode.attrs.session;
		const connection = store.sessions[session]?.state ?? "connecting";

		return m("section.settings-card", [
			m("div.settings-card-head", [
				m("span.session-dot", { class: `is-${connection}` }),
				m("h3.settings-card-title", session),
				m("span.settings-status", {
					class: state.statusError ? "is-error" : "",
				}, state.status ?? ""),
			]),
			state.loading
				? m(Spinner, { label: `Loading ${session}'s settings…` })
				: state.error !== null
					? m(FormError, { message: state.error })
					: [
							m("div.settings-subsection", [
								m("span.settings-section-label", "Highlights"),
								m("div.settings-add-row", [
									m("input.settings-add-input", {
										type: "text",
										placeholder: "Add a highlight…",
										value: state.highlightDraft,
										disabled: state.busy,
										oninput: (e: Event) => {
											state.highlightDraft = (e.target as HTMLInputElement).value;
										},
										onkeydown: (e: KeyboardEvent) => {
											if (e.key === "Enter") {
												e.preventDefault();
												addHighlight(state);
											}
										},
									}),
									m(
										"button.button.button-secondary.button-small",
										{
											type: "button",
											disabled: state.busy || state.highlightDraft.trim() === "",
											onclick: () => addHighlight(state),
										},
										"Add",
									),
								]),
								m(HighlightList, {
									values: state.highlights,
									disabled: state.busy,
									onRemove: (index) => removeHighlight(state, index),
								}),
							]),
							m(AutoJoinList, {
								entries: state.autoJoin,
								disabled: state.busy,
								joined: joinedTargets(store, session).length,
								onRemove: (index) => removeAutoJoin(state, index),
								onReplace: () => replaceAutoJoin(state, store, session),
							}),
							m(
								"p.settings-field-note",
								"Auto-join is attempted on the character's next login or reconnect.",
							),
							m("div.settings-subsection", [
								m("span.settings-section-label", "Automatic status"),
								state.autoStatus === undefined
									? m("p.settings-empty.muted", "No automatic status saved.")
									: m("div.settings-subsection-head", [
											m(
												"span.settings-auto-status",
												state.autoStatus.message !== undefined &&
													state.autoStatus.message !== ""
													? `${state.autoStatus.status} — ${state.autoStatus.message}`
													: state.autoStatus.status,
											),
											m(
												"button.button.button-secondary.button-small",
												{
													type: "button",
													disabled: state.busy,
													onclick: () => clearAutoStatus(state),
												},
												"Clear",
											),
									]),
								m(
									"p.settings-field-note",
									"Set from the status dialog; the core applies it after login.",
								),
							]),
							m(SettingsActions, {
								busy: state.busy,
								dirty: state.dirty,
								onSave: () => saveCharacter(state, session),
								onReset: () => resetCharacter(state, session),
							}),
						],
		]);
	},
};

/** reload re-reads one character's document. `spinner` is false when refreshing
 * in place after a write. */
function reloadCharacter(state: CharacterState, session: string, spinner: boolean): void {
	if (spinner) {
		state.loading = true;
	}
	void fetchSettings(session).then((v) => {
		state.loading = false;
		if (v === null) {
			state.error = `Could not load settings for ${session}.`;
			request();
			return;
		}
		state.error = null;
		state.highlights = [...(v.character.highlights ?? [])];
		state.autoJoin = (v.character.autoJoin ?? []).map((j) => ({
			kind: j.kind,
			id: j.id,
			name: j.name ?? j.id,
		}));
		state.autoStatus = v.character.autoStatus;
		if (spinner) {
			state.dirty = false;
			state.highlightDraft = "";
		}
		request();
	});
}

function addHighlight(state: CharacterState): void {
	const value = state.highlightDraft.trim();
	state.highlightDraft = "";
	if (value === "") {
		return;
	}
	if (state.highlights.some((h) => h.toLowerCase() === value.toLowerCase())) {
		return;
	}
	state.highlights = [...state.highlights, value];
	state.dirty = true;
	state.status = null;
}

function removeHighlight(state: CharacterState, index: number): void {
	state.highlights = state.highlights.filter((_, i) => i !== index);
	state.dirty = true;
	state.status = null;
}

function removeAutoJoin(state: CharacterState, index: number): void {
	state.autoJoin = state.autoJoin.filter((_, i) => i !== index);
	state.dirty = true;
	state.status = null;
}

/** clearAutoStatus drops the saved automatic status from the draft. */
function clearAutoStatus(state: CharacterState): void {
	state.autoStatus = undefined;
	state.dirty = true;
	state.status = null;
}

function replaceAutoJoin(state: CharacterState, store: Store, session: string): void {
	state.autoJoin = joinedTargets(store, session);
	state.dirty = true;
	state.status = null;
}

/** joinedTargets snapshots the channels and rooms the character is in now,
 * which is what "replace with joined" writes to auto-join. The store drops a
 * conversation when the character leaves it, so a non-DM conversation is joined. */
function joinedTargets(store: Store, session: string): JoinTarget[] {
	const per = store.conversations[session];
	if (per === undefined) {
		return [];
	}
	const out: JoinTarget[] = [];
	for (const conv of Object.values(per)) {
		if (conv.conv.kind !== "official" && conv.conv.kind !== "room") {
			continue;
		}
		out.push({
			kind: conv.conv.kind,
			id: conv.conv.id,
			name: conv.title !== undefined && conv.title !== "" ? conv.title : conv.conv.id,
		});
	}
	out.sort((a, b) => compareText(joinLabel(a), joinLabel(b)));
	return out;
}

function saveCharacter(state: CharacterState, session: string): void {
	writeDocument(
		state,
		() =>
			putCharacterSettings(session, {
				highlights: state.highlights,
				autoJoin: state.autoJoin,
				autoStatus: state.autoStatus,
			}),
		"Saved.",
		() => reloadCharacter(state, session, false),
	);
}

function resetCharacter(state: CharacterState, session: string): void {
	writeDocument(
		state,
		() => resetCharacterSettings(session),
		"Reset to defaults.",
		() => reloadCharacter(state, session, false),
	);
}

// ==========================================================================
// ThisDeviceCard.ts
// ==========================================================================
// ThisDeviceCard: browser-local preferences, persisted as one JSON document in
// localStorage (see store/persist.ts). Nothing here reaches the core, so there
// is no load or save round trip — a change applies and persists immediately.

export const ThisDeviceCard: Mithril.Component = {
	view: () => {
		const view = useView();
		return m("section.settings-card", [
			m("div.settings-card-head", [
				m("h3.settings-card-title", "This Device"),
			]),
			m(Checkbox, {
				label: "Notification sounds",
				checked: view.soundEnabled,
				onchange: (value) => setSoundEnabled(view, value),
			}),
			m(
				"p.settings-field-note",
				"Play the attention sound for an incoming DM or a channel highlight.",
			),
			m(Checkbox, {
				label: "Enter inserts a newline",
				checked: view.composerEnterNewline,
				onchange: (value) => setEnterNewline(view, value),
			}),
			m(
				"p.settings-field-note",
				"Off: Enter sends and Shift+Enter newlines. On: Enter newlines and Ctrl/Cmd+Enter sends.",
			),
			m(
				"p.settings-field-note",
				"Stored in this browser only; never sent to the core.",
			),
		]);
	},
};

// ==========================================================================
// SettingsView.ts
// ==========================================================================
// SettingsView: the configuration editor that replaces the chat workspace while
// the top-bar Config button is active. It shows browser-local device prefs, the
// global scope, plus one card per connected character (a session present in the
// store). Each card owns its own load/save; this view only decides what shows.

export const SettingsView: Mithril.Component = {
	view: () => {
		const store = useStore();
		const view = useView();
		const sessions = Object.keys(store.sessions).sort(compareText);

		return m("div.settings-view", [
			m("div.settings-head", [
				m("h2.settings-title", "Configuration"),
				m(
					"p.settings-subtitle.muted",
					"This browser, the core, and per-character configuration.",
				),
				m(
					"button.button.button-secondary.button-small",
					{
						type: "button",
						onclick: () => {
							view.settingsOpen = false;
						},
					},
					"Back to chat",
				),
			]),
			m(ThisDeviceCard),
			m(GlobalSettingsCard),
			m(
				"div.settings-characters",
				sessions.length === 0
					? m("p.settings-empty.muted", "No connected characters.")
					: sessions.map((name) =>
							m(CharacterSettingsCard, { key: name, session: name }),
						),
			),
		]);
	},
};
