// list.ts — bounding a filtered result set. Pure and dependency-free, shared by
// the pickers that cap rendered rows (the combobox popups, the join catalog) so
// a broad query cannot build unbounded DOM.

/** Bounded is a filtered list trimmed to a visible prefix plus the count left
 * over. `visible` is the input array itself when nothing is hidden. */
export interface Bounded<T> {
	readonly visible: readonly T[];
	readonly hidden: number;
}

/** boundMatches trims `matches` to at most `maxVisible` entries (0 or less means
 * no cap) and reports how many were left unrendered. Callers materialize only
 * `visible`; `hidden` drives a "keep typing" note. */
export function boundMatches<T>(
	matches: readonly T[],
	maxVisible: number,
): Bounded<T> {
	if (maxVisible > 0 && matches.length > maxVisible) {
		return {
			visible: matches.slice(0, maxVisible),
			hidden: matches.length - maxVisible,
		};
	}
	return { visible: matches, hidden: 0 };
}
