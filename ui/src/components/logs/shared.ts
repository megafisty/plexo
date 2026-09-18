// shared.ts — the two values more than one log module needs: the overview's
// fixed day bucket width and a byte formatter. Feature-internal; not a general
// utils dumping ground.

/** DAY_MS is the overview's fixed bucket width (one local day). */
export const DAY_MS = 86_400_000;

/** formatBytes renders a byte count in human units. */
export function formatBytes(n: number): string {
	if (n < 1024) return `${n} B`;
	if (n < 1024 * 1024) return `${Math.round(n / 1024)} KB`;
	return `${(n / (1024 * 1024)).toFixed(1)} MB`;
}