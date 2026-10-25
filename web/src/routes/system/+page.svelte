<script lang="ts">
	import { api, configErrorOf } from '#lib/api.ts';
	import { auditActionLabels, formatDate, formatRelative, statusLabels, statusOrder } from '#lib/format.ts';
	import type { AuditEntry, ConfigError, SystemStatus } from '#lib/types.ts';

	const AUDIT_SHOWN = 50;

	let status = $state<SystemStatus | null>(null);
	let loadError = $state('');
	let reloading = $state(false);
	let reloadMessage = $state('');
	let reloadError = $state<ConfigError | null>(null);
	let audit = $state<AuditEntry[]>([]);
	let auditError = $state('');

	const configError = $derived(reloadError ?? status?.config.last_reload_error ?? null);

	async function load() {
		try {
			status = await api.systemStatus();
			loadError = '';
		} catch (e) {
			loadError = e instanceof Error ? e.message : String(e);
		}
		try {
			audit = (await api.audit(AUDIT_SHOWN)).entries;
			auditError = '';
		} catch (e) {
			auditError = e instanceof Error ? e.message : String(e);
		}
	}

	async function reload() {
		reloading = true;
		reloadMessage = '';
		reloadError = null;
		try {
			const res = await api.reloadConfig();
			reloadMessage = `Configuration reloaded (${res.apps} apps).`;
		} catch (e) {
			reloadError = configErrorOf(e) ?? { kind: 'read', message: e instanceof Error ? e.message : String(e) };
		} finally {
			reloading = false;
			await load();
		}
	}

	$effect(() => {
		load();
	});
</script>

<svelte:head>
	<title>System status — LUM</title>
</svelte:head>

<h1>System status</h1>

{#if loadError}
	<div class="alert">Could not load: {loadError}</div>
{:else if status}
	<div class="grid">
		<section class="card">
			<div class="title-row">
				<h2>Configuration</h2>
				<button class="btn" onclick={reload} disabled={reloading}>{reloading ? 'Reloading…' : 'Reload'}</button>
			</div>
			{#if reloadMessage}
				<div class="alert ok">{reloadMessage}</div>
			{/if}
			<dl class="props">
				<dt>Status</dt>
				<dd>
					{#if status.config.valid && !reloadError}
						<span class="ok">Valid</span>
					{:else}
						<span class="ko">Last reload failed</span> — the previous configuration is still active
					{/if}
				</dd>
				<dt>Loaded</dt>
				<dd title={formatDate(status.config.loaded_at)}>{formatRelative(status.config.loaded_at)}</dd>
				<dt>Authentication</dt>
				<dd>{status.auth_mode === 'oidc' ? 'OIDC (Keycloak)' : 'disabled (trusted network)'}</dd>
				<dt>LUM version</dt>
				<dd class="mono">{status.version}</dd>
			</dl>
			{#if configError}
				<div class="problems">
					<p class="ko">{configError.message}</p>
					{#if configError.problems}
						<ul>
							{#each configError.problems as p (p.path + p.message)}
								<li><span class="mono">{p.path}</span>: {p.message}</li>
							{/each}
						</ul>
					{/if}
					{#if configError.missing_vars}
						<p>Missing variables: <span class="mono">{configError.missing_vars.join(', ')}</span></p>
					{/if}
				</div>
			{/if}
		</section>

		<section class="card">
			<h2>Checks</h2>
			<dl class="props">
				<dt>Schedule</dt>
				<dd><span class="mono">{status.scheduler.check_cron}</span> (jitter {status.scheduler.jitter})</dd>
				<dt>Next</dt>
				<dd title={formatDate(status.scheduler.next_check)}>{formatRelative(status.scheduler.next_check)}</dd>
				<dt>Last</dt>
				<dd title={formatDate(status.scheduler.last_check)}>
					{formatRelative(status.scheduler.last_check)}{status.scheduler.check_running ? ' · running…' : ''}
				</dd>
				<dt>Concurrency</dt>
				<dd>{status.scheduler.concurrency} at a time</dd>
				<dt>Apps</dt>
				<dd>
					{#each statusOrder as s (s)}
						{#if status.apps[s]}<span class="app-count">{status.apps[s]} {statusLabels[s].toLowerCase()}</span>{/if}
					{/each}
				</dd>
			</dl>
		</section>

		<section class="card">
			<h2>GitHub quota</h2>
			{#if status.github.known}
				<div class="quota">
					<div class="bar"><div style="width: {(100 * status.github.remaining) / Math.max(status.github.limit, 1)}%"></div></div>
					<span><strong>{status.github.remaining}</strong> / {status.github.limit} requests remaining</span>
				</div>
				<p class="muted">Resets {formatRelative(status.github.reset)}.</p>
			{:else}
				<p class="muted">Unknown until a first request is made.</p>
			{/if}
			{#if !status.github.token_configured}
				<p class="muted">Without <span class="mono">github.token</span>, the limit is 60 requests per hour.</p>
			{/if}
		</section>

		<section class="card">
			<h2>Proxmox</h2>
			{#if !status.proxmox.configured}
				<p class="muted">
					Not configured (<span class="mono">proxmox</span> section of <span class="mono">server.yaml</span>): updates
					with a snapshot will be refused.
				</p>
			{:else if status.proxmox.error}
				<p class="ko">API unreachable</p>
				<div class="error-text">{status.proxmox.error}</div>
			{:else}
				<p class="ok">API reachable · Proxmox VE {status.proxmox.version}</p>
				{#if status.proxmox.missing_lxc?.length}
					<p class="error-text">
						LXCs not visible with this token (missing, or no <span class="mono">VM.Audit</span> permission):
						{status.proxmox.missing_lxc.join(', ')}
					</p>
				{/if}
			{/if}
		</section>
	</div>

	<section class="card">
		<h2>Current errors</h2>
		{#if status.recent_errors.length === 0}
			<p class="muted">No app in error.</p>
		{:else}
			<ul class="errors">
				{#each status.recent_errors as e (e.app_id)}
					<li>
						<a href="/apps/{e.app_id}">{e.name}</a>
						<span class="muted">· {formatRelative(e.at)}</span>
						<div class="error-text">{e.error}</div>
					</li>
				{/each}
			</ul>
		{/if}
	</section>

	<section class="card">
		<h2>Audit log</h2>
		<p class="muted intro">The last {AUDIT_SHOWN} write actions, with the identity of their author (refusals included).</p>
		{#if auditError}
			<div class="alert">Audit log unavailable: {auditError}</div>
		{:else if audit.length === 0}
			<p class="muted">No action yet.</p>
		{:else}
			<div class="scroll">
				<table class="data audit">
					<thead>
						<tr><th>Date</th><th>User</th><th>Action</th><th>Target</th><th>Details</th></tr>
					</thead>
					<tbody>
						{#each audit as e (e.id)}
							<tr>
								<td title={formatDate(e.ts)}>{formatRelative(e.ts)}</td>
								<td>{e.user}</td>
								<td>{auditActionLabels[e.action] ?? e.action}</td>
								<td class="mono">
									{#if e.target && e.action.startsWith('job.')}
										<a href="/jobs/{e.target}">{e.target.slice(0, 8)}</a>
									{:else if e.target && e.action.startsWith('app.')}
										<a href="/apps/{e.target}">{e.target}</a>
									{:else}
										{e.target ?? ''}
									{/if}
								</td>
								<td class:ko={e.details?.startsWith('refused') || e.details === 'failed'}>
									{#if e.details?.startsWith('job ')}
										<a href="/jobs/{e.details.slice(4)}">job {e.details.slice(4, 12)}</a>
									{:else}
										{e.details ?? ''}
									{/if}
								</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</section>
{/if}

<style>
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 20rem), 1fr));
		gap: 0 1rem;
	}
	.title-row {
		display: flex;
		justify-content: space-between;
		align-items: center;
		margin-bottom: 0.75rem;
	}
	.title-row h2 {
		margin: 0;
	}
	.ok {
		color: var(--ok);
	}
	.ko {
		color: var(--ko);
	}
	.problems {
		margin-top: 0.75rem;
		font-size: 0.875rem;
	}
	.problems ul {
		padding-left: 1.25rem;
		margin: 0.25rem 0;
	}
	.app-count {
		margin-right: 0.75rem;
	}
	.quota {
		display: grid;
		gap: 0.5rem;
	}
	.bar {
		height: 0.5rem;
		background: var(--neutral-bg);
		border-radius: 999px;
		overflow: hidden;
	}
	.bar div {
		height: 100%;
		background: var(--accent);
	}
	.intro {
		margin-top: -0.25rem;
		font-size: 0.875rem;
	}
	.scroll {
		overflow-x: auto;
	}
	.audit td {
		white-space: nowrap;
	}
	.errors {
		list-style: none;
		padding: 0;
		margin: 0;
		display: grid;
		gap: 0.75rem;
	}
</style>
