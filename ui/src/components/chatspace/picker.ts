import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useActions, useStore, useView } from "../../context.js";
import { memo, memoInit, request, type Memo } from "../../render.js";
import { bindTab } from "../../store/state.js";
import { Avatar } from "../primitives/Avatar.js";
import { Button, FormError } from "../primitives/form.js";
// CharacterPicker: the content of an unconnected session tab. Presents the
// account's characters as a centered, scrollable list with avatars and a
// per-row login button; the search field filters client-side.

/** NO_CHARACTERS is a stable empty account list, so the memo key does not
 * change identity on every render before the account lands. */
const NO_CHARACTERS: string[] = [];

export interface CharacterPickerAttrs {
	/** tabId is the unconnected tab this picker belongs to. */
	tabId: string;
}

export const CharacterPicker: Mithril.Component<CharacterPickerAttrs> = {
	oninit: (vnode) => {
		const state = vnode.state as PickerState;
		state.query = "";
		state.error = null;
		state.busyName = null;
		state.visible = memoInit<string[]>();
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const actions = useActions();
		const state = vnode.state as PickerState;
		const tabId = vnode.attrs.tabId;

		// Memoize the filtered/sorted list; it changes only with the account
		// list, the set of connected sessions, or the query.
		const accounts = store.account.characters ?? NO_CHARACTERS;
		const sessionsSig = Object.keys(store.sessions).join("\u0000");
		const q = state.query.trim().toLowerCase();
		const characters = memo(state.visible, [accounts, sessionsSig, q], () => {
			const lowercasedSessions = new Set(
				Object.keys(store.sessions).map((name) => name.toLowerCase()),
			);
			return accounts
				.filter(
					(name) =>
						!lowercasedSessions.has(name.toLowerCase()) &&
						name.toLowerCase().includes(q),
				)
				.sort((a, b) => a.localeCompare(b));
		});

		const login = (name: string): void => {
			state.busyName = name;
			state.error = null;
			void actions.loginCharacter(name).then((err) => {
				state.busyName = null;
				if (err !== null) {
					state.error = err;
					request();
					return;
				}
				// Bind this tab to the character. The tab keeps its identity
				// whether or not the session snapshot has arrived yet.
				bindTab(view, tabId, name);
				request();
			});
		};

		return m("div.character-picker", [
			m("h2.picker-title", "Log in a character"),
			m("input.picker-search", {
				type: "search",
				placeholder: "Filter characters…",
				value: state.query,
				autofocus: true,
				oninput: (e: Event) => {
					state.query = (e.target as HTMLInputElement).value;
				},
			}),
			characters.length === 0
				? m("p.picker-empty.muted", "No matching characters.")
				: m(
						"ul.picker-list",
						characters.map((name) =>
							m("li.picker-row", { key: name }, [
								m(Avatar, { name, size: 40 }),
								m("span.picker-name", name),
								m(Button, {
									label: "Log in",
									small: true,
									busy: state.busyName === name,
									busyLabel: "Logging in…",
									disabled: state.busyName !== null,
									onclick: () => login(name),
								}),
							]),
						),
					),
			m(FormError, { message: state.error }),
		]);
	},
};

interface PickerState {
	query: string;
	error: string | null;
	busyName: string | null;
	/** visible memoizes the filter/sort against its inputs. */
	visible: Memo<string[]>;
}
