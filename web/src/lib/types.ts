// Types of the REST API /api/v1 (internal/api/types.go). Keep both in sync.

export type Status = 'up_to_date' | 'update_available' | 'unknown' | 'error';

export interface User {
	subject?: string;
	username: string;
	name?: string;
	email?: string;
	method: 'none' | 'session' | 'bearer';
}

export interface Me {
	user: User;
	auth_mode: 'oidc' | 'none';
}

export interface AppSummary {
	id: string;
	name: string;
	lxc_id: number;
	agent?: string;
	status: Status;
	current_version: string;
	latest_version: string;
	last_check_at: string | null;
	release_url?: string;
	error?: string;
	repo: string;
	current_type: 'api' | 'agent';
	update_type?: 'api' | 'script';
	active_job: { id: string; status: JobStatus } | null;
}

export interface AppsResponse {
	apps: AppSummary[];
	check_running: boolean;
}

export interface Check {
	id: number;
	app_id: string;
	checked_at: string;
	current_version: string;
	latest_version: string;
	status: Status;
	error?: string;
	release_url?: string;
}

export interface Recipe {
	upstream: { type: string; repo: string; include_prerelease: boolean; tag_regex: string };
	current: {
		type: 'api' | 'agent';
		url?: string;
		auth_header?: string;
		json_path?: string;
		command?: string[];
		output_regex?: string;
	};
	update?: {
		type: 'api' | 'script';
		method?: string;
		url?: string;
		script?: string;
		timeout: string;
		/** Minimum CPU cores and memory (MiB) of the LXC during the update. */
		resources?: { cores?: number; memory?: number };
	};
	healthcheck?: { url: string; expect_status: number; timeout: string };
	snapshot_before_update: boolean;
	rollback_on_failure: boolean;
	retention: number;
}

export interface AppDetail extends AppSummary {
	recipe: Recipe;
	release_notes: string;
	checks: Check[];
}

export interface ConfigProblem {
	path: string;
	message: string;
}

export interface ConfigError {
	kind: 'validation' | 'missing_env' | 'read';
	message: string;
	problems?: ConfigProblem[];
	missing_vars?: string[];
}

export interface SystemStatus {
	version: string;
	auth_mode: 'oidc' | 'none';
	config: { valid: boolean; loaded_at: string; last_reload_error?: ConfigError };
	github: { known: boolean; limit: number; remaining: number; reset: string; token_configured: boolean };
	scheduler: {
		check_cron: string;
		jitter: string;
		concurrency: number;
		next_check: string;
		last_check: string | null;
		check_running: boolean;
	};
	apps: Partial<Record<Status, number>>;
	recent_errors: { app_id: string; name: string; error: string; at: string }[];
	proxmox: { configured: boolean; version?: string; error?: string; missing_lxc?: number[] };
}

// Deliverable 2: jobs, agents, audit.

export type JobStatus =
	| 'pending'
	| 'snapshotting'
	| 'running'
	| 'healthchecking'
	| 'success'
	| 'failed'
	| 'rolled_back'
	| 'cancelled';

export interface Job {
	id: string;
	app_id: string;
	app_name: string;
	lxc_id?: number;
	type: 'api' | 'script';
	status: JobStatus;
	current_version: string;
	target_version: string;
	snapshot_name?: string;
	created_by: string;
	created_at: string;
	started_at?: string;
	finished_at?: string;
	error?: string;
}

export interface JobLog {
	seq: number;
	ts: string;
	stream: 'stdout' | 'stderr' | 'system';
	line: string;
}

export interface JobDetail {
	job: Job;
	running: boolean;
	rollback: { available: boolean; reason?: string; message?: string };
}

export type AgentStatus = 'online' | 'offline' | 'revoked' | 'not_enrolled';
export type ScriptState = 'in_sync' | 'stale' | 'missing';

export interface Agent {
	id: string;
	status: AgentStatus;
	agent_version?: string;
	hostname?: string;
	last_seen_at: string | null;
	enrolled_at: string | null;
	binary: 'up_to_date' | 'outdated' | 'unknown';
	apps: string[];
	scripts: { name: string; state: ScriptState; server_sha256: string; agent_sha256?: string }[];
}

export interface AuditEntry {
	id: number;
	ts: string;
	user: string;
	action: string;
	target?: string;
	details?: string;
}
