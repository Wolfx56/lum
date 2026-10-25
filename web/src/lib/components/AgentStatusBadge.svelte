<script lang="ts">
	import { agentStatusLabels } from '#lib/format.ts';
	import type { AgentStatus } from '#lib/types.ts';

	let { status }: { status: AgentStatus } = $props();
</script>

<span class="status {status}">{agentStatusLabels[status]}</span>

<style>
	.status {
		display: inline-block;
		font-size: 0.8125rem;
		font-weight: 500;
		padding: 0.125rem 0.625rem;
		border-radius: 999px;
		white-space: nowrap;
		color: var(--text-muted);
		background: var(--neutral-bg);
	}
	.online {
		color: var(--ok);
		background: var(--ok-bg);
	}
	.offline {
		color: var(--warn);
		background: var(--warn-bg);
	}
	.revoked {
		color: var(--ko);
		background: var(--ko-bg);
	}
</style>
