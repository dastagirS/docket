import MarkdownIt from "markdown-it/browser";

export const DESCRIPTION_BYTES_MAX = 16000;
const encoder = new TextEncoder();
const renderer = new MarkdownIt({ html: false, linkify: false, typographer: false }).disable("image");
const validateLink = renderer.validateLink.bind(renderer);
renderer.validateLink = (value: string): boolean => !/^data:/i.test(value) && validateLink(value);

export function renderDescription(source: string): string {
  try {
    if (encoder.encode(source).byteLength <= DESCRIPTION_BYTES_MAX) return renderer.render(source);
  } catch {
    // A parser failure must not lose stored text or make the rest of the ticket inaccessible.
  }
  return `<div class="markdown-plain">${renderer.utils.escapeHtml(source)}</div>`;
}
