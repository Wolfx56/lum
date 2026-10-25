<script lang="ts">
	import { api } from '#lib/api.ts';
	import AgentStatusBadge from '#lib/components/AgentStatusBadge.svelte';
	import ConfirmButton from '#lib/components/ConfirmButton.svelte';
	import { formatDate, formatRelative, scriptStateLabels } from '#lib/format.ts';
	import { actionError } from '#lib/jobs.ts';
	import type { Agent } from '#lib/types.ts';

	const REFRESH_MS = 15_000;

	let agents = $state<Agent[]>([]);
	let loaded = $state(false);
	let loadError = $state('');
	let busy = $state<Record<string, string>>({}); // agent id → action in progress
	let messages = $state<Record<string, { ok: boolean; text: string }>>({});

	const origin = typeof window !== 'undefined' ? window.location.origin : '';
	const binaryLabels: Record<Agent['binary'], string> = {
		up_to_date: 'up to date',
		outdated: 'differs from the server one (self_update pending)',
		unknown: 'unknown'
	};

	async function load() {
		try {
			agents = (await api.agents()).agents;
			loadError = '';
		} catch (e) {
			loadError = e instanceof Error ? e.message : String(e);
		} finally {
			loaded = true;
		}
	}

	async function act(id: string, action: string, run: () => Promise<unknown>, success: string) {
		busy[id] = action;
		delete messages[id];
		try {
			await run();
			messages[id] = { ok: true, text: success };
		} catch (e) {
			messages[id] = { ok: false, text: actionError(e) };
		} finally {
			delete busy[id];
			await load();
		}
	}

	const sync = (id: string) => act(id, 'sync', () => api.syncAgentScripts(id), 'Scripts synced.');
	const revoke = (id: string) =>
		act(id, 'revoke', () => api.revokeAgent(id), 'Agent revoked: its token is refused and its connection closed.');
	const remove = (id: string) =>
		act(id, 'delete', () => api.deleteAgent(id), 'Agent deleted: it can enroll again with the shared secret.');

	$effect(() => {
		load();
		const timer = setInterval(load, REFRESH_MS);
		return () => clearInterval(timer);
	});
</script>

<svelte:head>
	<title>Agents — LUM</title>
</svelte:head>

<h1>Agents</h1>

{#if loadError}
	<div class="alert">Could not load: {loadError}</div>
{/if}

{#if loaded && agents.length === 0 && !loadError}
	<div class="card muted">
		No agent. An agent is only needed for app definitions with <span class="mono">current.type: agent</span> or
		<span class="mono">update.type: script</span> (see the agent documentation).
	</div>
{/if}

<div class="list">
	{#each agents as a (a.id)}
		<section class="card agent">
			<div class="title-row">
				<h2 class="mono">{a.id}</h2>
				<AgentStatusBadge status={a.status} />
				<span class="spacer"></span>
				{#if a.status === 'online'}
					<button class="btn" onclick={() => sync(a.id)} disabled={!!busy[a.id]}>
						{busy[a.id] === 'sync' ? 'Syncing…' : 'Sync scripts'}
					</button>
				{/if}
				{#if a.status === 'online' || a.status === 'offline'}
					<ConfirmButton
						label="Revoke"
						question="Revoke {a.id}? Its token will be refused; a running script will no longer be tracked."
						confirmLabel="Revoke"
						busy={busy[a.id] === 'revoke'}
						busyLabel="Revoking…"
						disabled={!!busy[a.id]}
						danger
						onconfirm={() => revoke(a.id)}
					/>
				{:else if a.status === 'revoked'}
					<ConfirmButton
						label="Delete"
						question="Forget {a.id}? It will be able to enroll again with the shared secret."
						confirmLabel="Delete"
						busy={busy[a.id] === 'delete'}
						busyLabel="Deleting…"
						disabled={!!busy[a.id]}
						danger
						onconfirm={() => remove(a.id)}
					/>
				{/if}
			</div>

			{#if messages[a.id]}
				<div class="alert" class:ok={messages[a.id].ok}>{messages[a.id].text}</div>
			{/if}

			{#if a.status === 'not_enrolled'}
				<p class="muted">Referenced in <span class="mono">apps.yaml</span> but never connected. Install it in the LXC, as root:</p>
				<pre class="mono install">read -rs LUM_AGENT_SECRET && export LUM_AGENT_SECRET
curl -fsSL {origin}/install-agent.sh | bash -s -- --id {a.id}</pre>
			{:else}
				<dl class="props">
					<dt>Last seen</dt>
					<dd title={formatDate(a.last_seen_at)}>{formatRelative(a.last_seen_at)}</dd>
					<dt>Host</dt>
					<dd class="mono">{a.hostname || '—'}</dd>
					<dt>Version</dt>
					<dd>
						<span class="mono">{a.agent_version || '—'}</span>
						{#if a.status === 'online'}<span class="muted"> · binary {binaryLabels[a.binary]}</span>{/if}
					</dd>
					<dt>Enrolled</dt>
					<dd>{formatDate(a.enrolled_at)}</dd>
				</dl>
			{/if}

			<dl class="props">
				<dt>Apps</dt>
				<dd>
					{#each a.apps as app, i (app)}{i > 0 ? ', ' : ''}<a href="/apps/{app}">{app}</a>{:else}<span class="muted"
							>none in apps.yaml</span
						>{/each}
				</dd>
				<dt>Scripts</dt>
				<dd>
					{#if a.scripts.length === 0}
						<span class="muted">none (no app definition with update.type: script)</span>
					{:else}
						<ul class="scripts">
							{#each a.scripts as s (s.name)}
								<li>
									<span class="mono">{s.name}</span>
									<span class="sync {s.state}" title="server {s.server_sha256.slice(0, 12)}{s.agent_sha256
											? ' · agent ' + s.agent_sha256.slice(0, 12)
											: ''}">{scriptStateLabels[s.state]}</span
									>
								</li>
							{/each}
						</ul>
					{/if}
				</dd>
			</dl>
		</section>
	{/each}
</div>

<style>
	.list {
		display: grid;
		grid-template-columns: repeat(auto-fill, minmax(min(100%, 26rem), 1fr));
		gap: 0 1rem;
	}
	.title-row {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.5rem 0.75rem;
		margin-bottom: 0.75rem;
	}
	.title-row h2 {
		margin: 0;
		font-size: 1rem;
	}
	.spacer {
		flex: 1;
	}
	.agent dl.props + dl.props {
		margin-top: 0.375rem;
	}
	.install {
		background: var(--bg);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		padding: 0.5rem 0.75rem;
		overflow-x: auto;
		white-space: pre;
		word-break: normal;
		margin: 0 0 0.75rem;
	}
	.scripts {
		list-style: none;
		padding: 0;
		margin: 0;
		display: grid;
		gap: 0.25rem;
	}
	.sync {
		font-size: 0.75rem;
		padding: 0 0.5rem;
		border-radius: 999px;
		margin-left: 0.375rem;
	}
	.in_sync {
		color: var(--ok);
		background: var(--ok-bg);
	}
	.stale {
		color: var(--warn);
		background: var(--warn-bg);
	}
	.missing {
		color: var(--ko);
		background: var(--ko-bg);
	}
</style>
