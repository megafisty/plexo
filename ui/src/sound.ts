// Attention sound for elevated messages (an incoming DM or a highlight in a
// channel). One lazily-created <audio> element, resolved relative to the app
// root so the core serves it from the embedded UI assets.
//
// Playback is best-effort: browsers block programmatic audio until a user
// gesture, so main.ts primes the element on the first pointer/key event.
// playAttention never throws, and a short cooldown collapses a burst of
// highlights into a single chime.
const SRC = "./sound/attention.mp3";
const COOLDOWN_MS = 1000;

let el: HTMLAudioElement | null = null;
let last = 0;

function audio(): HTMLAudioElement | null {
	// A decode or network error poisons an HTMLAudioElement permanently: every
	// later play() on it stays silent. Rebuild it so one bad chime cannot
	// silence the rest of the session.
	if (el !== null && el.error === null) {
		return el;
	}
	el = null;
	try {
		el = new Audio(SRC);
		el.preload = "auto";
	} catch {
		el = null;
	}
	return el;
}

/** primeAudio unlocks playback on a user gesture: start a muted play (allowed
 * even before interaction) and immediately reset it. Safe to call repeatedly. */
export function primeAudio(): void {
	const a = audio();
	if (a === null) {
		return;
	}
	try {
		a.muted = true;
		const p = a.play();
		if (p !== undefined) {
			void p
				.then(() => {
					a.pause();
					a.currentTime = 0;
					a.muted = false;
				})
				.catch(() => {
					a.muted = false;
				});
		} else {
			a.pause();
			a.currentTime = 0;
			a.muted = false;
		}
	} catch {
		a.muted = false;
	}
}

/** playAttention plays the attention sound, at most once per cooldown. Never
 * throws; a rejected play (autoplay still blocked) is ignored. */
export function playAttention(): void {
	const now = Date.now();
	if (now - last < COOLDOWN_MS) {
		return;
	}
	last = now;
	const a = audio();
	if (a === null) {
		return;
	}
	try {
		a.currentTime = 0;
		const p = a.play();
		if (p !== undefined) {
			void p.catch(() => {
				/* not unlocked yet: the next gesture will prime it */
			});
		}
	} catch {
		/* ignore */
	}
}