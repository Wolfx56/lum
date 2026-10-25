import { describe, expect, it } from 'vitest';
import { formatDate, formatDuration, formatRelative, isActive, jobStatusLabels, statusLabels, version } from './format';

describe('formatRelative', () => {
	const now = new Date('2026-10-18T12:00:00Z');

	it('handles missing and zero dates', () => {
		expect(formatRelative(null, now)).toBe('never');
		expect(formatRelative('0001-01-01T00:00:00Z', now)).toBe('never');
	});

	it('formats past and future times in English', () => {
		expect(formatRelative('2026-10-18T11:59:50Z', now)).toBe('just now');
		expect(formatRelative('2026-10-18T11:55:00Z', now)).toBe('5 minutes ago');
		expect(formatRelative('2026-10-18T09:00:00Z', now)).toBe('3 hours ago');
		expect(formatRelative('2026-10-19T07:00:00Z', now)).toBe('in 19 hours');
		expect(formatRelative('2026-10-15T12:00:00Z', now)).toBe('3 days ago');
	});
});

describe('formatDate', () => {
	it('returns a dash for missing dates', () => {
		expect(formatDate(undefined)).toBe('—');
		expect(formatDate('0001-01-01T00:00:00Z')).toBe('—');
		expect(formatDate('2026-10-18T07:03:00Z')).toMatch(/10\/18\/26/);
	});
});

describe('labels', () => {
	it('labels every status in English', () => {
		expect(statusLabels.update_available).toBe('Update available');
		expect(version('')).toBe('—');
	});
});

describe('formatDuration', () => {
	const now = new Date('2026-10-18T12:00:00Z');

	it('formats seconds, minutes and hours', () => {
		expect(formatDuration('2026-10-18T11:59:15Z', '2026-10-18T12:00:00Z')).toBe('45s');
		expect(formatDuration('2026-10-18T11:56:55Z', '2026-10-18T12:00:00Z')).toBe('3m 05s');
		expect(formatDuration('2026-10-18T10:58:00Z', '2026-10-18T12:00:00Z')).toBe('1h 02m');
	});

	it('runs until now without end, and handles missing dates', () => {
		expect(formatDuration('2026-10-18T11:59:30Z', undefined, now)).toBe('30s');
		expect(formatDuration(undefined)).toBe('—');
		expect(formatDuration('0001-01-01T00:00:00Z', '2026-10-18T12:00:00Z')).toBe('—');
	});
});

describe('job statuses', () => {
	it('tells active states apart and labels them in English', () => {
		expect(isActive('healthchecking')).toBe(true);
		expect(isActive('rolled_back')).toBe(false);
		expect(jobStatusLabels.rolled_back).toBe('Rolled back');
	});
});
