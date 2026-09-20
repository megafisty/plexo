// picker.ts — the two-sided character + conversation selector shared by the
// export and cleanup tabs. Two searchable fields that may be filled in either
// order; choosing one narrows the other to what actually has history, and
// changing one keeps the prior pick only when it is still valid (adopting the
// exact per-session id/name from the fresh list). The store is not consulted:
// logs are durable and readable for any character, connected or not. The parent
// owns the selection; the picker reports the resolved pair.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { fetchAllLogConversations, fetchLogCharacters, fetchLogConversations, fetchLogSessions } from "../../api.js";
import { memo, memoInit, request, type Memo } from "../../render.js";
import { Combobox, type ComboboxOption } from "../primitives/select.js";
import { FormError } from "../primitives/form.js";
import { logKindLabel } from "./labels.js";
import type { LogConvRef, LogSessionConv } from "../../transport/protocol.js";

export interface ConversationPickerAttrs {
	character: string | null;
	conversation: LogConvRef | null;
	/** onChange reports the resolved pair after either field changes. */
	onChange: (session: string | null, conv: LogConvRef | null) => void;
	disabled?: boolean;
}

interface ConversationPickerState {
	characters: string[];
	allConvs: LogConvRef[];
	/** convsForChar is null while loading, [] once loaded (possibly empty). */
	convsForChar: LogConvRef[] | null;
	/** sessionsForConv is null while loading, [] once loaded. */
	sessionsForConv: LogSessionConv[] | null;
	initError: boolean;
	/** Memoized option lists; rebuilt only when their source list changes, so
	 * the comboboxes do not re-index on every redraw. */
	charChoices: Memo<ComboboxOption[]>;
	convChoices: Memo<ComboboxOption[]>;
	/** request sequence guards drop responses for a superseded pick. */
	convSeq: number;
	charSeq: number;
}

export const ConversationPicker: Mithril.Component<
	ConversationPickerAttrs,
	ConversationPickerState
> = {
	oninit: (vnode) => {
		const state = vnode.state;
		state.characters = [];
		state.allConvs = [];
		state.convsForChar = null;
		state.sessionsForConv = null;
		state.initError = false;
		state.charChoices = memoInit();
		state.convChoices = memoInit();
		state.convSeq = 0;
		state.charSeq = 0;
		// Seed both fields so either can be the first pick.
		void Promise.all([fetchLogCharacters(), fetchAllLogConversations()]).then(
			([characters, conversations]) => {
				state.characters = characters ?? [];
				state.allConvs = conversations ?? [];
				state.initError = characters === null || conversations === null;
				request();
			},
		);
	},
	view: (vnode) => {
		const state = vnode.state;
		const attrs = vnode.attrs;
		// Each field's options come from the other field's selection: choosing a
		// conversation lists the characters with history in it, and vice versa.
		// The source arrays are stable across redraws, so the mapped combobox
		// options are memoized on their identity and only re-indexed when the
		// source actually changes.
		const characterSource: ReadonlyArray<string> | LogSessionConv[] | null =
			attrs.conversation !== null ? state.sessionsForConv : state.characters;
		const conversationSource: LogConvRef[] | null =
			attrs.character !== null ? state.convsForChar : state.allConvs;

		const charChoices = memo(state.charChoices, [characterSource], () => {
			const names: ReadonlyArray<string> =
				attrs.conversation !== null
					? (state.sessionsForConv ?? []).map((s) => s.session)
					: state.characters;
			return names.map((name) => ({ id: name, label: name }));
		});
		const convChoices = memo(state.convChoices, [conversationSource], () =>
			(conversationSource ?? []).map((conv) => ({
				id: convOptionKey(conv),
				label: conv.name,
				hint: logKindLabel(conv.kind),
			})),
		);

		const conversationLoading =
			attrs.character !== null && state.convsForChar === null;
		const characterLoading =
			attrs.conversation !== null && state.sessionsForConv === null;
		const characterCount = characterSource === null ? 0 : characterSource.length;

		return [
			m("div.logs-fields", [
				m("div.field", [
					m("span.field-label", "Character"),
					m(Combobox, {
						label: "Character",
						options: charChoices,
						selected: attrs.character,
						disabled: attrs.disabled,
						placeholder: characterLoading
							? "Loading…"
							: characterCount === 0
								? "No characters"
								: "Any character",
						clearable: true,
						onchange: (id: string | null) => pickCharacter(vnode, id),
					}),
				]),
				m("div.field", [
					m("span.field-label", "Conversation"),
					m(Combobox, {
						label: "Conversation",
						options: convChoices,
						selected:
							attrs.conversation === null
								? null
								: convOptionKey(attrs.conversation),
						disabled: attrs.disabled,
						placeholder: conversationLoading ? "Loading…" : "Any conversation",
						clearable: true,
						onchange: (id: string | null) =>
							pickConversation(
								vnode,
								id === null ? null : findConv(conversationSource ?? [], id),
							),
					}),
				]),
			]),
			m(FormError, {
				message: state.initError ? "The log index is unavailable." : null,
			}),
		];
	},
};

/** pickCharacter loads the chosen character's conversations. A conversation
 * picked for a previous character is kept only if the new one also has it. */
function pickCharacter(
	vnode: Mithril.Vnode<ConversationPickerAttrs, ConversationPickerState>,
	name: string | null,
): void {
	const state = vnode.state;
	const attrs = vnode.attrs;
	state.convsForChar = null;
	if (name === null) {
		attrs.onChange(null, attrs.conversation);
		return;
	}
	const seq = ++state.convSeq;
	void fetchLogConversations(name).then((conversations) => {
		if (seq !== state.convSeq) {
			return; // a newer pick superseded this one
		}
		const list = conversations ?? [];
		state.convsForChar = list;
		const current = attrs.conversation;
		const conv =
			current === null ? null : (list.find((c) => sameConv(c, current)) ?? null);
		attrs.onChange(name, conv);
		request();
	});
}

/** pickConversation loads the characters with history in the chosen
 * conversation. The current character is kept only if it is among them. */
function pickConversation(
	vnode: Mithril.Vnode<ConversationPickerAttrs, ConversationPickerState>,
	conv: LogConvRef | null,
): void {
	const state = vnode.state;
	const attrs = vnode.attrs;
	state.sessionsForConv = null;
	if (conv === null) {
		attrs.onChange(attrs.character, null);
		return;
	}
	const seq = ++state.charSeq;
	void fetchLogSessions(conv.kind, conv.id).then((sessions) => {
		if (seq !== state.charSeq) {
			return;
		}
		const list = sessions ?? [];
		state.sessionsForConv = list;
		const current = attrs.character;
		let session = current;
		if (current !== null) {
			const match = list.find((s) => stringsEqualFold(s.session, current));
			session = match === undefined ? null : match.session;
		}
		attrs.onChange(session, conv);
		request();
	});
}

/** convOptionKey is the case-folded identity a conversation field selects on. */
function convOptionKey(conv: LogConvRef): string {
	return `${conv.kind}\u0000${conv.id.toLowerCase()}`;
}

function sameConv(a: LogConvRef, b: LogConvRef): boolean {
	return a.kind === b.kind && a.id.toLowerCase() === b.id.toLowerCase();
}

function findConv(options: LogConvRef[], id: string): LogConvRef | null {
	return options.find((c) => convOptionKey(c) === id) ?? null;
}

function stringsEqualFold(a: string, b: string): boolean {
	return a.toLowerCase() === b.toLowerCase();
}