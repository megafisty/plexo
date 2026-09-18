// A tiny trailing debounce. Pure: it only schedules a caller-supplied callback,
// so it has no app dependencies and can live in lib/. The caller owns the
// callback (typically a redraw request), which keeps this usable from any
// feature folder without importing the scheduler.

export interface Debounced {
	/** schedule (re)arms the timer; the callback runs once after ms of quiet. */
	schedule(fn: () => void): void;
	/** cancel drops any pending callback. */
	cancel(): void;
}

/** debounce returns a trailing debounce with a quiet period of `ms`. */
export function debounce(ms: number): Debounced {
	let timer: number | undefined;
	return {
		schedule(fn: () => void): void {
			if (timer !== undefined) {
				window.clearTimeout(timer);
			}
			timer = window.setTimeout(() => {
				timer = undefined;
				fn();
			}, ms);
		},
		cancel(): void {
			if (timer !== undefined) {
				window.clearTimeout(timer);
				timer = undefined;
			}
		},
	};
}