// User-facing messages of the job and agent API errors (codes of internal/api).
import { ApiError } from './api';

const messages: Record<string, string> = {
	app_not_found: 'Unknown app.',
	job_not_found: 'Job not found.',
	agent_not_found: 'Unknown agent.',
	no_update_recipe: 'No update block in the app definition: this app is not updated from LUM.',
	unsupported_update: 'Unsupported update type.',
	proxmox_not_configured:
		'snapshot_before_update and update.resources require the proxmox section of server.yaml: no update or rollback is possible without it.',
	no_update_available: 'No update available: run a check first.',
	job_active: 'A job is already running for this app.',
	rollback_running: 'A rollback is running for this app.',
	job_not_running: 'This job is no longer running.',
	no_snapshot: 'This job has no snapshot: rollback is not possible.',
	job_not_finished: 'The job is not finished.',
	already_rolled_back: 'This job has already been rolled back.',
	snapshot_gone: 'The snapshot no longer exists on Proxmox (deleted by retention or by hand).',
	agent_offline: 'The agent is not connected: nothing was done.',
	script_not_synced: 'The script could not be synced to the agent: nothing was done.',
	proxmox_error: 'Proxmox is unreachable.',
	agent_error: 'The agent did not answer.',
	agent_revoked: 'The agent is revoked.',
	agent_not_enrolled: 'The agent never enrolled.',
	agent_not_revoked: 'Only a revoked agent can be deleted.'
};

/** User-facing message for an API error code, undefined if the code is not known. */
export function codeMessage(code: string | undefined): string | undefined {
	return code ? messages[code] : undefined;
}

/** User-facing message of a failed action, with the server detail when useful. */
export function actionError(err: unknown): string {
	if (err instanceof ApiError) {
		const known = codeMessage(err.code);
		if (!known) return err.message;
		// Detail kept for errors coming from another system (agent, Proxmox).
		return ['proxmox_error', 'agent_error', 'script_not_synced'].includes(err.code) ? `${known} (${err.message})` : known;
	}
	return err instanceof Error ? err.message : String(err);
}
