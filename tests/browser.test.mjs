import test from "node:test";
import assert from "node:assert/strict";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { once } from "node:events";

const execute = promisify(execFile);
const commandTimeoutMs = 60000;
const requestTimeoutMs = 5000;
const outputBytesMax = 1048576;

async function startServer(database) {
  const child = spawn(resolve("bin/docket"), ["--database", database, "--listen", "127.0.0.1:0"], { stdio: ["ignore", "ignore", "pipe"] });
  let output = "";
  let failure;
  child.on("error", (error) => { failure = error; });
  child.stderr.on("data", (data) => { output = (output + data.toString()).slice(-65536); });
  for (let i = 0; i < 100; i++) {
    if (failure) throw failure;
    const match = output.match(/url=(http:\/\/127\.0\.0\.1:\d+)/);
    if (match) return { child, origin: match[1] };
    if (child.exitCode !== null) throw new Error(`Server exited before readiness: ${output}`);
    await delay(50);
  }
  child.kill("SIGKILL");
  throw new Error(`Server did not start: ${output}`);
}

async function stopServer(server, signal = "SIGTERM") {
  if (server.child.exitCode !== null || server.child.signalCode !== null) return;
  const exited = once(server.child, "exit", { signal: AbortSignal.timeout(requestTimeoutMs) });
  server.child.kill(signal);
  try { await exited; } catch (error) { server.child.kill("SIGKILL"); throw error; }
}

async function request(origin, path, method = "GET", body, version) {
  const response = await fetch(origin + path, { method, headers: { "Content-Type": "application/json", ...(version ? { "If-Match": `"${version}"` } : {}) }, body: body ? JSON.stringify(body) : undefined, signal: AbortSignal.timeout(requestTimeoutMs) });
  const value = await response.json();
  assert.ok(response.ok, `${method} ${path}: ${response.status} ${JSON.stringify(value)}`);
  return value;
}

test("real browser: forms, draft preservation, navigation races, conflicts, responsive UI", { timeout: 180000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-browser-"));
  const session = `docket-test-${process.pid}`;
  const server = await startServer(join(directory, "test.sqlite"));
  const browser = async (...arguments_) => (await execute("agent-browser", ["--session", session, ...arguments_], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax })).stdout;
  try {
    await browser("open", server.origin);
    assert.match(await browser("eval", `(async () => {
      await document.fonts.ready;
      if (![...document.fonts].some(face => face.family === 'Manrope' && face.status === 'loaded')) throw new Error('Bundled Manrope font failed to load');
      if (!getComputedStyle(document.body).fontFamily.startsWith('Manrope')) throw new Error('Interface font not applied');
      if (document.querySelector('script[type="module"]')?.getAttribute('src') !== '/assets/app.js') throw new Error('Vite module entry missing');
      if (document.querySelector('.icon use').getBBox().width <= 0) throw new Error('SVG sprite rendered empty');
      return 'PASS';
    })()`), /PASS/);
    const pageResponse = await fetch(server.origin + '/projects/1', { signal: AbortSignal.timeout(requestTimeoutMs) });
    assert.equal(pageResponse.status, 200);
    const policy = pageResponse.headers.get('content-security-policy');
    assert.match(policy, /script-src 'self'/);
    assert.doesNotMatch(policy, /unsafe-inline|unsafe-eval/);
    assert.match(await browser("eval", `(() => {
      for (const selector of ['#add-project', '#sidebar-new', '#welcome-new', '#board-view', '#list-view', '#refresh', '.search-label']) {
        const icon = document.querySelector(selector).querySelector('svg');
        if (!icon || icon.getAttribute('aria-hidden') !== 'true') throw new Error('Missing decorative SVG: ' + selector);
        const bounds = icon.getBoundingClientRect();
        if (selector === '#add-project' && (bounds.width !== 16 || bounds.height !== 16)) throw new Error('Unexpected icon size');
      }
      return 'PASS';
    })()`), /PASS/);
    const sprite = await fetch(server.origin + '/assets/icons.svg', { signal: AbortSignal.timeout(requestTimeoutMs) });
    assert.equal(sprite.status, 200);
    assert.match(sprite.headers.get('content-type'), /image\/svg\+xml/);
    assert.match(await sprite.text(), /id="plus"/);
    await browser("click", "#welcome-new");
    await browser("fill", "#project-title", "Website");
    assert.match(await browser("eval", "document.getElementById('project-key').value"), /WEBSI/);
    await browser("fill", "#project-key", "WEB");
    await browser("click", "#save-project");
    await browser("wait", "--fn", "location.pathname === '/projects/1'");
    await browser("click", "#new-ticket");
    await browser("fill", "#ticket-title", "<img src=x onerror=window.untrustedExecuted=true>");
    await browser("fill", "#ticket-description", "<script>window.untrustedExecuted=true</script>");
    await browser("click", "#save-ticket");
    await browser("wait", ".ticket-card");
    assert.match(await browser("eval", `(() => {
      const card = document.querySelector('.ticket-card');
      if (card.querySelectorAll('svg[aria-hidden="true"]').length !== 2) throw new Error('Ticket icons missing');
      if (card.getAttribute('aria-label') !== 'WEB-1: <img src=x onerror=window.untrustedExecuted=true>') throw new Error('Ticket accessible name changed');
      return 'PASS';
    })()`), /PASS/);
    await browser("click", ".ticket-card");
    await browser("fill", "#comment-body", "Unsent review notes");
    await browser("fill", "#ticket-title", "Ship the home page");
    await browser("click", "#save-ticket");
    await browser("wait", "--fn", "!document.getElementById('save-ticket').disabled");
    assert.match(await browser("eval", `(() => {
      if (!document.getElementById('ticket-dialog').open) throw new Error('Ticket save closed editor');
      if (document.getElementById('comment-body').value !== 'Unsent review notes') throw new Error('Comment draft was erased');
      if (window.untrustedExecuted) throw new Error('Stored text executed');
      return 'PASS';
    })()`), /PASS/);
    await browser("click", "#post-comment");
    await browser("wait", "--fn", "document.querySelector('.comment-body')?.textContent === 'Unsent review notes'");
    await browser("select", "#ticket-status", "In Progress");
    await browser("click", "#save-ticket");
    await browser("wait", "--fn", "!document.getElementById('save-ticket').disabled");
    await browser("fill", "#ticket-description", "Keep typography restrained. Add a short introduction and a clear link to recent work.");
    await browser("click", "#save-ticket");
    await browser("wait", "--fn", "!document.getElementById('save-ticket').disabled");
    await browser("click", "#close-ticket");
    await browser("click", "#list-view");
    await browser("wait", ".list-table");
    await browser("click", "#board-view");
    const second = await request(server.origin, "/api/projects", "POST", { name: "Release notes", key: "NOTES" });
    assert.equal(second.id, 2);
    await browser("click", "#refresh");
    await browser("wait", "--fn", "document.querySelectorAll('.project-link').length === 2");
    // Delay a real fetch, rather than mocking application responses or routing.
    await browser("eval", `window.originalFetch = window.fetch; window.releaseDelayed = null;
      window.fetch = (input, options) => input === '/api/projects/1'
        ? new Promise(resolve => { window.releaseDelayed = () => resolve(window.originalFetch(input, options)); })
        : window.originalFetch(input, options);
      document.querySelectorAll('.project-link')[0].click(); true`);
    await browser("wait", "--fn", "window.releaseDelayed !== null");
    await browser("eval", "document.querySelectorAll('.project-link')[1].click(); true");
    await browser("wait", "--fn", "location.pathname === '/projects/2'");
    await browser("eval", "window.fetch = window.originalFetch; window.releaseDelayed(); true");
    await browser("wait", "--load", "networkidle");
    assert.match(await browser("eval", `(() => {
      if (location.pathname !== '/projects/2' || document.getElementById('project-name').textContent !== 'Release notes') throw new Error('Older navigation won');
      return 'PASS';
    })()`), /PASS/);
    await browser("eval", `window.originalFetch = window.fetch; window.releaseDelayed = null;
      window.fetch = (input, options) => input === '/api/projects/2' && (!options.method || options.method === 'GET')
        ? new Promise(resolve => { window.releaseDelayed = () => resolve(window.originalFetch(input, options)); })
        : window.originalFetch(input, options);
      document.getElementById('refresh').click(); true`);
    await browser("wait", "--fn", "window.releaseDelayed !== null");
    await browser("click", "#project-settings");
    await request(server.origin, "/api/projects/2", "PATCH", { name: "Authoritative release notes", statuses: second.statuses }, second.version);
    await browser("eval", "window.fetch = window.originalFetch; window.releaseDelayed(); true");
    await browser("wait", "--fn", "document.getElementById('project-name').textContent === 'Authoritative release notes'");
    await browser("click", "#save-project");
    await browser("wait", "--fn", "!document.getElementById('save-project').disabled");
    assert.match(await browser("eval", `(() => {
      if (!document.getElementById('project-error').textContent.includes('Changed since')) throw new Error('Stale project form was accepted');
      return 'PASS';
    })()`), /PASS/);
    assert.equal((await request(server.origin, "/api/projects/2")).name, "Authoritative release notes");
    await browser("click", "#cancel-project");
    await browser("open", `${server.origin}/projects/1`);
    const screenshots = process.env.DOCKET_SCREENSHOTS;
    if (screenshots) {
      await mkdir(screenshots, { recursive: true });
      await browser("set", "viewport", "1440", "1000");
      await browser("screenshot", join(screenshots, "board-desktop.png"));
      await browser("click", ".ticket-card");
      await browser("screenshot", join(screenshots, "ticket-desktop.png"));
      await browser("set", "viewport", "390", "844");
      await browser("screenshot", join(screenshots, "ticket-phone.png"));
      await browser("click", "#close-ticket");
      await browser("screenshot", join(screenshots, "board-phone.png"));
    }
    await browser("set", "viewport", "390", "844");
    assert.match(await browser("eval", `(() => {
      if (!document.getElementById('add-project').getClientRects().length) throw new Error('New project action is hidden on phone widths');
      if (getComputedStyle(document.getElementById('sidebar-new')).display !== 'none') throw new Error('Redundant sidebar action should be hidden on phone widths');
      return 'PASS';
    })()`), /PASS/);
    await browser("click", "#add-project");
    await browser("fill", "#project-title", "Recovery check");
    await browser("fill", "#project-key", "RECOVER");
    await browser("eval", `window.originalFetch = window.fetch;
      window.fetch = (input, options) => {
        if (input.startsWith('/api/projects?after=')) { window.fetch = window.originalFetch; return Promise.reject(new TypeError('Simulated list refresh failure')); }
        return window.originalFetch(input, options);
      }; true`);
    await browser("click", "#save-project");
    await browser("wait", "--fn", "!document.getElementById('save-project').disabled");
    assert.match(await browser("eval", `(() => {
      const notice = document.getElementById('notification');
      if (notice.hidden || !notice.textContent.includes('Simulated list refresh failure')) throw new Error('Refresh failure was hidden');
      return 'PASS';
    })()`), /PASS/);
    const projects = await request(server.origin, '/api/projects');
    assert.ok(projects.items.some((project) => project.key === 'RECOVER'), 'The write must have committed before the refresh failed');
  } finally {
    try { await browser("close"); } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
  }
});

test("real process: replay survives SIGKILL and a consistent backup", { timeout: 30000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-restart-"));
  const database = join(directory, "test.sqlite");
  const backup = join(directory, "backup.sqlite");
  let server = await startServer(database);
  try {
    const project = await request(server.origin, "/api/projects", "POST", { name: "Persistence", key: "KEEP" });
    const ticket = await request(server.origin, `/api/projects/${project.id}/tickets`, "POST", { title: "Survive process loss" });
    const feed = `/api/projects/${project.id}/events?after=1`;
    const expected = await request(server.origin, feed);
    assert.equal(expected.items.length, 1);
    assert.equal(expected.items[0].data.id, ticket.id);
    await stopServer(server, "SIGKILL");
    server = await startServer(database);
    assert.deepEqual(await request(server.origin, feed), expected);
    await execute(resolve("bin/docket"), ["--database", database, "--backup-to", backup], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax });
    await stopServer(server);
    server = await startServer(backup);
    assert.deepEqual(await request(server.origin, feed), expected);
  } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
});

test("workflow editor: preview, inline rows, ordering, bounds, validation, and persistence", { timeout: 180000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-workflow-"));
  const session = `docket-workflow-${process.pid}`;
  const server = await startServer(join(directory, "test.sqlite"));
  const browser = async (...arguments_) => (await execute("agent-browser", ["--session", session, ...arguments_], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax })).stdout;
  const check = async (expression, message) => {
    assert.match(await browser("eval", `(() => { if (!(${expression})) throw new Error(${JSON.stringify(message)}); return 'PASS'; })()`), /PASS/);
  };
  try {
    await browser("open", server.origin);
    await browser("click", "#welcome-new");
    await check("document.querySelectorAll('.status-chip').length === 5", "New projects should show five compact default status chips");
    await check("document.getElementById('workflow-editor').hidden", "New-project workflow editor should start collapsed");
    await browser("fill", "#project-title", "Workflow test");
    await browser("fill", "#project-key", "FLOW");
    const screenshots = process.env.DOCKET_SCREENSHOTS;
    if (screenshots) {
      await mkdir(screenshots, { recursive: true });
      await browser("set", "viewport", "1440", "1000");
      await browser("screenshot", join(screenshots, "workflow-new-desktop.png"));
    }
    await browser("set", "viewport", "1280", "720");
    await browser("click", "#workflow-toggle");
    await check("document.querySelectorAll('.status-name').length === 5 && !document.getElementById('workflow-editor').hidden", "Customize should reveal inline status rows");
    await check("document.querySelector('.status-up').disabled && document.querySelector('.status-row:last-child .status-down').disabled", "Boundary reorder controls should be disabled");
    await browser("fill", ".status-row:first-child .status-name", "Ideas");
    await browser("click", ".status-row:first-child .status-down");
    await check("document.querySelectorAll('.status-name')[1].value === 'Ideas'", "Moving a status must retain its edited name");
    await browser("click", ".status-row:nth-child(2) .status-up");
    await check("document.querySelector('.status-name').value === 'Ideas'", "Moving a status up must retain its edited name");
    await browser("click", ".status-row:first-child .status-down");
    await browser("fill", ".status-row:first-child .status-name", "In Review");
    await browser("click", "#workflow-toggle");
    await browser("click", "#save-project");
    await check("document.getElementById('status-error').textContent.includes('unique')", "Duplicate status names should be rejected inline");
    await check("!document.getElementById('workflow-editor').hidden && document.activeElement.classList.contains('status-name')", "Validation must reveal and focus invalid names in a collapsed editor");
    assert.equal((await request(server.origin, "/api/projects")).items.length, 0);
    await browser("fill", ".status-row:first-child .status-name", "");
    await browser("click", "#save-project");
    await check("document.getElementById('status-error').textContent.includes('name')", "Blank status names should be rejected inline");
    await browser("fill", ".status-row:first-child .status-name", "😀".repeat(26));
    await browser("click", "#save-project");
    await check("document.getElementById('status-error').textContent.includes('100')", "Status names must respect the API byte limit");
    await browser("fill", ".status-row:first-child .status-name", "Queued");
    await browser("click", ".status-row:last-child .status-remove");
    await browser("wait", "--fn", "document.querySelectorAll('.status-row').length === 4");
    await browser("click", "#add-status");
    await browser("wait", "--fn", "document.querySelectorAll('.status-row').length === 5");
    await browser("fill", ".status-row:last-child .status-name", "Shipped");
    for (let i = 5; i < 12; i++) {
      await browser("click", "#add-status");
      await browser("wait", "--fn", `document.querySelectorAll('.status-row').length === ${i + 1}`);
    }
    await check("document.querySelectorAll('.status-row').length === 12 && document.getElementById('add-status').disabled", "Workflow must stop at twelve statuses");
    await check("(() => { const button = document.getElementById('add-status').getBoundingClientRect(); const dialog = document.getElementById('project-dialog').getBoundingClientRect(); return button.top >= Math.max(0, dialog.top) && button.bottom <= Math.min(innerHeight, dialog.bottom); })()", "Workflow actions must remain visible at the status limit in a short viewport");
    for (let i = 12; i > 5; i--) {
      await browser("click", ".status-row:last-child .status-remove");
      await browser("wait", "--fn", `document.querySelectorAll('.status-row').length === ${i - 1}`);
    }
    await browser("click", "#workflow-toggle");
    await check("[...document.querySelectorAll('.status-chip')].map(node => node.textContent).join('|') === 'Queued|Ideas|Blocked|In Review|Shipped'", "Collapsed preview must preserve custom order and names");
    await browser("click", "#workflow-toggle");
    if (screenshots) {
      await browser("set", "viewport", "1440", "1000");
      await browser("screenshot", join(screenshots, "workflow-editor-desktop.png"));
      await browser("set", "viewport", "390", "844");
      await browser("screenshot", join(screenshots, "workflow-editor-phone.png"));
    }
    await browser("click", "#save-project");
    await browser("wait", "--fn", "location.pathname === '/projects/1'");
    const expected = ["Queued", "Ideas", "Blocked", "In Review", "Shipped"];
    assert.deepEqual((await request(server.origin, "/api/projects/1")).statuses, expected);
    await request(server.origin, "/api/projects/1/tickets", "POST", { title: "Keep this status in use", status: "Queued" });
    await browser("click", "#project-settings");
    await check("!document.getElementById('workflow-editor').hidden && document.querySelector('.status-name').value === 'Queued'", "Project settings should open the saved editor directly");
    await browser("fill", ".status-row:first-child .status-name", "Removed name");
    await browser("click", "#save-project");
    await browser("wait", "--fn", "!document.getElementById('save-project').disabled");
    await check("document.getElementById('project-error').textContent.includes('Move tickets')", "In-use status protection must remain enforced by the API");
    assert.deepEqual((await request(server.origin, "/api/projects/1")).statuses, expected);
    await browser("fill", ".status-row:first-child .status-name", "Queued");
    for (let i = 5; i > 1; i--) {
      await browser("click", ".status-row:last-child .status-remove");
      await browser("wait", "--fn", `document.querySelectorAll('.status-row').length === ${i - 1}`);
    }
    await check("document.querySelector('.status-remove').disabled && document.querySelectorAll('.status-row').length === 1", "The final status must not be removable");
    await browser("click", "#save-project");
    await browser("wait", "--fn", "!document.getElementById('project-dialog').open");
    assert.deepEqual((await request(server.origin, "/api/projects/1")).statuses, ["Queued"]);
    await browser("click", "#add-project");
    await check("document.getElementById('workflow-editor').hidden && document.querySelectorAll('.status-chip').length === 5", "A fresh project must reset custom drafts to the defaults");
  } catch (error) {
    const diagnostics = await browser("eval", "JSON.stringify({rows:document.querySelectorAll('.status-row').length,addDisabled:document.getElementById('add-status').disabled,editorHidden:document.getElementById('workflow-editor').hidden,dialogOpen:document.getElementById('project-dialog').open})").catch(() => "Browser diagnostics unavailable");
    console.error(diagnostics);
    if (process.env.DOCKET_SCREENSHOTS) await browser("screenshot", join(process.env.DOCKET_SCREENSHOTS, "workflow-failure.png")).catch(() => console.error("Failure screenshot unavailable"));
    throw error;
  } finally {
    try { await browser("close"); } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
  }
});

test("Svelte ticket panel: stale writes require reload and keep the authoritative ticket", { timeout: 30000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-ticket-conflict-"));
  const session = `docket-conflict-${process.pid}`;
  const server = await startServer(join(directory, "test.sqlite"));
  const browser = async (...arguments_) => (await execute("agent-browser", ["--session", session, ...arguments_], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax })).stdout;
  try {
    const project = await request(server.origin, "/api/projects", "POST", { name: "Conflicts", key: "LOCK" });
    const ticket = await request(server.origin, `/api/projects/${project.id}/tickets`, "POST", { title: "Initial title" });
    const path = `/api/projects/${project.id}/tickets/${ticket.id}`;
    await browser("open", `${server.origin}/projects/${project.id}`);
    await browser("click", ".ticket-card");
    await browser("wait", "--fn", "document.getElementById('ticket-dialog').open");
    const authoritative = await request(server.origin, path, "PATCH", { title: "Authoritative title" }, ticket.version);
    await browser("click", "#save-ticket");
    await browser("wait", "--fn", "document.getElementById('ticket-error').textContent.includes('Changed since')");
    assert.match(await browser("eval", `(() => {
      if (document.getElementById('reload-ticket').hidden) throw new Error('Conflict reload action is missing');
      if (!document.getElementById('ticket-dialog').open) throw new Error('Conflict closed the ticket panel');
      return 'PASS';
    })()`), /PASS/);
    assert.deepEqual(await request(server.origin, path), authoritative);
    await browser("click", "#reload-ticket");
    await browser("wait", "--fn", "document.getElementById('ticket-title').value === 'Authoritative title'");
    await browser("fill", "#comment-body", "Reviewed after reload");
    await browser("click", "#post-comment");
    await browser("wait", "--fn", "document.querySelector('.comment-body')?.textContent === 'Reviewed after reload'");
    assert.equal((await request(server.origin, path)).version, authoritative.version + 1);
  } finally {
    try { await browser("close"); } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
  }
});
