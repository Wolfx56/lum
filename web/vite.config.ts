import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vitest/config';

// Dev proxy to the Go server (make run-server). changeOrigin must stay false:
// the server's CSRF check compares the browser Origin (localhost:5173) with the
// Host header, which must therefore be preserved, as HAProxy does in production.
const goServer = { target: 'http://localhost:8080', changeOrigin: false };

export default defineConfig({
	plugins: [
		sveltekit({
			compilerOptions: {
				// Force runes mode for the project, except for libraries. Can be removed in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},

			// Static SPA embedded in the Go binary (internal/ui, build tag embedui).
			// index.html is the fallback: the Go server serves it for every unknown non-/api path.
			adapter: adapter({
				pages: '../internal/ui/dist',
				assets: '../internal/ui/dist',
				fallback: 'index.html'
			})
		})
	],
	server: {
		proxy: {
			'/api': goServer,
			'/auth': goServer,
			'/healthz': goServer,
			'/metrics': goServer
		}
	},
	test: {
		include: ['src/**/*.test.ts']
	}
});
