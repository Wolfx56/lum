// Live logs of a job: GET /api/v1/jobs/{id}/logs as Server-Sent Events.
// EventSource sends the session cookie and, after a network cut, reconnects by
// itself with Last-Event-ID: the server resumes after the last line received.
import type { Job, JobLog } from './types';

export type StreamState = 'connecting' | 'live' | 'reconnecting' | 'ended';

export interface StreamHandlers {
	onLog: (line: JobLog) => void;
	onStatus: (job: Job) => void;
	onEnd: (job: Job) => void;
	onState: (state: StreamState) => void;
}

/** Opens the stream after line `after`; returns the function that closes it. */
export function streamJobLogs(id: string, after: number, h: StreamHandlers): () => void {
	const url = `/api/v1/jobs/${encodeURIComponent(id)}/logs` + (after > 0 ? `?after=${after}` : '');
	const es = new EventSource(url);
	h.onState('connecting');
	es.onopen = () => h.onState('live');
	es.onerror = () => {
		if (es.readyState !== EventSource.CLOSED) h.onState('reconnecting');
	};
	es.addEventListener('log', (e) => h.onLog(JSON.parse((e as MessageEvent).data)));
	es.addEventListener('status', (e) => h.onStatus(JSON.parse((e as MessageEvent).data)));
	es.addEventListener('end', (e) => {
		es.close();
		h.onState('ended');
		h.onEnd(JSON.parse((e as MessageEvent).data));
	});
	return () => es.close();
}
