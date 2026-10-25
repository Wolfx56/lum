<script lang="ts">
	import { isActive, jobStatusLabels } from '#lib/format.ts';
	import type { JobStatus } from '#lib/types.ts';

	let { status }: { status: JobStatus } = $props();
</script>

<span class="status {status}" class:active={isActive(status)}>
	{#if isActive(status)}<span class="dot" aria-hidden="true"></span>{/if}{jobStatusLabels[status]}
</span>

<style>
	.status {
		display: inline-flex;
		align-items: center;
		gap: 0.375rem;
		font-size: 0.8125rem;
		font-weight: 500;
		padding: 0.125rem 0.625rem;
		border-radius: 999px;
		white-space: nowrap;
		color: var(--text-muted);
		background: var(--neutral-bg);
	}
	.active {
		color: var(--accent);
	}
	.success {
		color: var(--ok);
		background: var(--ok-bg);
	}
	.failed {
		color: var(--ko);
		background: var(--ko-bg);
	}
	.rolled_back {
		color: var(--warn);
		background: var(--warn-bg);
	}
	.dot {
		width: 0.5rem;
		height: 0.5rem;
		border-radius: 50%;
		background: currentColor;
		animation: pulse 1.2s ease-in-out infinite;
	}
	@keyframes pulse {
		50% {
			opacity: 0.3;
		}
	}
	@media (prefers-reduced-motion: reduce) {
		.dot {
			animation: none;
		}
	}
</style>
