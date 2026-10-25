<script lang="ts">
	import { ApiError, api } from '#lib/api.ts';
	import ConfirmButton from '#lib/components/ConfirmButton.svelte';
	import JobStatusBadge from '#lib/components/JobStatusBadge.svelte';
	import { formatClock, formatDate, formatDuration, isActive, version } from '#lib/format.ts';
	import { actionError, codeMessage } from '#lib/jobs.ts';
	import { type StreamState, streamJobLogs } from '#lib/logstream.ts';
	import type { JobDetail, JobLog } from '#lib/types.ts';
	import { page } from '$app/state';
	import { tick } from 'svelte';

	let detail = $state<JobDetail | null>(null);
	let notFound = $state(false);
	let loadError = $state('');
	let logs = $state<JobLog[]>([]);
	let streamState = $state<StreamState>('connecting');
	let actionMessage = $state('');
	let cancelling = $state(false);
	let rollingBack = $state(false);
	let follow = $state(true);
	let now = $state(new Date());
	let logEl = $state<HTMLElement>();

	const id = $derived(page.params.id ?? '');
	const job = $derived(detail?.job);
	const active = $derived(job ? isActive(job.status) : false);

	let lastSeq = 0;
	let closeStream: (() => void) | undefined;

	async function load(jobID: string) {
		try {
			detail = await api.job(jobID);
			notFound = false;
			loadError = '';
		} catch (e) {
			notFound = e instanceof ApiError && e.status === 404;
			loadError = notFound ? '' : e instanceof Error ? e.message : String(e);
		}
	}

	function openStream(jobID: string) {
		closeStream?.();
		closeStream = streamJobLogs(jobID, lastSeq, {
			onLog: (l) => {
				if (l.seq <= lastSeq) return; // already received (reconnection)
				lastSeq = l.seq;
				logs.push(l);
			},
			onStatus: (j) => {
				if (detail && detail.job.status !== j.status) {
					detail.job = j;
					load(jobID); // cancel and rollback availability change with the status
				}
			},
			onEnd: (j) => {
				if (detail) detail.job = j;
				load(jobID);
			},
			onState: (s) => (streamState = s)
		});
	}

	$effect(() => {
		const jobID = id;
		logs = [];
		lastSeq = 0;
		follow = true;
		load(jobID);
		openStream(jobID);
		return () => closeStream?.();
	});

	// Duration of a running job.
	$effect(() => {
		if (!active) return;
		const timer = setInterval(() => (now = new Date()), 1000);
		return () => clearInterval(timer);
	});

	// Follow the end of the logs, unless the user scrolled up.
	$effect(() => {
		void logs.length;
		if (!follow || !logEl) return;
		tick().then(() => logEl && (logEl.scrollTop = logEl.scrollHeight));
	});

	function onLogScroll() {
		if (!logEl) return;
		follow = logEl.scrollHeight - logEl.scrollTop - logEl.clientHeight < 40;
	}

	async function cancel() {
		cancelling = true;
		actionMessage = '';
		try {
			await api.cancelJob(id);
		} catch (e) {
			actionMessage = `Could not cancel: ${actionError(e)}`;
		} finally {
			cancelling = false;
			await load(id);
		}
	}

	async function rollback() {
		rollingBack = true;
		actionMessage = '';
		try {
			await api.rollbackJob(id);
			openStream(id); // the rollback writes to the logs of the job
		} catch (e) {
			actionMessage = `Could not roll back: ${actionError(e)}`;
		} finally {
			rollingBack = false;
			await load(id);
		}
	}

	const streamLabels: Record<StreamState, string> = {
		connecting: 'connecting…',
		live: 'live',
		reconnecting: 'connection lost, retrying…',
		ended: 'ended'
	};
</script>

<svelte:head>
	<title>{job ? `${job.app_name} ${job.target_version}` : 'Job'} — LUM</title>
</svelte:head>

<p><a href="/jobs">← Jobs</a></p>

{#if notFound}
	<div class="card">Job not found: <span class="mono">{id}</span>.</div>
{:else if loadError && !detail}
	<div class="alert">Could not load: {loadError}</div>
{:else if detail && job}
	<div class="head">
		<h1><a href="/apps/{job.app_id}">{job.app_name}</a> <span class="versions mono">{version(job.current_version)} → {version(job.target_version)}</span></h1>
		<JobStatusBadge status={job.status} />
		<span class="spacer"></span>
		{#if detail.running && active}
			<ConfirmButton
				label="Cancel job"
				question={job.type === 'script'
					? 'Stop the script (SIGTERM, then SIGKILL after 30s)?'
					: 'Stop waiting for the update?'}
				confirmLabel="Stop"
				busy={cancelling}
				busyLabel="Cancelling…"
				danger
				onconfirm={cancel}
			/>
		{/if}
		{#if detail.rollback.available}
			<ConfirmButton
				label="Rollback"
				question="Restore the whole LXC {job.lxc_id ?? ''} to snapshot {job.snapshot_name}? Data changed since then will be lost."
				confirmLabel="Restore"
				busy={rollingBack}
				busyLabel="Rollback…"
				danger
				onconfirm={rollback}
			/>
		{/if}
	</div>

	{#if actionMessage}
		<div class="alert">{actionMessage}</div>
	{/if}
	{#if job.error}
		<div class="alert" class:muted-alert={job.status === 'cancelled'}>{job.error}</div>
	{/if}

	<section class="card">
		<dl class="props">
			<dt>Type</dt>
			<dd>{job.type === 'script' ? 'script run by the agent' : 'API request to the app'}</dd>
			<dt>Requested by</dt>
			<dd>{job.created_by}, {formatDate(job.created_at)}</dd>
			<dt>Duration</dt>
			<dd>{formatDuration(job.started_at || job.created_at, job.finished_at, now)}{active ? ' (running)' : ''}</dd>
			<dt>Snapshot</dt>
			<dd>
				{#if job.snapshot_name}
					<span class="mono">{job.snapshot_name}</span>{#if job.lxc_id} of LXC <span class="mono">{job.lxc_id}</span>{/if}
				{:else}
					<span class="muted">none</span>
				{/if}
			</dd>
			{#if job.snapshot_name && !detail.rollback.available && detail.rollback.reason && !['job_not_finished', 'already_rolled_back'].includes(detail.rollback.reason)}
				<dt>Rollback</dt>
				<dd class="muted">
					unavailable: {codeMessage(detail.rollback.reason) ?? detail.rollback.message}
					{#if detail.rollback.reason === 'proxmox_error'}<span class="error-text">({detail.rollback.message})</span>{/if}
				</dd>
			{/if}
			<dt>ID</dt>
			<dd class="mono">{job.id}</dd>
		</dl>
	</section>

	<section class="card logs-card">
		<div class="logs-head">
			<h2>Logs</h2>
			<span class="stream {streamState}">{streamLabels[streamState]}</span>
			{#if !follow}
				<button
					class="btn"
					onclick={() => {
						follow = true;
						if (logEl) logEl.scrollTop = logEl.scrollHeight;
					}}>↓ Follow</button
				>
			{/if}
		</div>
		<!-- Focusable so that the scrollable logs can be scrolled with the keyboard. -->
		<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
		<div class="logs" bind:this={logEl} onscroll={onLogScroll} role="log" aria-live="polite" tabindex="0">
			{#each logs as l (l.seq)}
				<div class="line {l.stream}"><span class="ts">{formatClock(l.ts)}</span><span class="text">{l.line}</span></div>
			{:else}
				<div class="muted">No lines yet.</div>
			{/each}
		</div>
		<p class="legend muted"><span class="system">■ LUM</span> <span>■ stdout</span> <span class="stderr">■ stderr</span></p>
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
	.head h1 a {
		color: var(--text);
		text-decoration: none;
	}
	.versions {
		font-size: 1rem;
		font-weight: 400;
		color: var(--text-muted);
	}
	.spacer {
		flex: 1;
	}
	.muted-alert {
		background: var(--neutral-bg);
		color: var(--text-muted);
	}
	.logs-card {
		padding-bottom: 0.5rem;
	}
	.logs-head {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-bottom: 0.5rem;
	}
	.logs-head h2 {
		margin: 0;
	}
	.stream {
		font-size: 0.8125rem;
		color: var(--text-muted);
		flex: 1;
	}
	.stream.live {
		color: var(--ok);
	}
	.stream.reconnecting {
		color: var(--warn);
	}
	.logs {
		font-family: ui-monospace, 'SF Mono', Menlo, monospace;
		font-size: 0.8125rem;
		line-height: 1.45;
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0.5rem 0.75rem;
		height: min(60vh, 36rem);
		overflow: auto;
	}
	.line {
		display: flex;
		gap: 0.75rem;
	}
	.ts {
		color: var(--text-muted);
		flex: none;
		user-select: none;
	}
	.text {
		white-space: pre-wrap;
		overflow-wrap: anywhere;
		min-width: 0;
	}
	.system,
	.line.system .text {
		color: var(--accent);
	}
	.stderr,
	.line.stderr .text {
		color: var(--ko);
	}
	.legend {
		display: flex;
		gap: 1rem;
		font-size: 0.75rem;
		margin: 0.375rem 0 0;
	}
	@media (max-width: 640px) {
		.ts {
			display: none;
		}
	}
</style>
