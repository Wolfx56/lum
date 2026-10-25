<script lang="ts">
	import { statusLabels } from '#lib/format.ts';
	import type { Status } from '#lib/types.ts';

	let { status }: { status: Status } = $props();
</script>

<span class="status {status}">{statusLabels[status]}</span>

<style>
	.status {
		display: inline-block;
		font-size: 0.8125rem;
		font-weight: 500;
		padding: 0.125rem 0.625rem;
		border-radius: 999px;
		white-space: nowrap;
	}
	.up_to_date {
		color: var(--ok);
		background: var(--ok-bg);
	}
	.update_available {
		color: var(--warn);
		background: var(--warn-bg);
	}
	.error {
		color: var(--ko);
		background: var(--ko-bg);
	}
	.unknown {
		color: var(--text-muted);
		background: var(--neutral-bg);
	}
</style>
