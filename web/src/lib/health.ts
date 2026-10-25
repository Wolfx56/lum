export interface Health {
	status: string;
	version: string;
}

export type ServerState = 'unknown' | 'online' | 'offline';

export interface HealthResult {
	state: ServerState;
	version: string | null;
}

/** Calls the Go server's /healthz and maps the answer to a server state. Never throws. */
export async function fetchHealth(fetchFn: typeof fetch = fetch): Promise<HealthResult> {
	try {
		const res = await fetchFn('/healthz');
		if (!res.ok) return { state: 'offline', version: null };
		const body = (await res.json()) as Health;
		return { state: body.status === 'ok' ? 'online' : 'offline', version: body.version };
	} catch {
		return { state: 'offline', version: null };
	}
}
