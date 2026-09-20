import m from "../../mithril.js";
import type * as Mithril from "mithril";
import { fetchMapping, postSearch } from "../../api.js";
import { useStore, useView } from "../../context.js";
import { memo, memoInit, request, type Memo } from "../../render.js";
import { recallSearch } from "../../store/search.js";
import { closeModal, type View } from "../../store/state.js";
import type { MemberInfo, SearchField, SearchMapping, SearchQuery } from "../../transport/protocol.js";
import { MultiSelect, type MultiSelectID } from "../primitives/select.js";
import { Dialog } from "../primitives/dialog.js";
import { Button, FormError } from "../primitives/form.js";
import { FeaturedCharacter } from "../presence/character.js";
import { RosterCharacter } from "../presence/character.js";
// SearchDialog: the character-search modal (FKS). Two columns: a query builder
// on the left (one MultiSelect per mapping field) and the result list on the
// right. The core caches the latest result set per session and announces only a
// `search` notice, so the dialog pulls the rows over HTTP (on open, on a notice
// while open, and via the recall button) and reads them from the store. The
// builder's selections live in View keyed by session, so closing and reopening
// the dialog restores them. It is bound to the active session, because FKS runs
// over that character's socket.
//
// Results with the "looking" status are surfaced first as FeaturedCharacter
// rows (avatar + status message); everything else follows as RosterCharacter
// rows. Both groups are alphabetical. Results are self-contained: the core
// enriches each with the presence it already holds, so the dialog renders them
// statically and never reads (or writes) the client character registry. Each
// result is a row, so the delegated click opens the character context menu like
// the channel roster does.

/** EMPTY is a shared empty selection so an unset field does not allocate. */
const EMPTY: MultiSelectID[] = [];

/** NO_RESULTS is a shared empty result set so memoization has a stable key. */
const NO_RESULTS: MemberInfo[] = [];

interface SearchDialogState {
	mapping: SearchMapping | null;
	loading: boolean;
	error: string | null;
	busy: boolean;
	/** sorted memoizes the status split against the result-set identity. */
	sorted: Memo<{ looking: MemberInfo[]; rest: MemberInfo[] }>;
}

export const SearchDialog: Mithril.Component = {
	oninit: (vnode) => {
		const state = vnode.state as SearchDialogState;
		state.mapping = null;
		state.loading = true;
		state.error = null;
		state.busy = false;
		state.sorted = memoInit();
		// The mapping is core-wide and cached after the first successful load; a
		// failure leaves it null so reopening retries (503 until the load lands).
		void fetchMapping().then((mapping) => {
			if (mapping === null) {
				state.loading = false;
				state.error = "Search mapping isn't available yet.";
			} else {
				state.mapping = mapping;
				state.loading = false;
			}
			request();
		});
		// Seed the results from the core's session cache, so a client that did not
		// witness the last search (a new tab, or one that reconnected) still sees
		// the latest set.
		const view = useView();
		if (view.activeSession !== null) {
			void recallSearch(useStore(), view.activeSession).then(request);
		}
	},
	view: (vnode) => {
		const store = useStore();
		const view = useView();
		const state = vnode.state as SearchDialogState;

		const session = view.activeSession;
		const sess = session === null ? undefined : store.sessions[session];
		if (session === null || sess === undefined) {
			return null; // the bound session was closed while the dialog was open
		}
		const live = sess.state === "live";

		const fields: SearchField[] =
			state.mapping === null ? [] : Object.values(state.mapping);
		const selection = view.searchSelection[session] ?? {};
		const results = store.search[session] ?? NO_RESULTS;
		// Split by current status: "looking" characters are featured first as
		// avatar rows, everyone else follows as plain roster rows. Both groups
		// are alphabetical so the ordering is predictable. The split is memoized
		// against the result-set identity (the core replaces it wholesale on a
		// pull), so an unrelated redraw does not re-sort.
		const { looking, rest } = memo(state.sorted, [results], () => ({
			looking: results
				.filter(isLooking)
				.sort((a, b) => a.name.localeCompare(b.name)),
			rest: results
				.filter((r) => !isLooking(r))
				.sort((a, b) => a.name.localeCompare(b.name)),
		}));

		const close = (): void => {
			closeModal(view);
		};

		return m(
			Dialog,
			{
				title: `Search characters as ${session}`,
				onClose: close,
				class: "search-dialog",
			},
			[
				m("div.search-columns", [
					m(SearchFilters, {
						fields,
						loading: state.loading,
						error: state.error,
						mappingLoaded: state.mapping !== null,
						selection,
						onChange: (field, ids) => setSelection(view, session, field, ids),
					}),
					m(SearchResults, {
						total: results.length,
						looking,
						rest,
					}),
				]),
				m(FormError, { message: state.mapping !== null ? state.error : null }),
				m("div.dialog-actions", [
					m(Button, {
						label: "Clear all",
						variant: "secondary",
						onclick: () => {
							view.searchSelection[session] = {};
						},
					}),
					m(Button, {
						label: "Recall last search",
						variant: "secondary",
						title: "Reload the latest results from the core's cache",
						onclick: () => {
							void recallSearch(store, session).then(request);
						},
					}),
					m("span.dialog-spacer"),
					m(Button, {
						label: "Search",
						busy: state.busy,
						busyLabel: "Searching…",
						disabled: state.mapping === null || !live,
						title: live ? undefined : "The session is not connected",
						onclick: () => {
							submit(state, view, session);
						},
					}),
				]),
			],
		);
	},
};

/** SearchFilters is the query builder's left column: one MultiSelect per mapping
 * field. It renders the loading/error state until the core-wide mapping lands. */
interface SearchFiltersAttrs {
	/** fields is empty until the mapping loads. */
	fields: SearchField[];
	loading: boolean;
	error: string | null;
	/** mappingLoaded distinguishes "not loaded yet" from an empty mapping. */
	mappingLoaded: boolean;
	selection: Record<string, MultiSelectID[]>;
	onChange: (field: string, ids: MultiSelectID[]) => void;
}

const SearchFilters: Mithril.Component<SearchFiltersAttrs> = {
	view: ({ attrs }) =>
		m("section.search-column.search-column-filters", [
			m("h3.search-column-title", "Filters"),
			attrs.loading
				? m("p.muted", "Loading filters…")
				: !attrs.mappingLoaded
					? m("p.muted", attrs.error ?? "Search isn't available.")
					: m(
							"div.search-fields",
							attrs.fields.map((field) =>
								m(MultiSelect, {
									key: field.field,
									label: field.name,
									options: field.entries,
									selected: attrs.selection[field.field] ?? EMPTY,
									onchange: (ids: MultiSelectID[]) =>
										attrs.onChange(field.field, ids),
								}),
							),
						),
		]),
};

/** SearchResults is the right column: the "looking" results as featured rows,
 * then everyone else as roster rows. */
interface SearchResultsAttrs {
	total: number;
	looking: MemberInfo[];
	rest: MemberInfo[];
}

const SearchResults: Mithril.Component<SearchResultsAttrs> = {
	view: ({ attrs }) =>
		m("section.search-column.search-column-results", [
			m("h3.search-column-title", `Results (${attrs.total})`),
			attrs.total === 0
				? m("p.search-empty.muted", "No results yet.")
				: m("div.search-results", [
						attrs.looking.length > 0
							? m(
									"ul.search-result-group",
									attrs.looking.map((character) =>
										m(
											"li",
											{ key: character.name },
											m(FeaturedCharacter, { character, row: true }),
										),
									),
								)
							: null,
						attrs.rest.length > 0
							? m(
									"ul.search-result-group",
									attrs.rest.map((character) =>
										m(
											"li",
											{ key: character.name },
											m(RosterCharacter, { character, row: true }),
										),
									),
								)
							: null,
					]),
		]),
};

/** setSelection records one field's selection for a session. The array is
 * copied so the caller's value is never aliased into View. */
function setSelection(
	view: View,
	session: string,
	field: string,
	ids: MultiSelectID[],
): void {
	const per = view.searchSelection[session] ?? {};
	per[field] = ids.length > 0 ? [...ids] : [];
	view.searchSelection[session] = per;
}

/** submit builds the FKS payload from the stored selection and posts it. The
 * result set arrives later as a `search` notice that triggers a pull, not in
 * this response. */
function submit(
	state: SearchDialogState,
	view: View,
	session: string,
): void {
	if (state.busy || state.mapping === null) {
		return;
	}
	state.busy = true;
	state.error = null;
	void postSearch(session, buildQuery(view.searchSelection[session] ?? {})).then(
		(res) => {
			state.busy = false;
			if (!res.ok) {
				state.error = res.error ?? "Search failed.";
			}
			request();
		},
	);
}

/** buildQuery maps the per-field selection to the FKS payload. Empty filters
 * are omitted and `kinks` is always present, as the protocol requires. The
 * mapping keys are exactly the payload fields, so this stays generic rather than
 * enumerating the six known filters. */
function buildQuery(
	selection: Record<string, Array<string | number>>,
): SearchQuery {
	const out: Record<string, Array<string | number>> = {};
	for (const [field, ids] of Object.entries(selection)) {
		if (ids.length > 0) {
			out[field] = ids;
		}
	}
	if (out.kinks === undefined) {
		out.kinks = [];
	}
	return out as unknown as SearchQuery;
}

/** isLooking reports whether a result is currently online with the "looking"
 * status, which features it at the top of the results. */
function isLooking(character: MemberInfo): boolean {
	return character.online && (character.status ?? "").toLowerCase() === "looking";
}