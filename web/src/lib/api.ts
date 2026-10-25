// Typed client of the REST API. Authentication is the HttpOnly session cookie
// (BFF): the browser sends it automatically, no token is handled here.
import type { Agent, AppDetail, AppsResponse, AuditEntry, Check, ConfigError, Job, JobDetail, Me, SystemStatus } from './types';

/** Uniform API error {"error": {"code", "message", "details"}}. */
export class ApiError extends Error {
	constructor(
		readonly status: number,
		readonly code: string,
		message: string,
		readonly details?: unknown
	) {
		super(message);
	}
}

/** Where the browser goes when the session is missing or expired. */
export function loginURL(current: string): string {
	return '/auth/login?redirect=' + encodeURIComponent(current);
}

type Fetch = typeof fetch;

export async function request<T>(method: string, path: string, fetchFn: Fetch = fetch): Promise<T> {
	let res: Response;
	try {
		res = await fetchFn('/api/v1' + path, { method, headers: { Accept: 'application/json' } });
	} catch {
		throw new ApiError(0, 'network', 'Server unreachable.');
	}
	if (res.status === 401 && typeof window !== 'undefined') {
		window.location.href = loginURL(window.location.pathname + window.location.search);
		throw new ApiError(401, 'unauthenticated', 'Session expired, redirecting to the login page…');
	}
	const body = await res.json().catch(() => null);
	if (!res.ok) {
		const e = body?.error;
		throw new ApiError(res.status, e?.code ?? 'http_' + res.status, e?.message ?? `HTTP error ${res.status}`, e?.details);
	}
	return body as T;
}

export const api = {
	me: () => request<Me>('GET', '/me'),
	apps: () => request<AppsResponse>('GET', '/apps'),
	app: (id: string) => request<AppDetail>('GET', '/apps/' + encodeURIComponent(id)),
	checkApp: (id: string) => request<{ check: Check }>('POST', '/apps/' + encodeURIComponent(id) + '/check'),
	checkAll: () => request<{ checks: Check[] }>('POST', '/checks'),
	systemStatus: () => request<SystemStatus>('GET', '/system/status'),
	reloadConfig: () => request<{ loaded_at: string; apps: number }>('POST', '/config/reload'),

	startUpdate: (appID: string) => request<{ job: Job }>('POST', '/apps/' + encodeURIComponent(appID) + '/update'),
	jobs: (filter: { app?: string; status?: string; limit?: number } = {}) =>
		request<{ jobs: Job[] }>('GET', '/jobs' + query(filter)),
	job: (id: string) => request<JobDetail>('GET', '/jobs/' + encodeURIComponent(id)),
	cancelJob: (id: string) => request<{ job: Job }>('POST', '/jobs/' + encodeURIComponent(id) + '/cancel'),
	rollbackJob: (id: string) => request<{ job: Job }>('POST', '/jobs/' + encodeURIComponent(id) + '/rollback'),
	agents: () => request<{ agents: Agent[] }>('GET', '/agents'),
	syncAgentScripts: (id: string) => request<{ agent: Agent }>('POST', '/agents/' + encodeURIComponent(id) + '/sync-scripts'),
	revokeAgent: (id: string) => request<{ agent: Agent }>('POST', '/agents/' + encodeURIComponent(id) + '/revoke'),
	deleteAgent: (id: string) => request<{ deleted: string }>('DELETE', '/agents/' + encodeURIComponent(id)),
	audit: (limit = 100) => request<{ entries: AuditEntry[] }>('GET', '/audit?limit=' + limit)
};

/** "?a=1&b=2" from the defined values, or "". */
export function query(params: Record<string, string | number | undefined>): string {
	const q = new URLSearchParams();
	for (const [k, v] of Object.entries(params)) {
		if (v !== undefined && v !== '') q.set(k, String(v));
	}
	const s = q.toString();
	return s ? '?' + s : '';
}

/** Logs out (server session + Keycloak session) and leaves the app. */
export async function logout(): Promise<void> {
	const res = await fetch('/auth/logout', { method: 'POST' });
	const body = await res.json().catch(() => ({ redirect: '/' }));
	window.location.href = body.redirect ?? '/';
}

/** Extracts the configuration error of a failed reload (422 invalid_config). */
export function configErrorOf(err: unknown): ConfigError | undefined {
	return err instanceof ApiError && err.code === 'invalid_config' ? (err.details as ConfigError) : undefined;
}
