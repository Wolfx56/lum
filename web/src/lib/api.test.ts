import { describe, expect, it } from 'vitest';
import { ApiError, configErrorOf, loginURL, query, request } from './api';

const respond = (status: number, body: unknown) =>
	(async () => new Response(JSON.stringify(body), { status })) as unknown as typeof fetch;

describe('request', () => {
	it('returns the JSON body', async () => {
		const res = await request<{ apps: unknown[] }>('GET', '/apps', respond(200, { apps: [] }));
		expect(res.apps).toEqual([]);
	});

	it('maps the uniform error format to ApiError', async () => {
		const err = await request('POST', '/checks', respond(409, { error: { code: 'check_running', message: 'busy' } })).catch(
			(e) => e
		);
		expect(err).toBeInstanceOf(ApiError);
		expect(err).toMatchObject({ status: 409, code: 'check_running', message: 'busy' });
	});

	it('reports network failures', async () => {
		const failing = (async () => {
			throw new TypeError('fetch failed');
		}) as unknown as typeof fetch;
		const err = await request('GET', '/apps', failing).catch((e) => e);
		expect(err).toMatchObject({ status: 0, code: 'network' });
	});
});

describe('helpers', () => {
	it('builds the login URL with the current location', () => {
		expect(loginURL('/apps/sonarr?x=1')).toBe('/auth/login?redirect=%2Fapps%2Fsonarr%3Fx%3D1');
	});

	it('extracts reload problems', () => {
		const details = { kind: 'validation', message: 'invalid', problems: [{ path: 'apps[0].id', message: 'bad' }] };
		expect(configErrorOf(new ApiError(422, 'invalid_config', 'x', details))).toEqual(details);
		expect(configErrorOf(new Error('x'))).toBeUndefined();
	});
});

describe('query', () => {
	it('keeps only the defined values', () => {
		expect(query({ app: 'sonarr', status: '', limit: 10 })).toBe('?app=sonarr&limit=10');
		expect(query({ app: undefined })).toBe('');
	});
});
