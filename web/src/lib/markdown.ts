import DOMPurify from 'dompurify';
import { marked } from 'marked';

// Links of release notes open in a new tab without access to this page.
DOMPurify.addHook('afterSanitizeAttributes', (node) => {
	if (node.tagName === 'A') {
		node.setAttribute('target', '_blank');
		node.setAttribute('rel', 'noopener noreferrer');
	}
});

/** Renders GitHub release notes (untrusted markdown) to sanitized HTML. */
export function renderMarkdown(md: string): string {
	const html = marked.parse(md, { async: false, gfm: true });
	return DOMPurify.sanitize(html, { USE_PROFILES: { html: true } });
}
