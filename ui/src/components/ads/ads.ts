// ads.ts — the ads modal: the buffered-ad search tab, the post placeholder,
// and the dialog shell. Absorbs AdsDialog.ts, AdsSearch.ts, AdsPosting.ts.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { closeModal } from "../../store/state.js";
import { useStore, useView } from "../../context.js";
import { compareText } from "../../lib/order.js";
import { formatClock } from "../../lib/format.js";
import { request } from "../../render.js";
import type { Ad } from "../../transport/protocol.js";
import { MultiSelect, type MultiSelectID, type MultiSelectOption } from "../primitives/select.js";
import { FilterInput } from "../primitives/FilterInput.js";
import { CharacterLink } from "../presence/character.js";
import { fetchAds } from "../../api.js";
import { Dialog, DialogTabs } from "../primitives/dialog.js";


// ==========================================================================
// AdsPosting.ts
// ==========================================================================
// AdsPosting: the "Post" tab of the Ads dialog. It is a deliberate dummy for
// now — no LRP posting is wired up — so it only says so. The tab exists so the
// dialog's shape is stable; when posting lands it can reuse the generic
// Composer (fixed height, `showSend: false`) inside this tab.

export const AdsPosting: Mithril.Component = {
	view: () =>
		m(
			"p.ads-post-placeholder.muted",
			"Posting advertisements isn't available yet.",
		),
};

// ==========================================================================
// AdsSearch.ts
// ==========================================================================
// AdsSearch: the "Search" tab of the Ads dialog. It shows one session's
// buffered LRP advertisements (the shell fetches them once over HTTP and passes
// them in) with client-local filtering: two MultiSelects bound to the channels
// and posters actually present in the set, plus a debounced fulltext field over
// the rendered ad body. Nothing is persisted and nothing is re-fetched here, so
// a reopen starts clean.
//
// The speaker is a CharacterLink so a poster can be opened as a DM through the
// shared character menu. Presence is best-effort from the partial registry:
// ads arrive over channels the session is in, so most posters are already
// online in `store.characters`; an unknown name renders without a gender color
// and no network call is made. Ad bodies arrive as rendered HTML from the core
// (the client never parses BBCode), so they are trusted and skipped by Mithril.

export interface AdsSearchAttrs {
	ads: Ad[];
	session: string;
}

interface AdsSearchState {
	/** query is the live input value; filter is the debounced value. */
	query: string;
	filter: string;
	channels: MultiSelectID[];
	characters: MultiSelectID[];
	/** matches caches the filtered set, rebuilt only when a filter input or the
	 * ad buffer changes, so an unrelated redraw does not re-scan the buffer. */
	matches: Ad[];
	matchesRef?: Ad[];
	channelsRef?: MultiSelectID[];
	charactersRef?: MultiSelectID[];
	filterRef?: string;
	/** optionsRef is the ads identity the option lists were built from. */
	optionsRef?: Ad[];
	channelOptions: MultiSelectOption[];
	characterOptions: MultiSelectOption[];
}

export const AdsSearch: Mithril.Component<AdsSearchAttrs> = {
	oninit: (vnode) => {
		const state = vnode.state as AdsSearchState;
		state.query = "";
		state.filter = "";
		state.channels = [];
		state.characters = [];
		state.matches = [];
		state.channelOptions = [];
		state.characterOptions = [];
	},
	view: (vnode) => {
		const state = vnode.state as AdsSearchState;
		const store = useStore();
		const attrs = vnode.attrs;

		// Rebuild the option lists only when the ad set identity changes.
		if (state.optionsRef !== attrs.ads) {
			state.optionsRef = attrs.ads;
			state.channelOptions = unique(attrs.ads.map((a) => a.channel)).map(
				(name) => ({ id: name, name }),
			);
			state.characterOptions = unique(attrs.ads.map((a) => a.character)).map(
				(name) => ({ id: name, name }),
			);
		}

		if (
			state.matchesRef !== attrs.ads ||
			state.channelsRef !== state.channels ||
			state.charactersRef !== state.characters ||
			state.filterRef !== state.filter
		) {
			state.matchesRef = attrs.ads;
			state.channelsRef = state.channels;
			state.charactersRef = state.characters;
			state.filterRef = state.filter;
			const q = state.filter.trim().toLowerCase();
			state.matches = attrs.ads.filter((ad) => {
				if (state.channels.length > 0 && !state.channels.includes(ad.channel)) {
					return false;
				}
				if (
					state.characters.length > 0 &&
					!state.characters.includes(ad.character)
				) {
					return false;
				}
				return q === "" || ad.message.toLowerCase().includes(q);
			});
		}
		const matches = state.matches;

		return m("div.ads-search", [
			m("div.ads-filters", [
				m(MultiSelect, {
					label: "Channels",
					options: state.channelOptions,
					selected: state.channels,
					onchange: (ids: MultiSelectID[]) => {
						state.channels = ids;
					},
				}),
				m(MultiSelect, {
					label: "Characters",
					options: state.characterOptions,
					selected: state.characters,
					onchange: (ids: MultiSelectID[]) => {
						state.characters = ids;
					},
				}),
			]),
			m(FilterInput, {
				class: "ads-fulltext",
				placeholder: "Filter ads…",
				value: state.query,
				oninput: (value) => {
					state.query = value;
				},
				onfilter: (value) => {
					state.filter = value;
				},
			}),
			m(
				"p.ads-result-count",
				`${matches.length} of ${attrs.ads.length} ads`,
			),
			matches.length === 0
				? m(
						"p.ads-empty.muted",
						attrs.ads.length === 0 ? "No ads yet." : "No ads match.",
					)
				: m(
						"ul.ads-results",
						matches.map((ad) =>
							adRow(ad, store.characters[ad.character]?.gender),
						),
					),
		]);
	},
};

/** adRow renders one advertisement as a card: speaker, channel badge, time,
 * then the pre-rendered body. The row is keyed by poster because the buffer
 * holds at most one ad per character. */
function adRow(ad: Ad, gender: string | undefined): Mithril.Vnode {
	return m("li.ad-row", { key: ad.character }, [
		m("div.ad-head", [
			m(CharacterLink, { name: ad.character, gender, class: "ad-speaker" }),
			m("span.ad-channel", ad.channel),
			m("span.ad-time", formatClock(ad.receivedAt)),
		]),
		m("div.ad-body", m.trust(ad.message)),
	]);
}

/** unique returns the sorted, case-insensitively deduped values. */
function unique(values: string[]): string[] {
	return [...new Set(values)].sort(compareText);
}

// ==========================================================================
// AdsDialog.ts
// ==========================================================================
// AdsDialog: the LRP advertisement modal, bound to the active session like the
// character-search dialog. It owns the two tabs (Search, Post) and the single
// HTTP fetch of the session's buffered ads, so switching tabs does not refetch;
// the Search tab renders the fetched set and the Post tab is a placeholder for
// now. Closing unmounts it, so filter state resets on reopen.

type AdsTab = "search" | "post";

interface AdsDialogState {
	tab: AdsTab;
	ads: Ad[];
	loading: boolean;
	/** session is the session the ads were fetched for; a switch closes it. */
	session: string | null;
}

export const AdsDialog: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as AdsDialogState;
		state.tab = "search";
		state.ads = [];
		state.loading = true;
		state.session = null;
		const session = useView().activeSession;
		if (session === null) {
			state.loading = false;
			return;
		}
		state.session = session;
		// fetchAds reports an unavailable core as an empty list; the dialog
		// simply shows "No ads yet." rather than an error.
		void fetchAds(session).then((ads) => {
			state.ads = ads;
			state.loading = false;
			request();
		});
	},
	view: (vnode) => {
		const view = useView();
		const state = vnode.state as AdsDialogState;
		const session = view.activeSession;
		if (session === null || state.session !== session) {
			return null; // bound session closed or switched while open
		}
		const close = (): void => {
			closeModal(view);
		};
		return m(
			Dialog,
			{
				title: `Ads for ${session}`,
				onClose: close,
				class: "ads-dialog",
			},
			m(DialogTabs, {
				active: state.tab,
				fill: true,
				onSelect: (id) => {
					state.tab = id as AdsTab;
				},
				tabs: [
					{
						id: "search",
						label: "Search",
						render: () =>
							state.loading
								? m("p.ads-empty.muted", "Loading ads…")
								: m(AdsSearch, { ads: state.ads, session }),
					},
					{ id: "post", label: "Post", render: () => m(AdsPosting) },
				],
			}),
		);
	},
};
