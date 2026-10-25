<script lang="ts">
	import { api } from '#lib/api.ts';
	import JobStatusBadge from '#lib/components/JobStatusBadge.svelte';
	import { formatDate, formatDuration, formatRelative, isActive, jobStatusLabels, version } from '#lib/format.ts';
	import type { AppSummary, Job, JobStatus } from '#lib/types.ts';
	import { page } from '$app/state';

	const ACTIVE_REFRESH_MS = 5_000;
	const IDLE_REFRESH_MS = 30_000;

	let jobs = $state<Job[]>([]);
	let apps = $state<AppSummary[]>([]);
	let loaded = $state(false);
	let loadError = $state('');
	// Initial filters from the URL (/jobs?app=sonarr&status=active).
	let appFilter = $state(page.url.searchParams.get('app') ?? '');
	let statusFilter = $state(page.url.searchParams.get('status') ?? '');

	const statusOptions: { value: string; label: string }[] = [
		{ value: '', label: 'All statuses' },
		{ value: 'active', label: 'Running' },
		...(['success', 'failed', 'rolled_back', 'cancelled'] as JobStatus[]).map((s) => ({ value: s, label: jobStatusLabels[s] }))
	];
	const anyActive = $derived(jobs.some((j) => isActive(j.status)));

	async function load() {
		try {
			jobs = (await api.jobs({ app: appFilter, status: statusFilter, limit: 100 })).jobs;
			loadError = '';
		} catch (e) {
			loadError = e instanceof Error ? e.message : String(e);
		} finally {
			loaded = true;
		}
	}

	$effect(() => {
		api.apps().then(
			(res) => (apps = res.apps.filter((a) => a.update_type)),
			() => {}
		);
	});

	// Reload when a filter changes, and periodically (faster while a job runs).
	$effect(() => {
		void appFilter;
		void statusFilter;
		load();
	});
	$effect(() => {
		const timer = setInterval(load, anyActive ? ACTIVE_REFRESH_MS : IDLE_REFRESH_MS);
		return () => clearInterval(timer);
	});
</script>

<svelte:head>
	<title>Jobs — LUM</title>
</svelte:head>

<div class="head">
	<h1>Update jobs</h1>
	<div class="filters">
		<label>
			<span class="sr-only">App</span>
			<select bind:value={appFilter}>
				<option value="">All apps</option>
				{#each apps as a (a.id)}
					<option value={a.id}>{a.name}</option>
				{/each}
			</select>
		</label>
		<label>
			<span class="sr-only">Status</span>
			<select bind:value={statusFilter}>
				{#each statusOptions as o (o.value)}
					<option value={o.value}>{o.label}</option>
				{/each}
			</select>
		</label>
	</div>
</div>

{#if loadError}
	<div class="alert">Could not load: {loadError}</div>
{/if}

{#if loaded && jobs.length === 0 && !loadError}
	<div class="card muted">
		No job{appFilter || statusFilter ? ' matching these filters' : ''}. Start an update from the dashboard or from an
		app page.
	</div>
{:else if jobs.length > 0}
	<div class="card table-card">
		<table class="data jobs">
			<thead>
				<tr>
					<th>Date</th>
					<th>App</th>
					<th>Versions</th>
					<th>Status</th>
					<th>Duration</th>
					<th>By</th>
				</tr>
			</thead>
			<tbody>
				{#each jobs as j (j.id)}
					<tr>
						<td data-label="Date"><a href="/jobs/{j.id}" title={formatDate(j.created_at)}>{formatRelative(j.created_at)}</a></td>
						<td data-label="App"><span><a href="/apps/{j.app_id}" class="name">{j.app_name}</a> <span class="muted type">{j.type}</span></span></td>
						<td data-label="Versions" class="mono">{version(j.current_version)} → {version(j.target_version)}</td>
						<td data-label="Status">
							<a href="/jobs/{j.id}" class="badge-link"><JobStatusBadge status={j.status} /></a>
							{#if j.error}<div class="error-text" class:quiet={j.status === 'cancelled'}>{j.error}</div>{/if}
						</td>
						<td data-label="Duration">{formatDuration(j.started_at || j.created_at, j.finished_at)}</td>
						<td data-label="By">{j.created_by}</td>
					</tr>
				{/each}
			</tbody>
		</table>
	</div>
{/if}

<style>
	.head {
		display: flex;
		align-items: center;
		justify-content: space-between;
		flex-wrap: wrap;
		gap: 0.75rem 1rem;
		margin-bottom: 1rem;
	}
	.head h1 {
		margin: 0;
	}
	.filters {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem;
	}
	select {
		font: inherit;
		font-size: 0.875rem;
		padding: 0.375rem 0.5rem;
		border-radius: var(--radius);
		border: 1px solid var(--border);
		background: var(--surface);
		color: var(--text);
		max-width: 100%;
	}
	.table-card {
		padding: 0.25rem 0.5rem;
		overflow-x: auto;
	}
	.name {
		font-weight: 500;
		color: var(--text);
	}
	.type {
		font-size: 0.75rem;
	}
	.badge-link {
		text-decoration: none;
	}
	.error-text {
		max-width: 28rem;
	}
	.error-text.quiet {
		color: var(--text-muted);
	}
	.sr-only {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip: rect(0 0 0 0);
	}

	/* Phones: one card per job. */
	@media (max-width: 700px) {
		.table-card {
			padding: 0;
			background: none;
			border: none;
		}
		.jobs thead {
			display: none;
		}
		.jobs,
		.jobs tbody,
		.jobs tr,
		.jobs td {
			display: block;
			width: 100%;
		}
		.jobs tr {
			background: var(--surface);
			border: 1px solid var(--border);
			border-radius: var(--radius);
			padding: 0.5rem 0.75rem;
			margin-bottom: 0.75rem;
		}
		.jobs td {
			border: none;
			padding: 0.25rem 0;
			display: flex;
			justify-content: space-between;
			flex-wrap: wrap;
			gap: 0.25rem 1rem;
		}
		.jobs td .error-text {
			flex-basis: 100%;
			max-width: none;
			text-align: right;
		}
		.jobs td::before {
			content: attr(data-label);
			color: var(--text-muted);
			font-size: 0.8125rem;
			font-family: system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif;
		}
	}
</style>
