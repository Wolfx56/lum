<script lang="ts">
	import { ApiError, api } from '#lib/api.ts';
	import ConfirmButton from '#lib/components/ConfirmButton.svelte';
	import JobStatusBadge from '#lib/components/JobStatusBadge.svelte';
	import StatusBadge from '#lib/components/StatusBadge.svelte';
	import { formatDate, formatRelative, statusLabels, statusOrder, version } from '#lib/format.ts';
	import { actionError } from '#lib/jobs.ts';
	import type { AppSummary, Status } from '#lib/types.ts';
	import { goto } from '$app/navigation';

	const REFRESH_MS = 60_000;
	const ACTIVE_REFRESH_MS = 5_000; // while a job runs

	let apps = $state<AppSummary[]>([]);
	let loaded = $state(false);
	let loadError = $state('');
	let checkingAll = $state(false);
	let checking = $state<Record<string, boolean>>({});
	let starting = $state<Record<string, boolean>>({});
	let actionMessage = $state('');

	const sorted = $derived(
		[...apps].sort(
			(a, b) => statusOrder.indexOf(a.status) - statusOrder.indexOf(b.status) || a.name.localeCompare(b.name, 'en')
		)
	);
	const anyActive = $derived(apps.some((a) => a.active_job));
	const counts = $derived(
		apps.reduce<Partial<Record<Status, number>>>((acc, a) => ({ ...acc, [a.status]: (acc[a.status] ?? 0) + 1 }), {})
	);

	async function load() {
		try {
			const res = await api.apps();
			apps = res.apps;
			checkingAll = checkingAll || res.check_running;
			loadError = '';
		} catch (e) {
			loadError = e instanceof Error ? e.message : String(e);
		} finally {
			loaded = true;
		}
	}

	async function checkAll() {
		checkingAll = true;
		actionMessage = '';
		try {
			await api.checkAll();
		} catch (e) {
			actionMessage =
				e instanceof ApiError && e.code === 'check_running'
					? 'A check is already running, try again in a moment.'
					: `Check failed: ${e instanceof Error ? e.message : e}`;
		} finally {
			checkingAll = false;
			await load();
		}
	}

	async function checkOne(id: string) {
		checking[id] = true;
		actionMessage = '';
		try {
			await api.checkApp(id);
		} catch (e) {
			actionMessage = `Check of ${id} failed: ${e instanceof Error ? e.message : e}`;
		} finally {
			checking[id] = false;
			await load();
		}
	}

	async function startUpdate(app: AppSummary) {
		starting[app.id] = true;
		actionMessage = '';
		try {
			const { job } = await api.startUpdate(app.id);
			await goto(`/jobs/${job.id}`);
		} catch (e) {
			actionMessage = `Update of ${app.name} refused: ${actionError(e)}`;
			await load();
		} finally {
			starting[app.id] = false;
		}
	}

	$effect(() => {
		load();
	});
	$effect(() => {
		const timer = setInterval(load, anyActive ? ACTIVE_REFRESH_MS : REFRESH_MS);
		return () => clearInterval(timer);
	});
</script>

<svelte:head>
	<title>Dashboard — LUM</title>
</svelte:head>

<div class="head">
	<h1>Dashboard</h1>
	<button class="btn primary" onclick={checkAll} disabled={checkingAll}>
		{checkingAll ? 'Checking…' : 'Check now'}
	</button>
</div>

{#if apps.length > 0}
	<div class="counts">
		{#each statusOrder as s (s)}
			{#if counts[s]}
				<span class="count {s}"><strong>{counts[s]}</strong> {statusLabels[s].toLowerCase()}</span>
			{/if}
		{/each}
	</div>
{/if}

{#if loadError}
	<div class="alert">Could not load: {loadError}</div>
{/if}
{#if actionMessage}
	<div class="alert">{actionMessage}</div>
{/if}

{#if loaded && apps.length === 0 && !loadError}
	<div class="card muted">No app configured. Add app definitions to <span class="mono">apps.yaml</span>.</div>
{:else if apps.length > 0}
	<div class="card table-card">
		<table class="data apps">
			<thead>
				<tr>
					<th>App</th>
					<th>LXC</th>
					<th>Installed</th>
					<th>Available</th>
					<th>Status</th>
					<th>Checked</th>
					<th><span class="sr-only">Actions</span></th>
				</tr>
			</thead>
			<tbody>
				{#each sorted as app (app.id)}
					<tr>
						<td data-label="App">
							<a href="/apps/{app.id}" class="name">{app.name}</a>
							{#if app.error}
								<div class="error-text" class:muted-error={app.status !== 'error'}>{app.error}</div>
							{/if}
						</td>
						<td data-label="LXC" class="mono">{app.lxc_id}</td>
						<td data-label="Installed" class="mono">{version(app.current_version)}</td>
						<td data-label="Available" class="mono">
							{#if app.release_url && app.latest_version}
								<a href={app.release_url} target="_blank" rel="noopener noreferrer" title="Release notes">{app.latest_version}</a>
							{:else}
								{version(app.latest_version)}
							{/if}
						</td>
						<td data-label="Status">
							<div class="state">
								<StatusBadge status={app.status} />
								{#if app.active_job}
									<a href="/jobs/{app.active_job.id}" class="job-link" title="Follow the running job">
										<JobStatusBadge status={app.active_job.status} />
									</a>
								{:else if app.update_type && app.status === 'update_available'}
									<ConfirmButton
										label="Update"
										question="Update {app.name} to {app.latest_version}?"
										confirmLabel="Start"
										busy={starting[app.id]}
										busyLabel="Starting…"
										onconfirm={() => startUpdate(app)}
									/>
								{/if}
							</div>
						</td>
						<td data-label="Checked" title={formatDate(app.last_check_at)}>{formatRelative(app.last_check_at)}</td>
						<td class="actions">
							<button
								class="btn"
								onclick={() => checkOne(app.id)}
								disabled={checking[app.id] || checkingAll}
								title="Check {app.name}"
								aria-label="Check {app.name}"
							>
								{checking[app.id] ? '…' : '↻'}
							</button>
						</td>
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
		gap: 1rem;
		flex-wrap: wrap;
		margin-bottom: 0.75rem;
	}
	.head h1 {
		margin: 0;
	}
	.counts {
		display: flex;
		flex-wrap: wrap;
		gap: 0.5rem 1.25rem;
		margin-bottom: 1rem;
		font-size: 0.875rem;
		color: var(--text-muted);
	}
	.count.update_available strong {
		color: var(--warn);
	}
	.count.error strong {
		color: var(--ko);
	}
	.count.up_to_date strong {
		color: var(--ok);
	}
	.table-card {
		padding: 0.25rem 0.5rem;
		overflow-x: auto;
	}
	.name {
		font-weight: 500;
		color: var(--text);
	}
	.state {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.375rem 0.5rem;
	}
	.job-link {
		text-decoration: none;
	}
	.muted-error {
		color: var(--text-muted);
	}
	.actions {
		text-align: right;
		width: 1%;
	}
	.sr-only {
		position: absolute;
		width: 1px;
		height: 1px;
		overflow: hidden;
		clip: rect(0 0 0 0);
	}

	/* Phones: one card per app instead of a table row. */
	@media (max-width: 700px) {
		.table-card {
			padding: 0;
			background: none;
			border: none;
		}
		.apps thead {
			display: none;
		}
		.apps,
		.apps tbody,
		.apps tr,
		.apps td {
			display: block;
			width: 100%;
		}
		.apps tr {
			background: var(--surface);
			border: 1px solid var(--border);
			border-radius: var(--radius);
			padding: 0.5rem 0.75rem;
			margin-bottom: 0.75rem;
			position: relative;
		}
		.apps td {
			border: none;
			padding: 0.25rem 0;
			display: flex;
			justify-content: space-between;
			gap: 1rem;
		}
		.apps td[data-label]::before {
			content: attr(data-label);
			color: var(--text-muted);
			font-size: 0.8125rem;
			/* Labels keep the text font even when the value is monospace (.mono). */
			font-family: system-ui, -apple-system, 'Segoe UI', Roboto, sans-serif;
		}
		.state {
			justify-content: flex-end;
		}
		.apps td[data-label='App'] {
			display: block;
			padding-right: 3rem;
		}
		.apps td[data-label='App']::before {
			display: none;
		}
		/* .apps td.actions: must beat the .apps td rule above (display, width). */
		.apps td.actions {
			position: absolute;
			top: 0.5rem;
			right: 0.75rem;
			display: block;
			width: auto;
			padding: 0;
		}
	}
</style>
