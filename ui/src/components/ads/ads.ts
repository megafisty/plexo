// ads.ts — the ads modal: the buffered-ad search tab, the campaign post tab,
// and the dialog shell.

import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { closeModal } from "../../store/state.js";
import { useStore, useView } from "../../context.js";
import { compareText } from "../../lib/order.js";
import { formatClock } from "../../lib/format.js";
import { memo, memoInit, request, type Memo } from "../../render.js";
import type { Ad, AdBody, AdChannel, AdsStatus, AdTargetStatus } from "../../transport/protocol.js";
import type { Store } from "../../store/state.js";
import { MultiSelect, type ComboboxOption, type MultiSelectID, type MultiSelectOption, Combobox } from "../primitives/select.js";
import { Button, Checkbox, FormError, Spinner, TextField } from "../primitives/form.js";
import { PreviewField } from "../composer/previewfield.js";
import { FilterInput } from "../primitives/FilterInput.js";
import { CharacterLink } from "../presence/character.js";
import { fetchAds, fetchAdsCampaign, putAdsCampaign } from "../../api.js";
import { Dialog, DialogTabs } from "../primitives/dialog.js";
import {
	adBody,
	assignAdToAll,
	channelAllowsAds,
	channelKey,
	deleteAd,
	emptyCampaign,
	findTarget,
	mergeAvailable,
	nextEligibleLabel,
	normalizeCampaign,
	removeChannel,
	saveAd,
	setChannelAd,
	type CampaignDraft,
} from "./posting.js";


// ==========================================================================
// AdsPosting.ts
// ==========================================================================
// AdsPosting is the "Post" tab: the per-character advertisement campaign. It
// edits a local draft and saves it whole (like the settings cards) with an
// on/off switch, a named-body editor built on the shared Composer+Preview, and
// the channel assignment table. On open it merges every currently joined
// channel that allows ads into the draft, so a campaign starts from the
// channels the character is actually in. A chat-only channel shows a note in
// place of its ad selector. The server paces posting, so the table shows each
// channel's next eligible time from the live ads/<character> status record.

export interface AdsPostingAttrs {
	session: string;
}

interface AdsPostState {
	loading: boolean;
	error: string | null;
	campaign: CampaignDraft;
	/** status is the last HTTP-fetched/returned status; a live store record takes
	 * precedence over it. */
	status: AdsStatus | undefined;
	selected: string | null;
	nameDraft: string;
	bodyDraft: string;
	dirty: boolean;
	busy: boolean;
	message: string | null;
	messageError: boolean;
	/** resetKey drops a stale preview when the loaded ad changes. */
	resetKey: number;
}

export const AdsPosting: Mithril.Component<AdsPostingAttrs> = {
	oninit: (vnode) => {
		const state = vnode.state as AdsPostState;
		state.loading = true;
		state.error = null;
		state.campaign = emptyCampaign();
		state.status = undefined;
		state.selected = null;
		state.nameDraft = "";
		state.bodyDraft = "";
		state.dirty = false;
		state.busy = false;
		state.message = null;
		state.messageError = false;
		state.resetKey = 0;
		void loadCampaign(state, vnode.attrs.session);
	},
	view: (vnode) => {
		const state = vnode.state as AdsPostState;
		const session = vnode.attrs.session;
		const store = useStore();
		// A live status record, when the core has streamed one, is fresher than the
		// value returned by the campaign fetch or save.
		const status = store.ads[session] ?? state.status;
		const now = Date.now();
		if (state.loading) {
			return m(Spinner, { label: `Loading the ad campaign for ${session}…` });
		}
		if (state.error !== null) {
			return m(FormError, { message: state.error });
		}
		return m("div.ads-post", [
			m("div.ads-post-head", [
				m(Checkbox, {
					label: "Automatically post ads",
					checked: state.campaign.enabled,
					disabled: state.busy,
					onchange: (value) => toggleEnabled(state, value),
				}),
				m(
					"span.ads-post-status",
					{ class: state.messageError ? "is-error" : "" },
					state.message ?? "",
				),
			]),
			m("div.ads-post-editor", [
				m(AdNamePanel, {
					ads: state.campaign.ads,
					selected: state.selected,
					busy: state.busy,
					onselect: (name) => selectAd(state, name),
				}),
				m(AdEditor, {
					name: state.nameDraft,
					body: state.bodyDraft,
					busy: state.busy,
					canDelete: state.selected !== null,
					resetKey: state.resetKey,
					onName: (value) => editName(state, value),
					onBody: (value) => {
						state.bodyDraft = value;
						state.message = null;
					},
					onSave: () => saveAdDraft(state),
					onDelete: () => deleteAdDraft(state),
				}),
			]),
			m("div.ads-post-assign", [
				m(Button, {
					label: "Assign to all channels",
					variant: "secondary",
					small: true,
					disabled: state.busy || state.selected === null,
					onclick: () => assignAll(state),
				}),
				m("p.field-note", "Sets every channel below to the selected ad."),
			]),
			m(AdChannelTable, {
				rows: state.campaign.channels.map((ch) => ({
					ch,
					target: findTarget(status, ch),
				})),
				adOptions: state.campaign.ads.map((a) => ({ id: a.name, label: a.name })),
				busy: state.busy,
				running: status?.running === true,
				now,
				onSetAd: (ch, id) => setAd(state, ch, id),
				onRemove: (ch) => removeChannelFromDraft(state, ch),
			}),
			m("div.ads-post-foot", [
				m(Button, {
					label: "Save",
					busy: state.busy,
					disabled: !state.dirty,
					onclick: () => saveCampaign(state, session),
				}),
				m(
					"p.field-note",
					"Posting is paced by the server: one ad per channel every ten minutes.",
				),
			]),
		]);
	},
};

/** AdNamePanel lists the saved named bodies; selecting one loads it into the
 * editor. A name not in the list saves as a new ad. */
interface AdNamePanelAttrs {
	ads: AdBody[];
	selected: string | null;
	busy: boolean;
	onselect: (name: string) => void;
}

const AdNamePanel: Mithril.Component<AdNamePanelAttrs> = {
	view: ({ attrs }) =>
		m("div.ads-name-panel", [
			m("div.ads-name-head", m("span.settings-section-label", "Ads")),
			attrs.ads.length === 0
				? m("p.ads-empty.muted", "No ads saved.")
				: m(
						"ul.ads-name-list",
						attrs.ads.map((a) =>
							m(
								"li",
								{ key: a.name },
								m(
									"button.ads-name",
									{
										type: "button",
										class: attrs.selected === a.name ? "is-active" : "",
										disabled: attrs.busy,
										onclick: () => attrs.onselect(a.name),
									},
									a.name,
								),
							),
						),
					),
		]),
};

/** AdEditor is the named-body editor: the name field, the shared Composer +
 * Preview, and the Save ad / Delete ad actions. It is presentational; the
 * owning tab holds the draft and the selection. */
interface AdEditorAttrs {
	name: string;
	body: string;
	busy: boolean;
	canDelete: boolean;
	/** resetKey drops a stale preview when the loaded ad changes. */
	resetKey: number;
	onName: (value: string) => void;
	onBody: (value: string) => void;
	onSave: () => void;
	onDelete: () => void;
}

const AdEditor: Mithril.Component<AdEditorAttrs> = {
	view: ({ attrs }) =>
		m("div.ads-editor", [
			m(TextField, {
				label: "Name",
				value: attrs.name,
				disabled: attrs.busy,
				oninput: attrs.onName,
				onsubmit: attrs.onSave,
			}),
			m(PreviewField, {
				value: attrs.body,
				oninput: attrs.onBody,
				label: "Body",
				placeholder: "Write the advertisement…",
				rows: 6,
				limit: 50000,
				resetKey: attrs.resetKey,
				emptyText: "No ad body.",
			}),
			m("div.ads-editor-actions", [
				m(Button, {
					label: "Save ad",
					disabled: attrs.busy,
					onclick: attrs.onSave,
				}),
				m(Button, {
					label: "Delete ad",
					variant: "secondary",
					disabled: attrs.busy || !attrs.canDelete,
					onclick: attrs.onDelete,
				}),
			]),
		]),
};

/** AdChannelTable is the campaign's per-channel assignment table. The owning tab
 * resolves each row's joined conversation and live status, so the table and its
 * rows stay presentational. */
interface AdChannelRowData {
	ch: AdChannel;
	target: AdTargetStatus | undefined;
}

interface AdChannelTableAttrs {
	rows: AdChannelRowData[];
	adOptions: ComboboxOption[];
	busy: boolean;
	running: boolean;
	now: number;
	onSetAd: (ch: AdChannel, id: string | null) => void;
	onRemove: (ch: AdChannel) => void;
}

const AdChannelTable: Mithril.Component<AdChannelTableAttrs> = {
	view: ({ attrs }) =>
		m("table.ads-channel-table", [
			m(
				"thead",
				m("tr", [
					m("th", "Channel"),
					m("th", "Ad"),
					m("th", "Next eligible"),
					m("th", { "aria-label": "Remove" }),
				]),
			),
			m(
				"tbody",
				attrs.rows.length === 0
					? m(
							"tr",
							m(
								"td.ads-empty.muted",
								{ colspan: 4 },
								"No channels. Join a channel that allows ads.",
							),
						)
					: attrs.rows.map((row) =>
							m(AdChannelRow, {
								key: channelKey(row.ch.kind, row.ch.id),
								...row,
								adOptions: attrs.adOptions,
								busy: attrs.busy,
								running: attrs.running,
								now: attrs.now,
								onSetAd: attrs.onSetAd,
								onRemove: attrs.onRemove,
							}),
						),
			),
		]),
};

/** AdChannelRow renders one assigned channel: its label, the ad selector (or the
 * chat-only note), the next eligible time, and the remove action. */
interface AdChannelRowAttrs extends AdChannelRowData {
	adOptions: ComboboxOption[];
	busy: boolean;
	running: boolean;
	now: number;
	onSetAd: (ch: AdChannel, id: string | null) => void;
	onRemove: (ch: AdChannel) => void;
}

const AdChannelRow: Mithril.Component<AdChannelRowAttrs> = {
	view: ({ attrs }) => {
		const allows = channelAllowsAds(attrs.target);
		const selected = (attrs.ch.ads ?? [])[0] ?? null;
		const label = attrs.ch.name ?? attrs.ch.id;
		return m("tr", [
			m("td.ads-channel-cell", [
				m("span.ads-channel-name", label),
				m("span.ads-channel-kind", attrs.ch.kind === "room" ? "room" : "channel"),
			]),
			m(
				"td.ads-ad-cell",
				allows
					? m(Combobox, {
							label: "Ad",
							options: attrs.adOptions,
							selected,
							placeholder:
								attrs.adOptions.length === 0 ? "No ads saved" : "Select an ad…",
							clearable: true,
							disabled: attrs.busy || attrs.adOptions.length === 0,
							onchange: (id) => attrs.onSetAd(attrs.ch, id),
						})
					: m("span.ads-no-ads.muted", "Channel does not allow ads."),
			),
			m(
				"td.ads-eligible-cell",
				nextEligibleLabel(attrs.target, attrs.running, attrs.now),
			),
			m(
				"td.ads-remove-cell",
				m(
					"button.chip-remove",
					{
						type: "button",
						"aria-label": `Remove ${label}`,
						title: "Remove",
						disabled: attrs.busy,
						onclick: () => attrs.onRemove(attrs.ch),
					},
					"×",
				),
			),
		]);
	},
};

/** loadCampaign fetches the campaign and merges the core's candidate channels
 * (joined, ad-allowing, not yet in the campaign) into the draft. */
function loadCampaign(state: AdsPostState, session: string): void {
	void fetchAdsCampaign(session).then((view) => {
		state.loading = false;
		if (view === null) {
			state.error = `Could not load the ad campaign for ${session}.`;
			request();
			return;
		}
		state.error = null;
		const base = normalizeCampaign(view.campaign);
		const merged = mergeAvailable(base, view.available ?? []);
		state.campaign = merged;
		state.status = view.status;
		state.selected = merged.ads[0]?.name ?? null;
		state.nameDraft = state.selected ?? "";
		state.bodyDraft = adBody(merged.ads, state.selected);
		state.dirty = merged.channels.length !== base.channels.length;
		state.message = null;
		state.messageError = false;
		state.resetKey++;
		request();
	});
}

/** sameAdName compares a loaded ad name with the editor text, ignoring case and
 * surrounding whitespace (the same normalization saveAd applies). */
function sameAdName(loaded: string, typed: string): boolean {
	return loaded.toLowerCase() === typed.trim().toLowerCase();
}

/** editName updates the name draft, dropping the loaded selection once the typed
 * name no longer identifies it, so Delete and Assign to all do not act on a
 * different ad. */
function editName(state: AdsPostState, value: string): void {
	state.nameDraft = value;
	state.message = null;
	if (state.selected !== null && !sameAdName(state.selected, value)) {
		state.selected = null;
	}
}

function selectAd(state: AdsPostState, name: string): void {
	state.selected = name;
	state.nameDraft = name;
	state.bodyDraft = adBody(state.campaign.ads, name);
	state.message = null;
	state.resetKey++;
}

function saveAdDraft(state: AdsPostState): void {
	const res = saveAd(state.campaign.ads, state.nameDraft, state.bodyDraft);
	if (res.error !== undefined) {
		state.message = res.error;
		state.messageError = true;
		return;
	}
	state.campaign = { ...state.campaign, ads: res.ads };
	state.selected = res.selected;
	state.nameDraft = res.selected;
	state.bodyDraft = adBody(res.ads, res.selected);
	state.dirty = true;
	state.messageError = false;
	state.message = "Ad saved to the draft. Save the campaign to apply.";
	state.resetKey++;
}

function deleteAdDraft(state: AdsPostState): void {
	if (state.selected === null) {
		return;
	}
	const removed = state.selected;
	state.campaign = deleteAd(state.campaign, removed);
	state.selected = null;
	state.nameDraft = "";
	state.bodyDraft = "";
	state.dirty = true;
	state.messageError = false;
	state.message = `Deleted “${removed}” from the draft.`;
	state.resetKey++;
}

function toggleEnabled(state: AdsPostState, value: boolean): void {
	state.campaign = { ...state.campaign, enabled: value };
	state.dirty = true;
	state.message = null;
}

function assignAll(state: AdsPostState): void {
	if (state.selected === null) {
		return;
	}
	state.campaign = assignAdToAll(state.campaign, state.selected);
	state.dirty = true;
	state.message = null;
}

function setAd(state: AdsPostState, ch: AdChannel, id: string | null): void {
	state.campaign = setChannelAd(state.campaign, ch, id);
	state.dirty = true;
	state.message = null;
}

function removeChannelFromDraft(state: AdsPostState, ch: AdChannel): void {
	state.campaign = removeChannel(state.campaign, ch);
	state.dirty = true;
	state.message = null;
}

/** saveCampaign writes the whole draft and adopts the core-normalized result. */
function saveCampaign(state: AdsPostState, session: string): void {
	state.busy = true;
	state.message = null;
	void putAdsCampaign(session, state.campaign).then((res) => {
		state.busy = false;
		if (!res.ok) {
			state.message = res.error ?? "Could not save the campaign.";
			state.messageError = true;
			request();
			return;
		}
		if (res.view !== undefined) {
			state.campaign = normalizeCampaign(res.view.campaign);
			state.status = res.view.status;
			if (state.selected !== null && adBody(state.campaign.ads, state.selected) === "") {
				state.selected = null;
				state.nameDraft = "";
				state.bodyDraft = "";
			}
			state.resetKey++;
		}
		state.dirty = false;
		state.message = "Campaign saved.";
		state.messageError = false;
		request();
	});
}

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
	/** matches memoizes the filtered set against the filter inputs and the ad
	 * buffer, so an unrelated redraw does not re-scan the buffer. */
	matches: Memo<Ad[]>;
	/** options memoizes the channel/character option lists against the buffer. */
	options: Memo<{
		channelOptions: MultiSelectOption[];
		characterOptions: MultiSelectOption[];
	}>;
}

export const AdsSearch: Mithril.Component<AdsSearchAttrs> = {
	oninit: (vnode) => {
		const state = vnode.state as AdsSearchState;
		state.query = "";
		state.filter = "";
		state.channels = [];
		state.characters = [];
		state.matches = memoInit();
		state.options = memoInit();
	},
	view: (vnode) => {
		const state = vnode.state as AdsSearchState;
		const store = useStore();
		const attrs = vnode.attrs;

		// Rebuild the option lists only when the ad set identity changes.
		const { channelOptions, characterOptions } = memo(
			state.options,
			[attrs.ads],
			() => ({
				channelOptions: unique(attrs.ads.map((a) => a.channel)).map(
					(name) => ({ id: name, name }),
				),
				characterOptions: unique(attrs.ads.map((a) => a.character)).map(
					(name) => ({ id: name, name }),
				),
			}),
		);

		const matches = memo(
			state.matches,
			[attrs.ads, state.channels, state.characters, state.filter],
			() => {
				const q = state.filter.trim().toLowerCase();
				return attrs.ads.filter((ad) => {
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
			},
		);

		return m("div.ads-search", [
			m(AdFilters, {
				channelOptions,
				characterOptions,
				channels: state.channels,
				characters: state.characters,
				query: state.query,
				onChannels: (ids) => {
					state.channels = ids;
				},
				onCharacters: (ids) => {
					state.characters = ids;
				},
				onQuery: (value) => {
					state.query = value;
				},
				onFilter: (value) => {
					state.filter = value;
				},
			}),
			m(AdResults, {
				matches,
				total: attrs.ads.length,
				characters: store.characters,
			}),
		]);
	},
};

/** AdFilters is the search tab's filter row: a MultiSelect per channel and poster
 * present in the buffer, plus the debounced fulltext field. */
interface AdFiltersAttrs {
	channelOptions: MultiSelectOption[];
	characterOptions: MultiSelectOption[];
	channels: MultiSelectID[];
	characters: MultiSelectID[];
	query: string;
	onChannels: (ids: MultiSelectID[]) => void;
	onCharacters: (ids: MultiSelectID[]) => void;
	onQuery: (value: string) => void;
	onFilter: (value: string) => void;
}

const AdFilters: Mithril.Component<AdFiltersAttrs> = {
	view: ({ attrs }) => [
		m("div.ads-filters", [
			m(MultiSelect, {
				label: "Channels",
				options: attrs.channelOptions,
				selected: attrs.channels,
				onchange: attrs.onChannels,
			}),
			m(MultiSelect, {
				label: "Characters",
				options: attrs.characterOptions,
				selected: attrs.characters,
				onchange: attrs.onCharacters,
			}),
		]),
		m(FilterInput, {
			name: "ads-filter",
			class: "ads-fulltext",
			placeholder: "Filter ads…",
			value: attrs.query,
			oninput: attrs.onQuery,
			onfilter: attrs.onFilter,
		}),
	],
};

/** AdResults is the search tab's result count and list, or the empty note. */
interface AdResultsAttrs {
	matches: Ad[];
	total: number;
	characters: Store["characters"];
}

const AdResults: Mithril.Component<AdResultsAttrs> = {
	view: ({ attrs }) => [
		m("p.ads-result-count", `${attrs.matches.length} of ${attrs.total} ads`),
		attrs.matches.length === 0
			? m(
					"p.ads-empty.muted",
					attrs.total === 0 ? "No ads yet." : "No ads match.",
				)
			: m(
					"ul.ads-results",
					attrs.matches.map((ad) =>
						adRow(ad, attrs.characters[ad.character]?.gender),
					),
				),
	],
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
// the Search tab renders the fetched set and the Post tab edits the campaign.
// Closing unmounts it, so filter state resets on reopen.

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
					{ id: "post", label: "Post", render: () => m(AdsPosting, { session }) },
				],
			}),
		);
	},
};
