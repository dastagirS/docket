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
    await check("document.querySelectorAll('.status-default').length === 1 && document.querySelector('.status-row:first-child .status-default')?.textContent === 'Default'", "The first status should have one clear default indicator");
    await check("document.getElementById('workflow-default-help').textContent.includes('New tickets') && document.querySelector('.status-name').getAttribute('aria-describedby').includes('workflow-default-help')", "The default status should explain where new tickets start");
    await check("document.querySelector('.workflow-heading #status-count').textContent === '5 / 12'", "Status capacity should be visible in the section heading");
    await check("document.getElementById('workflow-toggle').textContent.includes('Collapse')", "Expanded workflow should have a clear collapse action");
    await check("document.querySelector('.status-up').disabled && document.querySelector('.status-row:last-child .status-down').disabled", "Boundary reorder controls should be disabled");
    await browser("fill", ".status-row:first-child .status-name", "Ideas");
    await browser("click", ".status-row:first-child .status-down");
    await check("document.querySelectorAll('.status-name')[1].value === 'Ideas'", "Moving a status must retain its edited name");
    await check("document.querySelector('.status-row:first-child .status-name').value === 'In Progress' && document.querySelector('.status-row:first-child .status-default')?.textContent === 'Default'", "Default indicator should follow the first position after reordering");
    await browser("click", ".status-row:nth-child(2) .status-up");
    await check("document.querySelector('.status-name').value === 'Ideas'", "Moving a status up must retain its edited name");
    await browser("eval", "document.querySelector('.status-row:first-child .status-down').focus(); true");
    await browser("press", "Enter");
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
    const defaultTicket = await request(server.origin, "/api/projects/1/tickets", "POST", { title: "Keep this status in use" });
    assert.equal(defaultTicket.status, expected[0], "New tickets should start in the first saved status");
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

async function grantClipboardRead(browser, origin) {
  const connection = JSON.parse(await browser("get", "cdp-url", "--json"));
  assert.equal(connection.success, true);
  assert.ok(connection.data.cdpUrl.startsWith("ws://127.0.0.1:"));
  const socket = new WebSocket(connection.data.cdpUrl);
  try {
    await once(socket, "open", { signal: AbortSignal.timeout(requestTimeoutMs) });
    const command = async (id, method, parameters) => {
      const received = once(socket, "message", { signal: AbortSignal.timeout(requestTimeoutMs) });
      socket.send(JSON.stringify({ id, method, params: parameters }));
      const [message] = await received;
      const response = JSON.parse(message.data);
      assert.equal(response.id, id);
      assert.equal(response.error, undefined);
      return response.result;
    };
    const targets = await command(1, "Target.getTargets", {});
    const target = targets.targetInfos.find((item) => item.url.startsWith(origin + "/"));
    assert.ok(target, "Disposable application tab must exist");
    await command(2, "Browser.grantPermissions", { origin, permissions: ["clipboardReadWrite", "clipboardSanitizedWrite"], ...(target.browserContextId ? { browserContextId: target.browserContextId } : {}) });
    // Chromium removes this permission override when its CDP connection closes.
    return socket;
  } catch (error) { socket.close(); throw error; }
}

test("project actions: copy stable URL, delete-to-archive from settings, and restore", { timeout: 45000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-project-actions-"));
  const session = `docket-actions-${process.pid}`;
  let clipboardConnection;
  const server = await startServer(join(directory, "test.sqlite"));
  const browser = async (...arguments_) => (await execute("agent-browser", ["--session", session, ...arguments_], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax })).stdout;
  try {
    const project = await request(server.origin, "/api/projects", "POST", { name: "Keep this project", key: "KEEP" });
    const ticket = await request(server.origin, `/api/projects/${project.id}/tickets`, "POST", { title: "Keep this ticket" });
    const commentsPath = `/api/projects/${project.id}/tickets/${ticket.id}/comments`;
    await request(server.origin, commentsPath, "POST", { body: "Keep this discussion" }, ticket.version);
    const comments = await request(server.origin, commentsPath);
    const url = `${server.origin}/projects/${project.id}`;
    await browser("open", url);
    assert.match(await browser("eval", `(() => {
      if (!document.getElementById('copy-project-url')) throw new Error('Copy project URL action is missing');
      if (document.querySelector('.header-actions #archive-project')) throw new Error('Project deletion should be in settings, not the header');
      return 'PASS';
    })()`), /PASS/);
    clipboardConnection = await grantClipboardRead(browser, server.origin);
    await browser("click", "#copy-project-url");
    assert.ok((await browser("clipboard", "read")).includes(url), "Clipboard should contain the stable project URL");
    await browser("eval", "Object.defineProperty(navigator, 'clipboard', { configurable: true, value: { writeText: async () => { throw new Error('Clipboard unavailable'); } } }); true");
    await browser("click", "#copy-project-url");
    await browser("wait", "--fn", "document.getElementById('copy-link-dialog').open");
    assert.match(await browser("eval", "document.getElementById('project-url').value"), new RegExp(url.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));
    await browser("click", "#close-copy-link");
    await browser("click", "#project-settings");
    await browser("eval", "window.confirm = () => false; true");
    await browser("click", "#archive-project");
    assert.equal((await request(server.origin, `/api/projects/${project.id}`)).archived, false);
    await browser("eval", "window.confirm = () => true; true");
    await browser("click", "#archive-project");
    await browser("wait", "--fn", "!document.getElementById('archived-banner').hidden");
    assert.equal((await request(server.origin, `/api/projects/${project.id}`)).archived, true);
    assert.deepEqual(await request(server.origin, commentsPath), comments);
    await browser("click", "#active-projects");
    await browser("wait", "--fn", "document.querySelectorAll('.project-link').length === 0");
    await browser("click", "#archived-projects");
    await browser("wait", "--fn", "document.querySelectorAll('.project-link').length === 1");
    await browser("open", url);
    await browser("wait", "--fn", "!document.getElementById('archived-banner').hidden");
    await browser("click", "#project-settings");
    await browser("click", "#archive-project");
    await browser("wait", "--fn", "document.getElementById('archived-banner').hidden");
    const restored = await request(server.origin, `/api/projects/${project.id}`);
    assert.equal(restored.archived, false);
    assert.equal(restored.id, project.id);
    assert.equal(restored.key, project.key);
    assert.equal((await request(server.origin, "/api/projects")).items.length, 1);
    assert.deepEqual(await request(server.origin, commentsPath), comments);
    await browser("set", "viewport", "390", "844");
    assert.match(await browser("eval", `(() => {
      for (const id of ['copy-project-url', 'project-settings', 'archived-projects']) {
        const bounds = document.getElementById(id).getBoundingClientRect();
        if (bounds.width <= 0 || bounds.left < 0 || bounds.right > innerWidth) throw new Error('Project action is offscreen: ' + id);
      }
      return 'PASS';
    })()`), /PASS/);
  } finally {
    clipboardConnection?.close();
    try { await browser("close"); } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
  }
});

test("live updates: external changes sync without reload or losing drafts and pagination", { timeout: 60000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-live-"));
  const session = `docket-live-${process.pid}`;
  const server = await startServer(join(directory, "test.sqlite"));
  const browser = async (...arguments_) => (await execute("agent-browser", ["--session", session, ...arguments_], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax })).stdout;
  try {
    const project = await request(server.origin, "/api/projects", "POST", { name: "Live board", key: "LIVE" });
    const ticket = await request(server.origin, `/api/projects/${project.id}/tickets`, "POST", { title: "Initial title" });
    for (let i = 0; i < 100; i++) await request(server.origin, `/api/projects/${project.id}/tickets`, "POST", { title: `Other ticket ${i}` });
    const path = `/api/projects/${project.id}/tickets/${ticket.id}`;
    await browser("open", `${server.origin}/projects/${project.id}`);
    await browser("wait", ".ticket-card");
    await browser("eval", "window.liveDocumentMarker = 'same-document'; true");
    await browser("click", "#load-tickets");
    await browser("wait", "--fn", "document.querySelectorAll('.ticket-card').length === 101");
    await request(server.origin, path, "PATCH", { title: "Updated from API" }, ticket.version);
    await browser("wait", "--fn", "document.querySelector('.card-title')?.textContent === 'Updated from API'");
    assert.match(await browser("eval", "document.querySelectorAll('.ticket-card').length"), /101/);
    await browser("click", ".ticket-card");
    await browser("wait", "--fn", "document.getElementById('ticket-dialog').open");
    const current = await request(server.origin, path);
    await request(server.origin, path, "PATCH", { description: "Fresh description" }, current.version);
    await browser("wait", "--fn", "document.getElementById('ticket-description').value === 'Fresh description'");
    await browser("fill", "#ticket-title", "Unsaved local title");
    await browser("fill", "#comment-body", "Unsent local comment");
    const latest = await request(server.origin, path);
    const changed = await request(server.origin, path, "PATCH", { title: "Authoritative remote title" }, latest.version);
    await request(server.origin, path + "/comments", "POST", { body: "Remote discussion update" }, changed.version);
    await browser("wait", "--fn", "document.querySelector('.comment-body')?.textContent === 'Remote discussion update'");
    assert.match(await browser("eval", `(() => {
      if (document.getElementById('ticket-title').value !== 'Unsaved local title') throw new Error('Live update erased ticket draft');
      if (document.getElementById('comment-body').value !== 'Unsent local comment') throw new Error('Live update erased comment draft');
      if (document.getElementById('reload-ticket').hidden) throw new Error('Changed draft needs a reload action');
      if (document.querySelectorAll('.ticket-card').length !== 101) throw new Error('Live update reset pagination');
      return 'PASS';
    })()`), /PASS/);
    await browser("click", "#save-ticket");
    await browser("wait", "--fn", "document.getElementById('ticket-error').textContent.includes('Changed since')");
    assert.equal((await request(server.origin, path)).title, "Authoritative remote title");
    await request(server.origin, `/api/projects/${project.id}`, "PATCH", { archived: true }, project.version);
    await browser("wait", "--fn", "!document.getElementById('archived-banner').hidden");
    await request(server.origin, "/api/projects", "POST", { name: "Another live project", key: "NEXT" });
    await browser("wait", "--fn", "document.getElementById('active-projects').textContent.includes('1')");
    await browser("eval", "window.confirm = () => true; true");
    await browser("click", "#close-ticket");
    await browser("fill", "#search", "Authoritative");
    await browser("wait", "--fn", "document.querySelectorAll('.ticket-card').length === 1");
    const searchable = await request(server.origin, path);
    await request(server.origin, path, "PATCH", { title: "Authoritative revised" }, searchable.version);
    await browser("wait", "--fn", "document.querySelector('.card-title')?.textContent === 'Authoritative revised'");
    assert.match(await browser("eval", `(() => {
      if (window.liveDocumentMarker !== 'same-document') throw new Error('Live sync reloaded the page');
      if (document.getElementById('search').value !== 'Authoritative') throw new Error('Live sync erased search');
      if (document.querySelectorAll('.ticket-card').length !== 1) throw new Error('Live sync lost the search filter');
      return 'PASS';
    })()`), /PASS/);
    const archivable = await request(server.origin, path);
    const archived = await request(server.origin, path, "PATCH", { archived: true }, archivable.version);
    await browser("wait", "--fn", "document.querySelectorAll('.ticket-card').length === 0");
    await browser("click", "#archived-tickets");
    await browser("wait", ".ticket-card");
    await request(server.origin, path, "PATCH", { archived: false }, archived.version);
    await browser("wait", "--fn", "document.querySelectorAll('.ticket-card').length === 0");
    await browser("click", "#active-tickets");
    await browser("wait", ".ticket-card");
  } finally {
    try { await browser("close"); } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
  }
});

test("ticket archive: reversible removal keeps records and unsent comments", { timeout: 45000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-ticket-archive-"));
  const session = `docket-ticket-archive-${process.pid}`;
  const server = await startServer(join(directory, "test.sqlite"));
  const browser = async (...arguments_) => (await execute("agent-browser", ["--session", session, ...arguments_], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax })).stdout;
  try {
    const project = await request(server.origin, "/api/projects", "POST", { name: "Ticket archive", key: "TARC" });
    const ticket = await request(server.origin, `/api/projects/${project.id}/tickets`, "POST", { title: "Retain this ticket", description: "Retain the description" });
    const path = `/api/projects/${project.id}/tickets/${ticket.id}`;
    await request(server.origin, path + "/comments", "POST", { body: "Retain this comment" }, ticket.version);
    const comments = await request(server.origin, path + "/comments");
    await browser("open", `${server.origin}/projects/${project.id}`);
    await browser("click", ".ticket-card");
    await browser("wait", "--fn", "document.getElementById('ticket-dialog').open");
    assert.match(await browser("eval", `(() => {
      if (!document.getElementById('archive-ticket')) throw new Error('Ticket archive action is missing');
      return 'PASS';
    })()`), /PASS/);
    await browser("fill", "#comment-body", "Unsent comment survives archiving");
    await browser("eval", "window.confirm = () => false; true");
    await browser("click", "#archive-ticket");
    assert.equal((await request(server.origin, path)).archived, false);
    await browser("eval", "window.confirm = () => true; true");
    await browser("click", "#archive-ticket");
    await browser("wait", "--fn", "!document.getElementById('ticket-archived-banner').hidden");
    assert.match(await browser("eval", "document.getElementById('comment-body').value"), /Unsent comment survives archiving/);
    assert.equal((await request(server.origin, path)).archived, true);
    assert.deepEqual((await request(server.origin, `/api/projects/${project.id}/tickets`)).items, [], "The API must exclude archived tickets without a filter");
    assert.deepEqual((await request(server.origin, `/api/projects/${project.id}/tickets?archived=true`)).items.map((item) => item.id), [ticket.id], "Archived tickets must require an explicit API filter");
    assert.deepEqual(await request(server.origin, path + "/comments"), comments);
    await browser("click", "#close-ticket");
    await browser("wait", "--fn", "document.querySelectorAll('.ticket-card').length === 0");
    await browser("click", "#archived-tickets");
    await browser("wait", ".ticket-card");
    await browser("click", ".ticket-card");
    await browser("wait", "--fn", "!document.getElementById('ticket-archived-banner').hidden");
    await browser("click", "#archive-ticket");
    await browser("wait", "--fn", "document.getElementById('ticket-archived-banner').hidden");
    await browser("click", "#close-ticket");
    await browser("click", "#active-tickets");
    await browser("wait", ".ticket-card");
    const restored = await request(server.origin, path);
    assert.equal(restored.archived, false);
    assert.equal(restored.id, ticket.id);
    assert.equal(restored.title, ticket.title);
    assert.equal(restored.description, ticket.description);
    assert.deepEqual(await request(server.origin, path + "/comments"), comments);
    assert.equal((await request(server.origin, path + "/history")).items.length, 4);
    await browser("click", ".ticket-card");
    await browser("fill", "#ticket-title", "Unsaved changes");
    await browser("click", "#archive-ticket");
    await browser("wait", "--fn", "document.getElementById('ticket-error').textContent.includes('Save or discard')");
    assert.equal((await request(server.origin, path)).archived, false);
  } finally {
    try { await browser("close"); } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
  }
});

test("assignee: free-text create, edit, clear, safe rendering, and live draft preservation", { timeout: 45000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-assignee-"));
  const session = `docket-assignee-${process.pid}`;
  const server = await startServer(join(directory, "test.sqlite"));
  const browser = async (...arguments_) => (await execute("agent-browser", ["--session", session, ...arguments_], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax })).stdout;
  try {
    const project = await request(server.origin, "/api/projects", "POST", { name: "Assignments", key: "WHO" });
    await browser("open", `${server.origin}/projects/${project.id}`);
    await browser("click", "#new-ticket");
    assert.match(await browser("eval", `(() => {
      const input = document.getElementById('ticket-assignee');
      if (!input) throw new Error('Free-text assignee field is missing');
      if (input.value !== '') throw new Error('New tickets should be unassigned');
      return 'PASS';
    })()`), /PASS/);
    await browser("fill", "#ticket-title", "Assigned ticket");
    await browser("fill", "#ticket-assignee", "  Morgan  ");
    await browser("click", "#save-ticket");
    await browser("wait", "--fn", "document.querySelector('.card-assignee span')?.textContent === 'Morgan'");
    const created = (await request(server.origin, `/api/projects/${project.id}/tickets`)).items[0];
    assert.equal(created.assignee, "Morgan");
    const path = `/api/projects/${project.id}/tickets/${created.id}`;
    await browser("click", ".ticket-card");
    await browser("fill", "#ticket-assignee", "Casey");
    await browser("click", "#save-ticket");
    await browser("wait", "--fn", "!document.getElementById('save-ticket').disabled");
    const saved = await request(server.origin, path);
    assert.equal(saved.assignee, "Casey");
    await request(server.origin, path, "PATCH", { assignee: "Remote owner" }, saved.version);
    await browser("wait", "--fn", "document.getElementById('ticket-assignee').value === 'Remote owner'");
    await browser("fill", "#ticket-assignee", "Unsaved assignee");
    await browser("fill", "#comment-body", "Unsent assignment note");
    const remote = await request(server.origin, path);
    await request(server.origin, path, "PATCH", { assignee: "Authoritative owner" }, remote.version);
    await browser("wait", "--fn", "!document.getElementById('reload-ticket').hidden");
    assert.match(await browser("eval", `(() => {
      if (document.getElementById('ticket-assignee').value !== 'Unsaved assignee') throw new Error('Live sync erased assignee draft');
      if (document.getElementById('comment-body').value !== 'Unsent assignment note') throw new Error('Live sync erased comment');
      return 'PASS';
    })()`), /PASS/);
    await browser("click", "#save-ticket");
    await browser("wait", "--fn", "document.getElementById('ticket-error').textContent.includes('Changed since')");
    assert.equal((await request(server.origin, path)).assignee, "Authoritative owner");
    await browser("eval", "window.confirm = () => true; true");
    await browser("click", "#reload-ticket");
    await browser("wait", "--fn", "document.getElementById('ticket-assignee').value === 'Authoritative owner'");
    await browser("fill", "#ticket-assignee", "");
    await browser("click", "#save-ticket");
    await browser("wait", "--fn", "!document.getElementById('save-ticket').disabled");
    assert.equal((await request(server.origin, path)).assignee, "");
    const literal = "<img src=x onerror=window.assignmentExecuted=true>";
    await browser("fill", "#ticket-assignee", literal);
    await browser("click", "#save-ticket");
    await browser("wait", "--fn", "!document.getElementById('save-ticket').disabled");
    await browser("click", "#close-ticket");
    await browser("click", "#list-view");
    assert.match(await browser("eval", `(() => {
      if (document.querySelector('.list-assignee').textContent !== ${JSON.stringify(literal)}) throw new Error('Assignee not displayed literally in list');
      if (document.querySelector('.list-assignee img') || window.assignmentExecuted) throw new Error('Assignee executed as HTML');
      return 'PASS';
    })()`), /PASS/);
    await browser("set", "viewport", "390", "844");
    assert.match(await browser("eval", `(() => {
      const bounds = document.querySelector('.list-assignee').getBoundingClientRect();
      if (bounds.width <= 0 || bounds.left < 0 || bounds.right > innerWidth) throw new Error('Assignee column is offscreen on phone');
      return 'PASS';
    })()`), /PASS/);
  } finally {
    try { await browser("close"); } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
  }
});

test("project settings: gear entry, delete-to-archive help, cancellation, stale restore, and phone layout", { timeout: 45000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-settings-"));
  const session = `docket-settings-${process.pid}`;
  const server = await startServer(join(directory, "test.sqlite"));
  const browser = async (...arguments_) => (await execute("agent-browser", ["--session", session, ...arguments_], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax })).stdout;
  try {
    const project = await request(server.origin, "/api/projects", "POST", { name: "Settings example", key: "PREF" });
    await browser("open", `${server.origin}/projects/${project.id}`);
    await browser("wait", "#project-settings");
    assert.match(await browser("eval", `(() => {
      const button = document.getElementById('project-settings');
      if (button.textContent.trim() || !button.querySelector('use')?.getAttribute('href').endsWith('#settings')) throw new Error('Project settings should use a gear icon, not text');
      if (button.getAttribute('aria-label') !== 'Project settings' || button.title !== 'Project settings') throw new Error('Gear action needs an accessible label and tooltip');
      if (document.querySelector('.header-actions #archive-project')) throw new Error('Delete should not be in the board header');
      return 'PASS';
    })()`), /PASS/);
    await browser("click", "#project-settings");
    await browser("wait", "--fn", "document.getElementById('project-dialog').open");
    assert.match(await browser("eval", `(() => {
      const button = document.getElementById('archive-project');
      if (!document.getElementById('project-form').contains(button) || button.type !== 'button') throw new Error('Delete action must live in settings without submitting edits');
      if (!button.textContent.includes('Delete project')) throw new Error('Delete action label is missing');
      const help = document.getElementById(button.getAttribute('aria-describedby')).textContent.toLowerCase();
      if (!help.includes('archived') || !help.includes('restore') || !help.includes('history')) throw new Error('Delete helper must explain reversible archival and retained history');
      return 'PASS';
    })()`), /PASS/);
    const screenshots = process.env.DOCKET_SCREENSHOTS;
    if (screenshots) {
      await mkdir(screenshots, { recursive: true });
      await browser("set", "viewport", "1440", "1000");
      await browser("screenshot", join(screenshots, "project-settings-desktop.png"));
      await browser("set", "viewport", "1280", "720");
    }
    await browser("fill", "#project-title", "Unsaved settings name");
    await browser("eval", "window.confirm = message => { window.lastConfirmation = message; return false; }; true");
    await browser("click", "#archive-project");
    assert.deepEqual(await request(server.origin, `/api/projects/${project.id}`), project);
    assert.match(await browser("eval", "document.getElementById('project-title').value"), /Unsaved settings name/);
    await browser("fill", "#project-title", "");
    await browser("eval", "window.confirm = message => { window.lastConfirmation = message; return true; }; true");
    await browser("click", "#archive-project");
    await browser("wait", "--fn", "!document.getElementById('project-dialog').open && !document.getElementById('archived-banner').hidden");
    assert.match(await browser("eval", "window.lastConfirmation"), /unsaved/i);
    const archived = await request(server.origin, `/api/projects/${project.id}`);
    assert.equal(archived.archived, true);
    assert.equal(archived.name, project.name, "Delete should not silently save unsaved settings");
    await browser("click", "#project-settings");
    const changed = await request(server.origin, `/api/projects/${project.id}`, "PATCH", { name: "Updated elsewhere", statuses: archived.statuses }, archived.version);
    await browser("wait", "--fn", "document.getElementById('project-name').textContent === 'Updated elsewhere'");
    await browser("click", "#archive-project");
    await browser("wait", "--fn", "document.getElementById('project-error').textContent.includes('Changed since')");
    assert.equal((await request(server.origin, `/api/projects/${project.id}`)).version, changed.version);
    assert.equal((await request(server.origin, `/api/projects/${project.id}`)).archived, true);
    await browser("click", "#cancel-project");
    await browser("click", "#project-settings");
    assert.match(await browser("eval", "document.getElementById('archive-project').textContent"), /Restore project/);
    await browser("click", "#archive-project");
    await browser("wait", "--fn", "!document.getElementById('project-dialog').open && document.getElementById('archived-banner').hidden");
    assert.equal((await request(server.origin, `/api/projects/${project.id}`)).archived, false);
    await browser("set", "viewport", "390", "844");
    await browser("click", "#project-settings");
    await browser("eval", "document.getElementById('project-lifecycle').scrollIntoView({block:'nearest'}); true");
    assert.match(await browser("eval", `(() => {
      for (const id of ['archive-project', 'save-project', 'cancel-project']) {
        const bounds = document.getElementById(id).getBoundingClientRect();
        if (bounds.width <= 0 || bounds.left < 0 || bounds.right > innerWidth || bounds.top < 0 || bounds.bottom > innerHeight) throw new Error('Settings control is offscreen on phone: ' + id);
      }
      return 'PASS';
    })()`), /PASS/);
    if (screenshots) await browser("screenshot", join(screenshots, "project-settings-phone.png"));
    await browser("eval", "window.confirm = () => false; true");
    await browser("click", "#archive-project");
    assert.equal((await request(server.origin, `/api/projects/${project.id}`)).archived, false);
    await browser("click", "#cancel-project");
    await browser("click", "#add-project");
    assert.match(await browser("eval", `(() => {
      if (document.getElementById('archive-project')) throw new Error('New-project form must not offer deletion');
      return 'PASS';
    })()`), /PASS/);
  } finally {
    try { await browser("close"); } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
  }
});

test("board drag: native status moves, no-op and cancelled drops, retained records, and editor fallback", { timeout: 45000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-board-drag-"));
  const session = `docket-drag-${process.pid}`;
  const server = await startServer(join(directory, "test.sqlite"));
  const browser = async (...arguments_) => (await execute("agent-browser", ["--session", session, ...arguments_], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax })).stdout;
  try {
    const project = await request(server.origin, "/api/projects", "POST", { name: "Drag board", key: "DRAG", statuses: ["Queue", "Building", "Review"] });
    const ticket = await request(server.origin, `/api/projects/${project.id}/tickets`, "POST", { title: "Move this ticket", description: "Keep this description", assignee: "Morgan" });
    const path = `/api/projects/${project.id}/tickets/${ticket.id}`;
    const posted = await request(server.origin, path + "/comments", "POST", { body: "Keep the discussion" }, ticket.version);
    const comments = await request(server.origin, path + "/comments");
    const targetTicket = await request(server.origin, `/api/projects/${project.id}/tickets`, "POST", { title: "Existing destination ticket", status: "Building" });
    await browser("set", "viewport", "1440", "1000");
    await browser("open", `${server.origin}/projects/${project.id}`);
    await browser("wait", ".ticket-card");
    assert.match(await browser("eval", `(() => {
      if (!document.querySelector('.ticket-card').draggable) throw new Error('Board tickets should support dragging');
      document.addEventListener('dragstart', event => { window.nativeDragTrusted = event.isTrusted; });
      document.addEventListener('drop', event => { window.highlightedDrop = event.target.closest('.column')?.classList.contains('drop-target'); }, true);
      return 'PASS';
    })()`), /PASS/);
    await browser("drag", ".ticket-card", '.column[aria-label="Queue"]');
    assert.equal((await request(server.origin, path)).version, posted.ticket_version, "Same-column drop must not create a write");
    await browser("drag", ".ticket-card", `[data-ticket-id="${targetTicket.id}"]`);
    await browser("wait", "--fn", "document.querySelectorAll('.column[aria-label=\"Building\"] .ticket-card').length === 2 && document.querySelector('.ticket-card')?.disabled === false");
    const moved = await request(server.origin, path);
    assert.equal(moved.status, "Building");
    assert.equal(moved.version, posted.ticket_version + 1);
    assert.equal(moved.title, ticket.title);
    assert.equal(moved.description, ticket.description);
    assert.equal(moved.assignee, ticket.assignee);
    assert.deepEqual(await request(server.origin, path + "/comments"), comments);
    assert.deepEqual(await request(server.origin, `/api/projects/${project.id}/tickets/${targetTicket.id}`), targetTicket, "Dropping on a card must not modify the destination ticket");
    const history = await request(server.origin, path + "/history");
    assert.equal(history.items.at(-1).kind, "ticket.updated");
    assert.equal(history.items.at(-1).data.status, "Building");
    assert.match(await browser("eval", `(() => {
      if (window.nativeDragTrusted !== true) throw new Error('Test must exercise a native drag, not synthetic DOM events');
      if (!window.highlightedDrop) throw new Error('Valid drop target was not highlighted');
      if (document.getElementById('ticket-dialog').open) throw new Error('Dragging should not open the editor');
      if (document.querySelectorAll('.dragging,.drop-target').length) throw new Error('Drag styling was not cleaned up');
      if (document.querySelector('.column[aria-label="Queue"] .count').textContent !== '0' || document.querySelector('.column[aria-label="Building"] .count').textContent !== '2') throw new Error('Column counts did not move with the ticket');
      return 'PASS';
    })()`), /PASS/);
    await browser("drag", ".ticket-card", ".brand");
    assert.equal((await request(server.origin, path)).version, moved.version, "Dropping outside columns must not write");
    assert.match(await browser("eval", "window.nativeDragTrusted === true && !document.getElementById('ticket-dialog').open && !document.querySelector('.dragging,.drop-target')"), /true/);
    await browser("eval", "document.querySelector('.ticket-card').focus(); true");
    await browser("press", "Enter");
    await browser("wait", "--fn", "document.getElementById('ticket-dialog').open");
    assert.match(await browser("eval", "document.getElementById('ticket-status').value"), /Building/);
    await browser("click", "#close-ticket");
    await request(server.origin, path, "PATCH", { archived: true }, moved.version);
    await browser("click", "#archived-tickets");
    await browser("wait", "--fn", "document.querySelector('.ticket-card') !== null && document.getElementById('content').getAttribute('aria-busy') === 'false'");
    assert.match(await browser("eval", `(() => {
      if (document.querySelector('.ticket-card').draggable) throw new Error('Archived tickets should not be draggable');
      return 'PASS';
    })()`), /PASS/);
  } finally {
    try { await browser("close"); } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
  }
});

async function connectNativeDrag(origin, browser) {
  const connection = JSON.parse(await browser("get", "cdp-url", "--json"));
  assert.equal(connection.success, true);
  assert.ok(connection.data.cdpUrl.startsWith("ws://127.0.0.1:"));
  const socket = new WebSocket(connection.data.cdpUrl);
  const events = new EventTarget();
  let sequence = 0;
  let pending;
  let pageSession;
  let dragData;
  socket.addEventListener("message", (message) => {
    try {
      const response = JSON.parse(message.data);
      if (response.method === "Input.dragIntercepted") {
        dragData = response.params.data;
        events.dispatchEvent(new Event("drag"));
      }
      if (pending && response.id === pending.id) {
        clearTimeout(pending.timer);
        const waiting = pending;
        pending = undefined;
        if (response.error) waiting.reject(new Error("Native drag protocol command failed"));
        else waiting.resolve(response.result);
      }
    } catch (error) {
      if (pending) { clearTimeout(pending.timer); pending.reject(error); pending = undefined; }
      socket.close();
    }
  });
  for (const event of ["error", "close"]) socket.addEventListener(event, () => {
    if (pending) { clearTimeout(pending.timer); pending.reject(new Error("Native drag protocol disconnected")); pending = undefined; }
  });
  const send = (method, parameters) => new Promise((resolve, reject) => {
    assert.equal(pending, undefined, "Only one native drag protocol command may be pending");
    const id = ++sequence;
    const timer = setTimeout(() => { pending = undefined; reject(new Error("Native drag protocol command timed out")); }, requestTimeoutMs);
    pending = { id, resolve, reject, timer };
    try { socket.send(JSON.stringify({ id, method, params: parameters, ...(pageSession ? { sessionId: pageSession } : {}) })); }
    catch (error) { clearTimeout(timer); pending = undefined; reject(error); }
  });
  try {
    await once(socket, "open", { signal: AbortSignal.timeout(requestTimeoutMs) });
    const targets = await send("Target.getTargets", {});
    const target = targets.targetInfos.find((item) => item.url.startsWith(origin + "/"));
    assert.ok(target, "Disposable drag tab must exist");
    pageSession = (await send("Target.attachToTarget", { targetId: target.targetId, flatten: true })).sessionId;
    await send("Input.setInterceptDrags", { enabled: true });
    return { send, events, get data() { return dragData; }, close() { socket.close(); } };
  } catch (error) { socket.close(); throw error; }
}

test("board drag safety: frozen versions, live polling, offline recovery, and navigation scope", { timeout: 90000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-drag-safety-"));
  const session = `docket-drag-safety-${process.pid}`;
  let nativeDrag;
  const server = await startServer(join(directory, "test.sqlite"));
  const browser = async (...arguments_) => (await execute("agent-browser", ["--session", session, ...arguments_], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax })).stdout;
  const center = async (selector) => {
    const result = JSON.parse(await browser("eval", `(() => {
      const element = document.querySelector(${JSON.stringify(selector)});
      if (!element) throw new Error('Native drag element is missing');
      const bounds = element.getBoundingClientRect();
      return {x: Math.floor(bounds.left + bounds.width / 2), y: Math.floor(bounds.top + bounds.height / 2)};
    })()`, "--json"));
    assert.equal(result.success, true);
    assert.ok(Number.isFinite(result.data.result.x) && Number.isFinite(result.data.result.y));
    return result.data.result;
  };
  try {
    const statuses = ["Queue", "Building", "Review"];
    const project = await request(server.origin, "/api/projects", "POST", { name: "Drag safety", key: "SAFE", statuses });
    const other = await request(server.origin, "/api/projects", "POST", { name: "Other board", key: "ELSE", statuses });
    const ticket = await request(server.origin, `/api/projects/${project.id}/tickets`, "POST", { title: "Frozen drag snapshot" });
    const otherTicket = await request(server.origin, `/api/projects/${other.id}/tickets`, "POST", { title: "Other board ticket" });
    const path = `/api/projects/${project.id}/tickets/${ticket.id}`;
    await browser("set", "viewport", "1440", "1000");
    await browser("open", `${server.origin}/projects/${project.id}`);
    await browser("wait", "--fn", "document.querySelector('.ticket-card')?.draggable === true");
    await browser("eval", `window.originalMetadataFetch = window.fetch; window.releaseMetadataRequest = null;
      window.fetch = (input, options) => typeof input === 'string' && input.startsWith('/api/projects?after=')
        ? new Promise((resolve, reject) => { window.releaseMetadataRequest = () => window.originalMetadataFetch(input, options).then(resolve, reject); })
        : window.originalMetadataFetch(input, options); true`);
    await browser("wait", "--fn", "window.releaseMetadataRequest !== null");
    await browser("eval", "document.addEventListener('dragstart', event => { window.safetyDragTrusted = event.isTrusted; }, true); document.addEventListener('drop', event => { window.safetyDropTrusted = event.isTrusted; }, true); true");
    const source = await center(".ticket-card");
    const target = await center('.column[aria-label="Building"]');
    const dragThresholdPx = 20;
    nativeDrag = await connectNativeDrag(server.origin, browser);
    await Promise.all([
      once(nativeDrag.events, "drag", { signal: AbortSignal.timeout(requestTimeoutMs) }),
      (async () => {
        await nativeDrag.send("Input.dispatchMouseEvent", { type: "mouseMoved", x: source.x, y: source.y });
        await nativeDrag.send("Input.dispatchMouseEvent", { type: "mousePressed", button: "left", buttons: 1, clickCount: 1, x: source.x, y: source.y });
        // Chromium needs both held-button fields to initiate a native HTML drag through CDP.
        await nativeDrag.send("Input.dispatchMouseEvent", { type: "mouseMoved", button: "left", buttons: 1, x: source.x + dragThresholdPx, y: source.y });
        await nativeDrag.send("Input.dispatchMouseEvent", { type: "mouseMoved", button: "left", buttons: 1, x: source.x + dragThresholdPx * 2, y: source.y });
      })(),
    ]);
    await browser("wait", ".ticket-card.dragging");
    assert.ok(Array.isArray(nativeDrag.data.items) && nativeDrag.data.items.length > 0, "Drag data must come from Chromium's native source");
    assert.ok(nativeDrag.data.items.every((item) => item.data === String(ticket.id)), "Transferable drag data should contain only the ticket ID, not ticket text");
    await nativeDrag.send("Input.dispatchDragEvent", { type: "dragEnter", x: target.x, y: target.y, data: nativeDrag.data });
    await nativeDrag.send("Input.dispatchDragEvent", { type: "dragOver", x: target.x, y: target.y, data: nativeDrag.data });
    await browser("wait", ".column.drop-target");
    if (process.env.DOCKET_SCREENSHOTS) {
      await mkdir(process.env.DOCKET_SCREENSHOTS, { recursive: true });
      await browser("screenshot", join(process.env.DOCKET_SCREENSHOTS, "board-drag-active.png"));
    }
    await browser("eval", "window.fetch = window.originalMetadataFetch; window.releaseMetadataRequest(); true");
    const authoritative = await request(server.origin, path, "PATCH", { status: "Review", title: "Changed while dragging" }, ticket.version);
    // Holding across a poll interval proves the snapshot version is not silently rebased mid-gesture.
    const liveCycleWaitMs = 3500;
    await delay(liveCycleWaitMs);
    assert.match(await browser("eval", `(() => {
      if (!document.querySelector('.column[aria-label="Queue"] .ticket-card.dragging')) throw new Error('Live polling replaced the native drag source');
      return 'PASS';
    })()`), /PASS/);
    await nativeDrag.send("Input.dispatchDragEvent", { type: "drop", x: target.x, y: target.y, data: nativeDrag.data });
    await nativeDrag.send("Input.dispatchMouseEvent", { type: "mouseReleased", button: "left", buttons: 0, clickCount: 1, x: target.x, y: target.y });
    await nativeDrag.send("Input.setInterceptDrags", { enabled: false });
    nativeDrag.close();
    nativeDrag = undefined;
    await browser("wait", "--fn", "document.getElementById('notification').textContent.includes('Changed since') && document.querySelector('.column[aria-label=\"Review\"] .ticket-card') !== null && document.querySelector('.ticket-card')?.draggable === true");
    assert.deepEqual(await request(server.origin, path), authoritative, "Stale drop must not overwrite a newer ticket");
    assert.match(await browser("eval", "window.safetyDragTrusted === true && window.safetyDropTrusted === true"), /true/);
    await browser("set", "offline", "on");
    await browser("drag", ".ticket-card", '.column[aria-label="Building"]');
    await browser("wait", "--fn", "document.getElementById('notification').textContent.includes('Reload to check') && !document.querySelector('.ticket-card').disabled");
    assert.deepEqual(await request(server.origin, path), authoritative, "Offline dragging must not move the ticket client-side only");
    assert.match(await browser("eval", `(() => {
      if (!document.querySelector('.column[aria-label="Review"] .ticket-card')) throw new Error('Failed drop left the card in the wrong column');
      if (document.querySelector('.dragging,.drop-target')) throw new Error('Failed drop left drag styling active');
      return 'PASS';
    })()`), /PASS/);
    await browser("set", "offline", "off");
    await browser("drag", ".ticket-card", '.column[aria-label="Queue"]');
    await browser("wait", "--fn", "document.querySelector('.column[aria-label=\"Queue\"] .ticket-card') !== null && document.querySelector('.ticket-card')?.draggable === true");
    const recovered = await request(server.origin, path);
    assert.equal(recovered.status, "Queue");
    assert.equal(recovered.version, authoritative.version + 1);
    await browser("eval", `window.originalMoveFetch = window.fetch; window.releaseMoveRequest = null;
      window.fetch = (input, options) => input === ${JSON.stringify(path)} && options?.method === 'PATCH'
        ? new Promise((resolve, reject) => { window.releaseMoveRequest = () => window.originalMoveFetch(input, options).then(resolve, reject); })
        : window.originalMoveFetch(input, options); true`);
    await browser("drag", ".ticket-card", '.column[aria-label="Building"]');
    await browser("wait", "--fn", "window.releaseMoveRequest !== null");
    await browser("click", ".project-link:nth-child(2)");
    await browser("wait", "--fn", `location.pathname === '/projects/${other.id}'`);
    await browser("eval", "window.fetch = window.originalMoveFetch; window.releaseMoveRequest(); true");
    await browser("wait", "--fn", "document.querySelector('.ticket-card')?.draggable === true");
    const completed = await request(server.origin, path);
    assert.equal(completed.status, "Building");
    assert.equal(completed.version, recovered.version + 1, "Move must commit exactly once after navigation");
    assert.deepEqual(await request(server.origin, `/api/projects/${other.id}/tickets/${otherTicket.id}`), otherTicket);
    assert.match(await browser("eval", `(() => {
      if (location.pathname !== '/projects/${other.id}' || document.getElementById('project-name').textContent !== 'Other board') throw new Error('Move completion retargeted project navigation');
      const cards = document.querySelectorAll('.ticket-card');
      if (cards.length !== 1 || cards[0].dataset.ticketId !== '${otherTicket.id}') throw new Error('Old project ticket leaked into the new board');
      return 'PASS';
    })()`), /PASS/);
  } finally {
    nativeDrag?.close();
    await browser("mouse", "up").catch(() => console.error("Disposable drag pointer cleanup unavailable"));
    try { await browser("close"); } finally { await stopServer(server); await rm(directory, { recursive: true, force: true }); }
  }
});
