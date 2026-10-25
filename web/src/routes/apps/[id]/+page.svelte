<script lang="ts">
	import { ApiError, api } from '#lib/api.ts';
	import ConfirmButton from '#lib/components/ConfirmButton.svelte';
	import JobStatusBadge from '#lib/components/JobStatusBadge.svelte';
	import StatusBadge from '#lib/components/StatusBadge.svelte';
	import { formatDate, formatDuration, formatRelative, version } from '#lib/format.ts';
	import { actionError } from '#lib/jobs.ts';
	import { renderMarkdown } from '#lib/markdown.ts';
	import type { AppDetail, Job } from '#lib/types.ts';
	import { goto } from '$app/navigation';
	import { page } from '$app/state';

	const JOBS_SHOWN = 10;

	let app = $state<AppDetail | null>(null);
	let notFound = $state(false);
	let loadError = $state('');
	let checking = $state(false);
	let starting = $state(false);
	let actionMessage = $state('');
	let jobs = $state<Job[]>([]);

	const id = $derived(page.params.id ?? '');
	const canUpdate = $derived(!!app?.recipe.update && app.status === 'update_available' && !app.active_job);
	const notes = $derived(app?.release_notes ? renderMarkdown(app.release_notes) : '');

	async function load(appID: string) {
		try {
			app = await api.app(appID);
			notFound = false;
			loadError = '';
			jobs = app.recipe.update ? (await api.jobs({ app: appID, limit: JOBS_SHOWN })).jobs : [];
		} catch (e) {
			notFound = e instanceof ApiError && e.status === 404;
			loadError = notFound ? '' : e instanceof Error ? e.message : String(e);
		}
	}

	async function check() {
		checking = true;
		actionMessage = '';
		try {
			await api.checkApp(id);
		} catch (e) {
			actionMessage = `Check failed: ${e instanceof Error ? e.message : e}`;
		} finally {
			checking = false;
			await load(id);
		}
	}

	async function startUpdate() {
		starting = true;
		actionMessage = '';
		try {
			const { job } = await api.startUpdate(id);
			await goto(`/jobs/${job.id}`);
		} catch (e) {
			actionMessage = `Update refused: ${actionError(e)}`;
			await load(id);
		} finally {
			starting = false;
		}
	}

	$effect(() => {
		load(id);
	});
</script>

<svelte:head>
	<title>{app?.name ?? id} — LUM</title>
</svelte:head>

<p><a href="/">← Dashboard</a></p>

{#if notFound}
	<div class="card">Unknown app: <span class="mono">{id}</span>.</div>
{:else if loadError}
	<div class="alert">Could not load: {loadError}</div>
{:else if app}
	<div class="head">
		<h1>{app.name}</h1>
		<StatusBadge status={app.status} />
		<span class="spacer"></span>
		<button class="btn" class:primary={!canUpdate} onclick={check} disabled={checking}>
			{checking ? 'Checking…' : 'Check now'}
		</button>
		{#if canUpdate}
			<ConfirmButton
				label="Update to {app.latest_version}"
				question={app.recipe.snapshot_before_update
					? `Snapshot LXC ${app.lxc_id}, then update to ${app.latest_version}?`
					: `Update to ${app.latest_version}, without a snapshot?`}
				confirmLabel="Start"
				busy={starting}
				busyLabel="Starting…"
				onconfirm={startUpdate}
			/>
		{/if}
	</div>

	{#if actionMessage}
		<div class="alert">{actionMessage}</div>
	{/if}
	{#if app.active_job}
		<div class="alert info running">
			<JobStatusBadge status={app.active_job.status} />
			<span>An update is running.</span>
			<a href="/jobs/{app.active_job.id}">Follow the logs →</a>
		</div>
	{/if}
	{#if app.error}
		<div class="alert" class:info={app.status !== 'error'}>{app.error}</div>
	{/if}

	<div class="grid">
		<section class="card">
			<h2>Versions</h2>
			<dl class="props">
				<dt>Installed</dt>
				<dd class="mono">{version(app.current_version)}</dd>
				<dt>Available</dt>
				<dd class="mono">
					{version(app.latest_version)}
					{#if app.release_url}· <a href={app.release_url} target="_blank" rel="noopener noreferrer">release notes</a>{/if}
				</dd>
				<dt>Last check</dt>
				<dd title={formatDate(app.last_check_at)}>{formatRelative(app.last_check_at)}</dd>
				<dt>Container</dt>
				<dd>LXC <span class="mono">{app.lxc_id}</span>{#if app.agent} · agent <span class="mono">{app.agent}</span>{/if}</dd>
				<dt>Repository</dt>
				<dd><a href="https://github.com/{app.repo}" target="_blank" rel="noopener noreferrer">{app.repo}</a></dd>
			</dl>
		</section>

		<section class="card">
			<h2>App definition</h2>
			<dl class="props">
				<dt>Installed version</dt>
				<dd>
					{#if app.recipe.current.type === 'api'}
						API <span class="mono">{app.recipe.current.url}</span> → <span class="mono">{app.recipe.current.json_path}</span>
					{:else}
						agent: <span class="mono">{app.recipe.current.command?.join(' ')}</span>
					{/if}
				</dd>
				<dt>Latest version</dt>
				<dd>
					GitHub{app.recipe.upstream.include_prerelease ? ', pre-releases included' : ''} ·
					<span class="mono">{app.recipe.upstream.tag_regex}</span>
				</dd>
				<dt>Update</dt>
				<dd>
					{#if app.recipe.update?.type === 'api'}
						API <span class="mono">{app.recipe.update.method} {app.recipe.update.url}</span>
					{:else if app.recipe.update?.type === 'script'}
						script <span class="mono">{app.recipe.update.script}</span>
					{:else}
						<span class="muted">not configured</span>
					{/if}
				</dd>
				{#if app.recipe.update?.resources}
					{@const res = app.recipe.update.resources}
					<dt>Resources</dt>
					<dd>
						at least {[res.cores && `${res.cores} CPU`, res.memory && `${res.memory} MiB`].filter(Boolean).join(', ')} during the
						update
					</dd>
				{/if}
				{#if app.recipe.update}
					<dt>Snapshot / rollback</dt>
					<dd>
						{app.recipe.snapshot_before_update ? 'snapshot before update' : 'no snapshot'} ·
						{app.recipe.rollback_on_failure ? 'automatic rollback' : 'manual rollback'}
					</dd>
				{/if}
			</dl>
		</section>
	</div>

	{#if notes}
		<section class="card">
			<h2>Release notes {app.latest_version}</h2>
			<!-- Sanitized by DOMPurify in renderMarkdown. -->
			<div class="notes">{@html notes}</div>
		</section>
	{/if}

	{#if app.recipe.update}
		<section class="card history">
			<h2>Updates</h2>
			{#if jobs.length === 0}
				<p class="muted">No update started from LUM yet.</p>
			{:else}
				<div class="scroll">
					<table class="data">
						<thead>
							<tr><th>Date</th><th>Versions</th><th>Status</th><th>Duration</th><th>By</th></tr>
						</thead>
						<tbody>
							{#each jobs as j (j.id)}
								<tr>
									<td><a href="/jobs/{j.id}">{formatDate(j.created_at)}</a></td>
									<td class="mono">{version(j.current_version)} → {version(j.target_version)}</td>
									<td><JobStatusBadge status={j.status} /></td>
									<td>{formatDuration(j.started_at || j.created_at, j.finished_at)}</td>
									<td>{j.created_by}</td>
								</tr>
							{/each}
						</tbody>
					</table>
				</div>
				{#if jobs.length === JOBS_SHOWN}
					<p><a href="/jobs?app={app.id}">All jobs of {app.name} →</a></p>
				{/if}
			{/if}
		</section>
	{/if}

	<section class="card history">
		<h2>Check history</h2>
		{#if app.checks.length === 0}
			<p class="muted">No check yet.</p>
		{:else}
			<div class="scroll">
				<table class="data">
					<thead>
						<tr><th>Date</th><th>Installed</th><th>Available</th><th>Status</th><th>Error</th></tr>
					</thead>
					<tbody>
						{#each app.checks as c (c.id)}
							<tr>
								<td>{formatDate(c.checked_at)}</td>
								<td class="mono">{version(c.current_version)}</td>
								<td class="mono">{version(c.latest_version)}</td>
								<td><StatusBadge status={c.status} /></td>
								<td class="error-text">{c.status === 'error' ? c.error : ''}</td>
							</tr>
						{/each}
					</tbody>
				</table>
			</div>
		{/if}
	</section>
{/if}

<style>
	.head {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.75rem;
		margin-bottom: 1rem;
	}
	.head h1 {
		margin: 0;
	}
	.spacer {
		flex: 1;
	}
	.alert.info {
		background: var(--neutral-bg);
		color: var(--text-muted);
	}
	.alert.running {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem 0.75rem;
		color: var(--text);
	}
	.grid {
		display: grid;
		grid-template-columns: repeat(auto-fit, minmax(min(100%, 22rem), 1fr));
		gap: 0 1rem;
	}
	.notes {
		overflow-wrap: anywhere;
	}
	.notes :global(pre) {
		overflow-x: auto;
		background: var(--neutral-bg);
		padding: 0.75rem;
		border-radius: var(--radius);
	}
	.notes :global(img) {
		max-width: 100%;
	}
	.scroll {
		overflow-x: auto;
	}
</style>
