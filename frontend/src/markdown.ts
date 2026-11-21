import MarkdownIt from "markdown-it/browser";

export const DESCRIPTION_BYTES_MAX = 16000;
const encoder = new TextEncoder();
const renderer = new MarkdownIt({ html: false, linkify: false, typographer: false });
const validateLink = renderer.validateLink.bind(renderer);
renderer.validateLink = (value: string): boolean => !/^data:/i.test(value) && validateLink(value);

// Images are opt-in per ticket: only the ticket's own attachment paths render, so a
// description cannot turn into a request for third-party content. Everything else
// degrades to its alt text instead of silently disappearing.
renderer.renderer.rules.image = (tokens, index, options, environment, self) => {
  const token = tokens[index];
  const source = String(token.attrGet("src") ?? "");
  const allowed: string[] = (environment as { attachments?: string[] } | undefined)?.attachments ?? [];
  if (!source.startsWith("/api/") || !allowed.includes(source)) return renderer.utils.escapeHtml(token.content);
  token.attrSet("src", source);
  token.attrSet("alt", token.content);
  token.attrSet("loading", "lazy");
  return self.renderToken(tokens, index, options);
};

export function renderDescription(source: string, attachments: string[] = []): string {
  try {
    if (encoder.encode(source).byteLength <= DESCRIPTION_BYTES_MAX) return renderer.render(source, { attachments });
  } catch {
    // A parser failure must not lose stored text or make the rest of the ticket inaccessible.
  }
  return `<div class="markdown-plain">${renderer.utils.escapeHtml(source)}</div>`;
}
