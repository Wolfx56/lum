import type { AgentStatus, JobStatus, ScriptState, Status } from './types';

export const statusLabels: Record<Status, string> = {
	up_to_date: 'Up to date',
	update_available: 'Update available',
	unknown: 'Unknown',
	error: 'Error'
};

/** Dashboard order: what needs attention first. */
export const statusOrder: Status[] = ['update_available', 'error', 'unknown', 'up_to_date'];

const dateTime = new Intl.DateTimeFormat('en', { dateStyle: 'short', timeStyle: 'short', hourCycle: 'h23' });
const relative = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });

/** "10/18/26, 07:03", or "—" for a missing date. */
export function formatDate(iso: string | null | undefined): string {
	if (!iso) return '—';
	const d = new Date(iso);
	return Number.isNaN(d.getTime()) || d.getFullYear() < 2000 ? '—' : dateTime.format(d);
}

/** "5 minutes ago", "in 2 hours", or "never". */
export function formatRelative(iso: string | null | undefined, now: Date = new Date()): string {
	if (!iso) return 'never';
	const d = new Date(iso);
	if (Number.isNaN(d.getTime()) || d.getFullYear() < 2000) return 'never';
	const seconds = Math.round((d.getTime() - now.getTime()) / 1000);
	const abs = Math.abs(seconds);
	if (abs < 45) return 'just now';
	if (abs < 3600) return relative.format(Math.round(seconds / 60), 'minute');
	if (abs < 86400) return relative.format(Math.round(seconds / 3600), 'hour');
	return relative.format(Math.round(seconds / 86400), 'day');
}

/** Version or a dash. */
export function version(v: string | undefined): string {
	return v ? v : '—';
}

export const jobStatusLabels: Record<JobStatus, string> = {
	pending: 'Pending',
	snapshotting: 'Snapshot',
	running: 'Updating',
	healthchecking: 'Healthcheck',
	success: 'Succeeded',
	failed: 'Failed',
	rolled_back: 'Rolled back',
	cancelled: 'Cancelled'
};

/** States of a job that is not finished (store.ActiveJobStates). */
export const activeJobStatuses: JobStatus[] = ['pending', 'snapshotting', 'running', 'healthchecking'];

export const isActive = (s: JobStatus): boolean => activeJobStatuses.includes(s);

export const agentStatusLabels: Record<AgentStatus, string> = {
	online: 'Online',
	offline: 'Offline',
	revoked: 'Revoked',
	not_enrolled: 'Not enrolled'
};

export const scriptStateLabels: Record<ScriptState, string> = {
	in_sync: 'In sync',
	stale: 'Stale',
	missing: 'Missing'
};

/** "45s", "3m 05s", "1h 02m"; "—" without start. end defaults to now. */
export function formatDuration(start: string | null | undefined, end?: string | null, now: Date = new Date()): string {
	if (!start) return '—';
	const from = new Date(start).getTime();
	const to = end ? new Date(end).getTime() : now.getTime();
	if (Number.isNaN(from) || Number.isNaN(to) || new Date(start).getFullYear() < 2000) return '—';
	const s = Math.max(0, Math.round((to - from) / 1000));
	if (s < 60) return `${s}s`;
	const pad = (n: number) => String(n).padStart(2, '0');
	if (s < 3600) return `${Math.floor(s / 60)}m ${pad(s % 60)}s`;
	return `${Math.floor(s / 3600)}h ${pad(Math.floor((s % 3600) / 60))}m`;
}

/** "10:42:07": time of a log line. */
const clock = new Intl.DateTimeFormat('en', { hour: '2-digit', minute: '2-digit', second: '2-digit', hourCycle: 'h23' });
export function formatClock(iso: string): string {
	const d = new Date(iso);
	return Number.isNaN(d.getTime()) ? '' : clock.format(d);
}

export const auditActionLabels: Record<string, string> = {
	'app.check': 'Check',
	'checks.run': 'Check all',
	'config.reload': 'Config reload',
	'app.update': 'Update',
	'job.cancel': 'Job cancellation',
	'job.rollback': 'Rollback',
	'agent.sync_scripts': 'Script sync',
	'agent.revoke': 'Agent revocation',
	'agent.delete': 'Agent deletion'
};
