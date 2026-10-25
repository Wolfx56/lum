import { describe, expect, it } from 'vitest';
import { ApiError } from './api';
import { actionError, codeMessage } from './jobs';

describe('actionError', () => {
	it('translates the known codes', () => {
		expect(actionError(new ApiError(503, 'agent_offline', 'agent offline: vw-lxc is not connected'))).toBe(
			'The agent is not connected: nothing was done.'
		);
		expect(codeMessage('job_active')).toBe('A job is already running for this app.');
	});

	it('keeps the detail of errors coming from Proxmox or an agent', () => {
		expect(actionError(new ApiError(502, 'proxmox_error', 'proxmox error: 401'))).toBe('Proxmox is unreachable. (proxmox error: 401)');
	});

	it('falls back to the server message', () => {
		expect(actionError(new ApiError(500, 'internal', 'internal error'))).toBe('internal error');
		expect(actionError(new Error('boom'))).toBe('boom');
	});
});
