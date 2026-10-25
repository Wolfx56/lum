import { fetchHealth, type ServerState } from './health';

const POLL_MS = 30_000;

/** Shared, reactive server health (polled from /healthz). */
export const health = $state<{ state: ServerState; version: string | null }>({
	state: 'unknown',
	version: null
});

export async function refreshHealth(): Promise<void> {
	Object.assign(health, await fetchHealth());
}

/** Starts polling; returns the cleanup function (use in an $effect). */
export function pollHealth(): () => void {
	refreshHealth();
	const timer = setInterval(refreshHealth, POLL_MS);
	return () => clearInterval(timer);
}
