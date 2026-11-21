import test from "node:test";
import assert from "node:assert/strict";
import { renderDescription, DESCRIPTION_BYTES_MAX } from "../src/markdown.ts";

test("descriptions render Markdown formatting without enabling embedded HTML or images", () => {
  const output = renderDescription('# Heading\n\n**Bold** and *emphasis*\n\n- Item\n\n```go\n<script>literal</script>\n```\n\n[label](https://example.test)\n\n![image](https://example.test/image.png)\n<script>window.untrustedExecuted=true</script>');
  assert.match(output, /<h1>Heading<\/h1>/);
  assert.match(output, /<strong>Bold<\/strong>/);
  assert.match(output, /<em>emphasis<\/em>/);
  assert.match(output, /<li>Item<\/li>/);
  assert.match(output, /<code class="language-go">&lt;script&gt;literal&lt;\/script&gt;/);
  assert.match(output, /href="https:\/\/example.test"/);
  assert.doesNotMatch(output, /<(?:script|img|iframe|svg)\b/i);
});

test("only a ticket's own attachment paths render as images", () => {
  const own = "/api/projects/1/tickets/2/attachments/3";
  const rendered = renderDescription(`![shot](${own})`, [own]);
  assert.match(rendered, /<img src="\/api\/projects\/1\/tickets\/2\/attachments\/3"/);
  assert.match(rendered, /alt="shot"/);
  assert.match(rendered, /loading="lazy"/);
  assert.doesNotMatch(renderDescription(`![shot](${own})`), /<img/, "images need an explicit allow-list");
  for (const source of [`![x](/api/projects/1/tickets/2/attachments/9)`, `![x](https://evil.test${own})`, "![x](data:image/png;base64,eA==)", `![x](javascript:alert(1))`]) {
    assert.doesNotMatch(renderDescription(source, [own]), /<img/, source);
  }
  assert.doesNotMatch(renderDescription(`<img src="${own}">`, [own]), /<img/, "raw HTML stays escaped");
  assert.doesNotMatch(renderDescription(`![a" onerror="alert(1)](${own})`, [own]), /onerror="/);
});

test("Markdown rejects active URL schemes and escapes HTML, attributes, and code fences", () => {
  for (const source of [
    '[x](javascript:alert(1))', '[x](JaVaScRiPt:alert(1))', '[x](vbscript:msgbox(1))',
    '[x](java&#x73;cript:alert(1))', '[x](data:text/html,evil)', '[x](data:image/png;base64,eA==)',
    '<svg onload="alert(1)">', '<img src=x onerror="alert(1)">', '<iframe srcdoc="evil"></iframe>',
    '```" onmouseover="alert(1)\n<script>alert(1)</script>\n```',
  ]) {
    const output = renderDescription(source);
    assert.doesNotMatch(output, /<(?:script|img|iframe|svg)\b/i, source);
    assert.doesNotMatch(output, /href="(?:javascript|vbscript|data):/i, source);
    assert.doesNotMatch(output, /\s(?:onload|onerror|onmouseover)="/i, source);
  }
  assert.doesNotMatch(renderDescription('https://example.test test@example.test'), /<a\b/);
  assert.match(renderDescription('"plain quotes"'), /&quot;plain quotes&quot;/);
});

test("description parsing is bounded in UTF-8 bytes and keeps oversized legacy content intact", { timeout: 5000 }, () => {
  const source = '**' + 'x'.repeat(DESCRIPTION_BYTES_MAX) + '**\n<script>literal</script>';
  const output = renderDescription(source);
  assert.match(output, /class="markdown-plain"/);
  assert.doesNotMatch(output, /<strong>|<script>/);
  assert.ok(output.includes('x'.repeat(DESCRIPTION_BYTES_MAX)));
  assert.ok(output.includes('\n&lt;script&gt;literal&lt;/script&gt;'));
  assert.match(renderDescription('界'.repeat(DESCRIPTION_BYTES_MAX)), /class="markdown-plain"/);
  for (const source of ['[a]: /u "' + 'x\n'.repeat(4000), 'x ' + '<!--'.repeat(3000), '"a '.repeat(2000), 'a://'.repeat(3000)]) {
    assert.equal(typeof renderDescription(source), "string");
  }
});
