import { describe, expect, it } from 'vitest';
import { fetchHealth } from './health';

const fakeFetch = (res: Response | Error) => (async () => {
	if (res instanceof Error) throw res;
	return res;
}) as unknown as typeof fetch;

describe('fetchHealth', () => {
	it('is online when /healthz answers ok', async () => {
		const res = new Response(JSON.stringify({ status: 'ok', version: '1.2.3' }), { status: 200 });
		expect(await fetchHealth(fakeFetch(res))).toEqual({ state: 'online', version: '1.2.3' });
	});

	it('is offline on an HTTP error', async () => {
		expect(await fetchHealth(fakeFetch(new Response('', { status: 502 })))).toEqual({
			state: 'offline',
			version: null
		});
	});

	it('is offline when the server is unreachable', async () => {
		expect(await fetchHealth(fakeFetch(new TypeError('fetch failed')))).toEqual({
			state: 'offline',
			version: null
		});
	});
});
