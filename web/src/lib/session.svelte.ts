import { api } from './api';
import type { Me } from './types';

/** The logged-in user, loaded once by the layout. */
export const session = $state<{ me: Me | null }>({ me: null });

export async function loadSession(): Promise<void> {
	try {
		session.me = await api.me();
	} catch {
		session.me = null; // 401 already redirected to the login page
	}
}
