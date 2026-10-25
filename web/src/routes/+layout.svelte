<script lang="ts">
	import '../app.css';
	import favicon from '#lib/assets/favicon.svg';
	import { logout } from '#lib/api.ts';
	import { health, pollHealth } from '#lib/health.svelte.ts';
	import { loadSession, session } from '#lib/session.svelte.ts';
	import { page } from '$app/state';
	import type { LayoutProps } from './$types';

	let { children }: LayoutProps = $props();

	const links = [
		{ href: '/', label: 'Dashboard' },
		{ href: '/jobs', label: 'Jobs' },
		{ href: '/agents', label: 'Agents' },
		{ href: '/system', label: 'System status' }
	];

	let menuOpen = $state(false);
	let loggingOut = $state(false);
	let menuEl = $state<HTMLElement>();
	let toggleEl = $state<HTMLButtonElement>();

	const active = (href: string) =>
		href === '/' ? page.url.pathname === '/' || page.url.pathname.startsWith('/apps/') : page.url.pathname.startsWith(href);

	const user = $derived(session.me?.user);
	const oidc = $derived(session.me?.auth_mode === 'oidc');

	async function onLogout() {
		loggingOut = true;
		await logout();
	}

	// Mobile menu: close after navigating, on Escape, and on a click outside it.
	$effect(() => {
		void page.url.pathname;
		menuOpen = false;
	});

	function onWindowClick(e: MouseEvent) {
		const target = e.target as Node;
		if (menuOpen && !menuEl?.contains(target) && !toggleEl?.contains(target)) menuOpen = false;
	}

	function onWindowKeydown(e: KeyboardEvent) {
		if (menuOpen && e.key === 'Escape') {
			menuOpen = false;
			toggleEl?.focus();
		}
	}

	$effect(() => pollHealth());
	$effect(() => {
		loadSession();
	});
</script>

<svelte:head>
	<link rel="icon" href={favicon} />
</svelte:head>

<svelte:window onclick={onWindowClick} onkeydown={onWindowKeydown} />

{#snippet navLinks()}
	{#each links as link (link.href)}
		<a href={link.href} class:active={active(link.href)} aria-current={active(link.href) ? 'page' : undefined}>
			{link.label}
		</a>
	{/each}
{/snippet}

{#snippet logoutButton()}
	<button class="btn logout" onclick={onLogout} disabled={loggingOut}>
		{loggingOut ? 'Logging out…' : 'Log out'}
	</button>
{/snippet}

<header class="topbar">
	<a class="brand" href="/">
		<strong>LUM</strong>
		<span class="muted">LXC Update Manager</span>
	</a>

	<!-- Wide screens: everything visible in the bar. -->
	<nav class="desktop-nav" aria-label="Main navigation">
		{@render navLinks()}
	</nav>

	<div class="right">
		{#if health.state === 'offline'}
			<span class="offline">Server offline</span>
		{/if}

		<div class="desktop-account">
			{#if oidc && user}
				<span class="who-inline" title={user.email}>{user.name || user.username}</span>
				{@render logoutButton()}
			{:else if session.me}
				<span class="muted anonymous" title="Authentication disabled (auth.mode: none)">Anonymous access</span>
			{/if}
		</div>

		<!-- Phones: navigation and account behind a hamburger. -->
		<button
			bind:this={toggleEl}
			class="burger"
			class:open={menuOpen}
			aria-label={menuOpen ? 'Close menu' : 'Open menu'}
			aria-expanded={menuOpen}
			aria-controls="mobile-menu"
			onclick={() => (menuOpen = !menuOpen)}
		>
			<span></span><span></span><span></span>
		</button>
	</div>

	{#if menuOpen}
		<div id="mobile-menu" class="menu" bind:this={menuEl}>
			<nav aria-label="Main navigation">
				{@render navLinks()}
			</nav>

			<div class="account">
				{#if oidc && user}
					<div class="who">
						<strong>{user.name || user.username}</strong>
						{#if user.email}<span class="muted">{user.email}</span>{/if}
					</div>
					{@render logoutButton()}
				{:else if session.me}
					<p class="muted note">Authentication disabled (<span class="mono">auth.mode: none</span>): anonymous access.</p>
				{/if}
			</div>
		</div>
	{/if}
</header>

<main>
	{@render children()}
</main>

<style>
	.topbar {
		position: sticky;
		top: 0;
		z-index: 10;
		display: flex;
		align-items: center;
		gap: 0.75rem 1.5rem;
		padding: 0.625rem 16px;
		background: var(--surface);
		border-bottom: 1px solid var(--border);
	}
	.brand {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		color: var(--text);
		text-decoration: none;
	}
	.brand .muted {
		font-size: 0.875rem;
	}
	.right {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin-left: auto;
	}
	.offline {
		font-size: 0.8125rem;
		color: var(--ko);
		background: var(--ko-bg);
		padding: 0.125rem 0.625rem;
		border-radius: 999px;
	}

	/* Wide screens: inline navigation and account. */
	.desktop-nav {
		display: flex;
		gap: 1rem;
	}
	.desktop-nav a {
		color: var(--text-muted);
		text-decoration: none;
		padding: 0.25rem 0;
		border-bottom: 2px solid transparent;
	}
	.desktop-nav a:hover {
		color: var(--text);
	}
	.desktop-nav a.active {
		color: var(--text);
		border-bottom-color: var(--accent);
	}
	.desktop-account {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		font-size: 0.875rem;
	}
	.who-inline {
		color: var(--text-muted);
	}
	.anonymous {
		cursor: help;
	}

	/* Hamburger button, turns into a cross when open. */
	.burger {
		display: none;
		place-content: center;
		gap: 5px;
		width: 2.5rem;
		height: 2.5rem;
		padding: 0;
		border: 1px solid var(--border);
		border-radius: var(--radius);
		background: var(--surface);
		cursor: pointer;
	}
	.burger:hover,
	.burger.open {
		border-color: var(--accent);
	}
	.burger span {
		display: block;
		width: 1.125rem;
		height: 2px;
		border-radius: 1px;
		background: var(--text);
		transition:
			transform 0.15s ease,
			opacity 0.15s ease;
	}
	.burger.open span:nth-child(1) {
		transform: translateY(7px) rotate(45deg);
	}
	.burger.open span:nth-child(2) {
		opacity: 0;
	}
	.burger.open span:nth-child(3) {
		transform: translateY(-7px) rotate(-45deg);
	}

	/* Mobile dropdown panel under the button. */
	.menu {
		display: none;
		position: absolute;
		top: calc(100% + 0.375rem);
		right: 16px;
		width: min(18rem, calc(100vw - 32px));
		background: var(--surface);
		border: 1px solid var(--border);
		border-radius: var(--radius);
		box-shadow: 0 8px 24px rgb(0 0 0 / 0.15);
		padding: 0.5rem;
	}
	.menu nav {
		display: grid;
	}
	.menu nav a {
		padding: 0.5rem 0.75rem;
		border-radius: calc(var(--radius) - 2px);
		color: var(--text);
		text-decoration: none;
	}
	.menu nav a:hover {
		background: var(--neutral-bg);
	}
	.menu nav a.active {
		color: var(--accent);
		font-weight: 500;
	}
	.account {
		border-top: 1px solid var(--border);
		margin-top: 0.5rem;
		padding: 0.75rem 0.75rem 0.25rem;
		display: grid;
		gap: 0.625rem;
	}
	.who {
		display: grid;
		font-size: 0.875rem;
		overflow-wrap: anywhere;
	}
	.menu .logout {
		justify-content: center;
	}
	.note {
		margin: 0;
		font-size: 0.8125rem;
	}

	@media (max-width: 768px) {
		.desktop-nav,
		.desktop-account {
			display: none;
		}
		.burger {
			display: grid;
		}
		.menu {
			display: block;
		}
	}
	@media (max-width: 480px) {
		.brand .muted {
			display: none;
		}
	}

	main {
		max-width: 1200px;
		margin: 0 auto;
		padding: 1.5rem 16px;
	}
</style>
