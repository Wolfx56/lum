<!-- Button asking for an inline confirmation before acting (no browser dialog). -->
<script lang="ts">
	let {
		label,
		question,
		confirmLabel = 'Confirm',
		busyLabel = '…',
		busy = false,
		disabled = false,
		danger = false,
		onconfirm
	}: {
		label: string;
		question: string;
		confirmLabel?: string;
		busyLabel?: string;
		busy?: boolean;
		disabled?: boolean;
		danger?: boolean;
		onconfirm: () => void;
	} = $props();

	let asking = $state(false);
	let confirmEl = $state<HTMLButtonElement>();

	$effect(() => {
		if (asking) confirmEl?.focus();
	});

	function onkeydown(e: KeyboardEvent) {
		if (e.key === 'Escape') asking = false;
	}
</script>

{#if asking}
	<div class="confirm" role="group" aria-label={question}>
		<span class="question">{question}</span>
		<span class="buttons">
			<button
				bind:this={confirmEl}
				class="btn"
				class:primary={!danger}
				class:danger
				{onkeydown}
				onclick={() => {
					asking = false;
					onconfirm();
				}}>{confirmLabel}</button
			>
			<button class="btn" {onkeydown} onclick={() => (asking = false)}>Back</button>
		</span>
	</div>
{:else}
	<button class="btn" class:primary={!danger} class:danger-outline={danger} disabled={disabled || busy} onclick={() => (asking = true)}>
		{busy ? busyLabel : label}
	</button>
{/if}

<style>
	.confirm {
		display: inline-flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem 0.75rem;
		padding: 0.5rem 0.75rem;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--neutral-bg);
		max-width: 100%;
	}
	.question {
		font-size: 0.875rem;
	}
	.buttons {
		display: inline-flex;
		gap: 0.5rem;
	}
	.danger {
		background: var(--ko);
		border-color: var(--ko);
		color: var(--surface);
	}
	.danger-outline {
		color: var(--ko);
	}
	.danger-outline:hover:not(:disabled) {
		border-color: var(--ko);
	}
</style>
