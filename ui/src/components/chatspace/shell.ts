// shell.ts — the signed-in shell: Chatspace, its top bar, and the session
// tabs. Absorbs Chatspace.ts, TopBar.ts, and SessionTabs.ts.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { useActions, useDispatch, useStore, useView } from "../../context.js";
import { closeSession, clickHandlers } from "../../store/commands.js";
import { addTab, removeTab, MAX_TABS, closeModal, closePopout, dismissToast, toggleModal, type Modal } from "../../store/state.js";
import { FriendsMenu } from "../presence/menus.js";
import { CharacterPicker } from "./picker.js";
import { SessionView } from "./session.js";
import { JoinChannelDialog } from "../conversations/join.js";
import { CharacterMenu } from "../presence/menus.js";
import { StatusDialog } from "../presence/status.js";
import { SearchDialog } from "../search/search.js";
import { AdsDialog } from "../ads/ads.js";
import { LogsDialog } from "../logs/logs.js";
import { SettingsView } from "../settings/editor.js";
import { WarpmarkDialog, WarpmarksMenu } from "../warpmarks/warpmarks.js";


// ==========================================================================
// SessionTabs.ts
// ==========================================================================
// SessionTabs: one tab per character plus unconnected "new session" tabs,
// capped at MAX_TABS. Closing logs a character out or drops an unconnected tab;
// "+" opens another picker tab.

export const SessionTabs: Mithril.Component = {
	view: () => {
		const store = useStore();
		const view = useView();
		const actions = useActions();

		const canAdd = view.tabs.length < MAX_TABS;

		return m("nav.session-tabs", [
			view.tabs.map((tab) => {
				const name = tab.session;
				const session = name !== null ? store.sessions[name] : undefined;
				const active = view.activeTab === tab.id;
				return m("span.session-tab-wrap", { key: tab.id }, [
					m(
						"button.session-tab",
						{
							class: active ? "is-active" : "",
							type: "button",
							onclick: () => {
								view.settingsOpen = false;
								view.activeTab = tab.id;
							},
						},
						name !== null
							? [
									m("span.session-dot", {
										class: `is-${session?.state ?? "connecting"}`,
									}),
									m("span.session-name", name),
								]
							: m("span.session-name.muted", "New session"),
					),
					m("button.tab-close", {
						type: "button",
						title: name !== null ? `Log out ${name}` : "Discard tab",
						"aria-label": name !== null ? `Log out ${name}` : "Discard tab",
						onclick: () => {
							if (name !== null) {
								void closeSession(store, view, actions, name);
							} else {
								removeTab(view, tab.id);
							}
						},
					}, "×"),
				]);
			}),
			m(
				"button.session-add",
				{
					type: "button",
					title: canAdd ? "Add a session tab" : `At most ${MAX_TABS} tabs`,
					disabled: !canAdd,
					onclick: () => {
						view.settingsOpen = false;
						addTab(view);
					},
				},
				"+",
			),
		]);
	},
};

// ==========================================================================
// TopBar.ts
// ==========================================================================
// TopBar: session tabs, and the core connection indicator + brand on the right.
// When the core socket drops, the bar turns red and the brand text becomes a
// "Disconnected — refresh" hint; refreshing the page is the canonical recovery.

export function TopBar(): Mithril.Component {
	const store = useStore();
	const view = useView();

	return {
		view: () => {
			const disconnected = store.core.connection === "closed";
			const settingsOpen = view.settingsOpen;
			const searchOpen = view.modal?.kind === "search";
			const adsOpen = view.modal?.kind === "ads";
			const logsOpen = view.modal?.kind === "logs";
			const session = view.activeSession;
			// Search and Ads are bound to a live session, so those buttons only
			// appear with one.
			const sessionAvailable =
				session !== null && store.sessions[session] !== undefined;
			return m(
				"header.topbar",
				{ class: disconnected ? "is-disconnected" : undefined },
				[
					m(SessionTabs),
					m("div.topbar-status", [
						m(FriendsMenu),
						sessionAvailable
							? m(
									"button.search-button",
									{
										type: "button",
										class: searchOpen ? "is-open" : "",
										title: "Search characters by kink and profile filters",
										onclick: () => toggleModal(view, "search"),
									},
									"Search",
								)
							: null,
						sessionAvailable
							? m(
									"button.search-button",
									{
										type: "button",
										class: adsOpen ? "is-open" : "",
										title: "Browse this character's channel advertisements",
										onclick: () => toggleModal(view, "ads"),
									},
									"Ads",
								)
							: null,
						// Open the log browser. Unlike Search and Ads it needs no live
						// session: it reads the persisted timeline directly, so a
						// disconnected character's history is browsable too.
						m(
							"button.search-button",
							{
								type: "button",
								class: logsOpen ? "is-open" : "",
								title: "Browse and export chatlogs",
								onclick: () => toggleModal(view, "logs"),
							},
							"Logs",
						),
						m(WarpmarksMenu),
						m(
							"button.config-button",
							{
								type: "button",
								class: settingsOpen ? "is-open" : "",
								title: "Edit global and character configuration",
								onclick: () => {
									view.settingsOpen = !settingsOpen;
									if (view.settingsOpen) {
										closeModal(view);
										closePopout(view);
									}
								},
							},
							"Config",
						),
						m("span.connection", {
							class: `is-${store.core.connection}`,
							title: `Core connection: ${store.core.connection}`,
						}),
						m(
							"span.brand",
							disconnected ? "Disconnected — refresh" : "Plexo",
						),
					]),
				],
			);
		},
	};
}

// ==========================================================================
// Chatspace.ts
// ==========================================================================
// Chatspace: the signed-in shell. Sits directly under App's Chatspace gate.

/** renderModal mounts whichever dialog owns the single modal slot. */
function renderModal(modal: Modal): Mithril.Children {
	switch (modal.kind) {
		case "join":
			return m(JoinChannelDialog);
		case "status":
			return m(StatusDialog);
		case "search":
			return m(SearchDialog);
		case "ads":
			return m(AdsDialog);
		case "logs":
			return m(LogsDialog);
		case "warpmark":
			return m(WarpmarkDialog);
	}
}

export const Chatspace: Mithril.Component = {
	view: () => {
		const store = useStore();
		const view = useView();
		const dispatch = useDispatch();
		const actions = useActions();

		// Never show an empty workspace: ensure the active tab exists, and seed
		// one unconnected tab when none do.
		if (
			view.activeTab === null ||
			!view.tabs.some((t) => t.id === view.activeTab)
		) {
			view.activeTab = view.tabs[view.tabs.length - 1]?.id ?? addTab(view);
		}
		const tab = view.tabs.find((t) => t.id === view.activeTab) ?? null;

		return m(
			"div.chatspace",
			{ ...clickHandlers(store, view, dispatch, actions) },
			[
				m(TopBar),
				m(
					"main.chatspace-body",
					view.settingsOpen
						? m(SettingsView)
						: tab === null
							? null
							: tab.session !== null
								? m(SessionView)
								: m(CharacterPicker, { tabId: tab.id }),
				),
				view.modal !== null ? renderModal(view.modal) : null,
				view.characterMenu !== null ? m(CharacterMenu) : null,
				m("div.toast-host", view.toasts.map((t) =>
					m(
						"div.toast",
						{ key: t.id, onclick: () => dismissToast(view, t.id) },
						t.message,
					),
				)),
			],
		);
	},
};
