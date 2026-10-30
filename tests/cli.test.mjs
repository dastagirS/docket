import test from "node:test";
import { AsyncLocalStorage } from "node:async_hooks";
import assert from "node:assert/strict";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, rm, writeFile, stat, mkdir, readFile, chmod, symlink, realpath, readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { once } from "node:events";
import http from "node:http";

const execute = promisify(execFile);
const binary = resolve(process.env.DOCKET_BINARY ?? "bin/docket");
const commandTimeoutMs = 15000;
const outputBytesMax = 1048576;
const homeContext = new AsyncLocalStorage();
function isolatedTest(name, options, callback) {
  return test(name, options, async (context) => {
    const home = await mkdtemp(join(tmpdir(), "docket-cli-home-"));
    try { await homeContext.run(home, () => callback(context, home)); }
    finally { await rm(home, { recursive: true, force: true }); }
  });
}

async function cli(arguments_, options = {}) {
  const { stdin, keepStdinOpen = false, ...executionOptions } = options;
  try {
    const pending = execute(binary, arguments_, { timeout: commandTimeoutMs, maxBuffer: outputBytesMax, env: { ...process.env, HOME: homeContext.getStore(), DOCKET_URL: "http://127.0.0.1:0", DOCKET_ACTOR: "" }, ...executionOptions });
    if (!keepStdinOpen) pending.child.stdin.end(stdin);
    const result = await pending;
    return { ...result, code: 0 };
  } catch (failure) {
    if (typeof failure.code !== "number") throw failure;
    return { code: failure.code, stdout: failure.stdout, stderr: failure.stderr };
  }
}
async function startServer(directory, explicit = false) {
  const child = spawn(binary, [...(explicit ? ["serve"] : []), "--database", join(directory, "test.sqlite"), "--listen", "127.0.0.1:0"], { stdio: ["ignore", "ignore", "pipe"] });
  let output = "";
  let failure;
  child.on("error", error => { failure = error; });
  child.stderr.on("data", data => { output = (output + data.toString()).slice(-65536); });
  for (let i = 0; i < 100; i++) {
    if (failure) throw failure;
    const match = output.match(/url=(http:\/\/127\.0\.0\.1:\d+)/);
    if (match) return { child, origin: match[1] };
    if (child.exitCode !== null) throw new Error("Isolated CLI server exited before readiness");
    await delay(50);
  }
  child.kill("SIGKILL");
  throw new Error("Isolated CLI server did not become ready");
}
async function stopServer(child) {
  if (child.exitCode !== null || child.signalCode !== null) return;
  const exited = once(child, "exit", { signal: AbortSignal.timeout(commandTimeoutMs) });
  child.kill("SIGTERM");
  try { await exited; } catch (failure) { child.kill("SIGKILL"); throw failure; }
}

isolatedTest("CLI real service: doctor, project/ticket commands, subtickets, versions, archives, comments, and events", { timeout: 60000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-cli-"));
  const server = await startServer(directory);
  const command = async (arguments_, expected = 0) => {
    const result = await cli([...arguments_, "--server", server.origin]);
    assert.equal(result.code, expected, result.stderr);
    assert.equal(expected ? result.stdout : result.stderr, "", "JSON results and diagnostics must stay on separate streams");
    return JSON.parse(expected ? result.stderr : result.stdout);
  };
  try {
    assert.equal((await command(["doctor"])).status, "ok");
    const project = await command(["project", "create", "--name", "CLI board", "--key", "CLI", "--statuses", '["Queue","Building","Done"]']);
    const scope = ["--project", String(project.id)];
    assert.equal((await command(["project", "list"])).items[0].id, project.id);
    assert.equal((await command(["project", "get", String(project.id)])).key, "CLI");
    const parent = await command(["ticket", "create", ...scope, "--title", "Parent", "--description", "Keep this", "--assignee", "Morgan"]);
    const child = await command(["ticket", "create", ...scope, "--title", "Child", "--parent", String(parent.id)]);
    assert.equal(child.parent_id, parent.id);
    assert.equal((await command(["ticket", "list", ...scope, "--parent", String(parent.id)])).items[0].id, child.id);
    const first = await command(["ticket", "list", ...scope, "--limit", "1", "--json"]);
    assert.equal(first.items[0].id, parent.id);
    assert.equal((await command(["ticket", "list", ...scope, "--after", String(first.next_cursor)])).items[0].id, child.id);
    const moved = await command(["ticket", "update", String(child.id), ...scope, "--version", "1", "--status", "Building"]);
    assert.equal(moved.status, "Building");
    assert.equal(moved.parent_id, parent.id);
    assert.equal((await command(["ticket", "update", String(child.id), ...scope, "--version", "1", "--status", "Done"], 4)).error.code, "conflict");
    assert.equal((await command(["ticket", "get", String(parent.id), ...scope])).version, parent.version);
    const cleared = await command(["ticket", "update", String(parent.id), ...scope, "--version", "1", "--assignee", ""]);
    assert.equal(cleared.assignee, "");
    assert.equal(cleared.description, "Keep this", "Omitted PATCH flags must preserve fields");
    const comment = await command(["comment", "add", String(child.id), ...scope, "--version", String(moved.version), "--body", "Ready\nSecond line", "--actor", "Test agent"]);
    assert.equal(comment.comment.actor, "Test agent");
    assert.equal((await command(["comment", "list", String(child.id), ...scope])).items[0].body, "Ready\nSecond line");
    const archived = await command(["ticket", "archive", String(child.id), ...scope, "--version", String(comment.ticket_version)]);
    assert.equal(archived.archived, true);
    assert.equal((await command(["ticket", "list", ...scope])).items.length, 1);
    assert.equal((await command(["ticket", "list", ...scope, "--archived"])).items[0].id, child.id);
    const restored = await command(["ticket", "restore", String(child.id), ...scope, "--version", String(archived.version)]);
    assert.equal(restored.archived, false);
    assert.ok((await command(["ticket", "history", String(child.id), ...scope])).items.length >= 4);
    const events = await command(["events", "list", ...scope, "--after", "0"]);
    assert.ok(events.next_cursor > 0);
    const fromStdin = await cli(["ticket", "create", ...scope, "--server", server.origin, "--input", "-"], { stdin: JSON.stringify({title:"From stdin", description:"first\nsecond"}) });
    assert.equal(fromStdin.code, 0, fromStdin.stderr);
    assert.equal(JSON.parse(fromStdin.stdout).description, "first\nsecond");
    const input = join(directory, "ticket.json");
    await writeFile(input, JSON.stringify({ title: "From file", description: "Literal <script> and multiline\ntext" }));
    assert.equal((await command(["ticket", "create", ...scope, "--input", input])).title, "From file");
    const other = await command(["project", "create", "--name", "Other", "--key", "OTHER"]);
    assert.equal((await command(["ticket", "get", String(child.id), "--project", String(other.id)], 3)).error.code, "not_found");
    await command(["ticket", "create", ...scope, "--title", "Too deep", "--parent", String(child.id)], 2);
    const renamed = await command(["project", "update", String(project.id), "--name", "Renamed", "--statuses", '["Queue","Building","Done"]', "--version", String(project.version)]);
    const archivedProject = await command(["project", "archive", String(project.id), "--version", String(renamed.version)]);
    assert.equal(archivedProject.archived, true);
    assert.equal((await command(["project", "restore", String(project.id), "--version", String(archivedProject.version)])).archived, false);
  } finally {
    try { await stopServer(server.child); } finally { await rm(directory, { recursive: true, force: true }); }
  }
});

isolatedTest("CLI help and invalid/unavailable requests never create a local database", { timeout: 30000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-cli-no-database-"));
  try {
    for (const arguments_ of [[], ["--help"], ["ticket", "update", "--help"]]) {
      const result = await cli(arguments_, { cwd: directory });
      assert.equal(result.code, 0);
      assert.match(result.stdout, /Usage:/);
      assert.equal(result.stderr, "");
    }
    for (const arguments_ of [
      ["ticket", "get", "1"],
      ["ticket", "update", "1", "--project", "1", "--title", "No version"],
      ["ticket", "create", "--project", "1", "--parent", "0", "--title", "Invalid parent"],
      ["doctor", "--server", "http://user:private-password@127.0.0.1:0"],
      ["doctor", "--timeout", "0s"],
    ]) {
      const result = await cli(arguments_, { cwd: directory });
      assert.equal(result.code, 2, result.stderr);
      assert.equal(result.stdout, "");
      assert.doesNotMatch(result.stderr, /private-password/);
      assert.ok(JSON.parse(result.stderr).error.code);
    }
    const inputTimeout = await cli(["ticket", "create", "--project", "1", "--input", "-", "--timeout", "50ms"], { cwd: directory, keepStdinOpen: true });
    assert.equal(inputTimeout.code, 2, inputTimeout.stderr);
    assert.equal(JSON.parse(inputTimeout.stderr).error.code, "input_timeout");
    const unavailable = await cli(["doctor", "--server", "http://127.0.0.1:0"], { cwd: directory });
    assert.equal(unavailable.code, 5);
    assert.equal(JSON.parse(unavailable.stderr).error.code, "unavailable");
    const uncertain = await cli(["ticket", "create", "--project", "1", "--title", "No server", "--server", "http://127.0.0.1:0"], { cwd: directory });
    assert.equal(uncertain.code, 6);
    assert.equal(JSON.parse(uncertain.stderr).error.outcome_unknown, true);
    await assert.rejects(stat(join(directory, "data")), { code: "ENOENT" });
  } finally { await rm(directory, { recursive: true, force: true }); }
});

isolatedTest("CLI explicit serve, environment configuration, and legacy backup operate only on disposable files", { timeout: 30000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-cli-serve-"));
  const server = await startServer(directory, true);
  try {
    const result = await cli(["doctor"], { env: { ...process.env, HOME: homeContext.getStore(), DOCKET_URL: server.origin, DOCKET_ACTOR: "Test actor" } });
    assert.equal(result.code, 0, result.stderr);
    assert.equal(JSON.parse(result.stdout).status, "ok");
    const override = await cli(["doctor", "--server", "http://127.0.0.1:0"], { env: { ...process.env, HOME: homeContext.getStore(), DOCKET_URL: server.origin } });
    assert.equal(override.code, 5, "Explicit server must take precedence over environment");
    const help = await cli(["doctor", "--help"], { env: { ...process.env, HOME: homeContext.getStore(), DOCKET_URL: "http://user:private-password@localhost", DOCKET_ACTOR: "private-label" } });
    assert.equal(help.code, 0);
    assert.doesNotMatch(help.stdout + help.stderr, /private-password|private-label/);
    const backup = join(directory, "backup.sqlite");
    const backedUp = await cli(["--database", join(directory, "test.sqlite"), "--backup-to", backup]);
    assert.equal(backedUp.code, 0, backedUp.stderr);
    assert.ok((await stat(backup)).size > 0);
  } finally {
    try { await stopServer(server.child); } finally { await rm(directory, { recursive: true, force: true }); }
  }
});

isolatedTest("CLI lost write response: real commit is not retried and is reported as uncertain", { timeout: 30000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-cli-response-loss-"));
  const server = await startServer(directory);
  let attempts = 0;
  let committed;
  let forwardingFailure;
  const proxy = http.createServer(async (request, response) => {
    attempts++;
    try {
      assert.equal(request.method, "POST");
      const chunks = [];
      let size = 0;
      const requestBytesMax = 64 * 1024;
      for await (const chunk of request) {
        size += chunk.length;
        assert.ok(size <= requestBytesMax, "Forwarded request exceeded the CLI body bound");
        chunks.push(chunk);
      }
      const forwarded = await fetch(server.origin + request.url, { method: "POST", headers: { "Content-Type": "application/json" }, body: Buffer.concat(chunks), signal: AbortSignal.timeout(5000) });
      assert.equal(forwarded.status, 201);
      committed = await forwarded.json();
      response.destroy();
    } catch (failure) { forwardingFailure = failure; response.destroy(); }
  });
  proxy.requestTimeout = 5000;
  proxy.maxConnections = 2;
  try {
    const projectResult = await cli(["project", "create", "--name", "Fault test", "--key", "FAULT", "--server", server.origin]);
    assert.equal(projectResult.code, 0, projectResult.stderr);
    const project = JSON.parse(projectResult.stdout);
    const listening = once(proxy, "listening", { signal: AbortSignal.timeout(5000) });
    proxy.listen(0, "127.0.0.1");
    await listening;
    const result = await cli(["ticket", "create", "--project", String(project.id), "--title", "One committed attempt", "--server", `http://127.0.0.1:${proxy.address().port}`]);
    if (forwardingFailure) throw forwardingFailure;
    assert.equal(result.code, 6, result.stderr);
    assert.equal(result.stdout, "");
    assert.equal(JSON.parse(result.stderr).error.outcome_unknown, true);
    assert.equal(attempts, 1, "A failed write response must not cause an automatic replay");
    assert.ok(committed.id > 0);
    const listed = await cli(["ticket", "list", "--project", String(project.id), "--server", server.origin]);
    assert.equal(listed.code, 0, listed.stderr);
    assert.deepEqual(JSON.parse(listed.stdout).items.map(ticket => ticket.id), [committed.id]);
  } finally {
    if (proxy.listening) { proxy.closeAllConnections(); await new Promise((resolve, reject) => proxy.close(error => error ? reject(error) : resolve())); }
    try { await stopServer(server.child); } finally { await rm(directory, { recursive: true, force: true }); }
  }
});

isolatedTest("CLI closed stdout pipe: committed write returns an uncertainty diagnostic instead of SIGPIPE", { timeout: 30000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-cli-broken-pipe-"));
  const server = await startServer(directory);
  try {
    const created = await cli(["project", "create", "--name", "Pipe test", "--key", "PIPE", "--server", server.origin]);
    assert.equal(created.code, 0, created.stderr);
    const project = JSON.parse(created.stdout);
    const child = spawn(binary, ["ticket", "create", "--project", String(project.id), "--title", "Committed despite output failure", "--server", server.origin], { stdio: ["ignore", "pipe", "pipe"] });
    let diagnostics = "";
    child.stderr.on("data", chunk => { diagnostics = (diagnostics + chunk.toString()).slice(-outputBytesMax); });
    const exited = once(child, "exit", { signal: AbortSignal.timeout(commandTimeoutMs) });
    child.stdout.destroy();
    let code, signal;
    try { [code, signal] = await exited; } catch (failure) { child.kill("SIGKILL"); throw failure; }
    assert.equal(signal, null, "SIGPIPE must not bypass structured write-outcome diagnostics");
    assert.equal(code, 6, diagnostics);
    assert.equal(JSON.parse(diagnostics).error.outcome_unknown, true);
    const listed = await cli(["ticket", "list", "--project", String(project.id), "--server", server.origin]);
    assert.equal(listed.code, 0, listed.stderr);
    assert.equal(JSON.parse(listed.stdout).items.length, 1, "Output failure must neither lose nor replay the committed write");
  } finally {
    try { await stopServer(server.child); } finally { await rm(directory, { recursive: true, force: true }); }
  }
});

isolatedTest("CLI project init: folder binding, inherited scope, three repeat choices, and archived ticket counts", { timeout: 60000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-init-"));
  const folder = join(directory, "work");
  await mkdir(folder);
  const server = await startServer(directory);
  const inFolder = (arguments_, options = {}) => cli(arguments_, { cwd: folder, ...options });
  try {
    const initialized = await inFolder(["project", "init", "--server", server.origin, "--name", "Folder project", "--key", "FOLDER"]);
    assert.equal(initialized.code, 0, initialized.stderr);
    const first = JSON.parse(initialized.stdout).project;
    const markerPath = join(homeContext.getStore(), ".docket", "registry.json");
    const originalMarker = await readFile(markerPath, "utf8");
    assert.equal(JSON.parse(originalMarker).bindings[0].project_url, `${server.origin}/projects/${first.id}`);
    const created = await inFolder(["ticket", "create", "--title", "Uses this folder"]);
    assert.equal(created.code, 0, created.stderr);
    const ticket = JSON.parse(created.stdout);
    assert.equal(ticket.project_id, first.id);
    const nested = join(folder, "source", "nested");
    await mkdir(nested, { recursive: true });
    const listed = await cli(["ticket", "list"], { cwd: nested });
    assert.equal(listed.code, 0, listed.stderr);
    assert.equal(JSON.parse(listed.stdout).items[0].id, ticket.id);
    const archived = await inFolder(["ticket", "archive", String(ticket.id), "--version", String(ticket.version)]);
    assert.equal(archived.code, 0, archived.stderr);
    const repeated = await inFolder(["project", "init"]);
    assert.equal(repeated.code, 2);
    assert.match(JSON.parse(repeated.stderr).error.message, /1 ticket/);
    assert.equal(await readFile(markerPath, "utf8"), originalMarker);
    const keep = await inFolder(["project", "init", "--on-existing", "keep"]);
    assert.equal(keep.code, 0, keep.stderr);
    assert.equal(JSON.parse(keep.stdout).project.id, first.id);
    assert.equal(await readFile(markerPath, "utf8"), originalMarker);
    const another = await inFolder(["project", "init", "--on-existing", "create", "--name", "Second folder project", "--key", "SECOND"]);
    assert.equal(another.code, 0, another.stderr);
    const second = JSON.parse(another.stdout).project;
    const old = await cli(["project", "get", String(first.id), "--server", server.origin]);
    assert.equal(JSON.parse(old.stdout).archived, false, "Create another must leave old project active");
    const replaced = await inFolder(["project", "init", "--on-existing", "archive", "--name", "Replacement", "--key", "REPLACED"]);
    assert.equal(replaced.code, 0, replaced.stderr);
    const third = JSON.parse(replaced.stdout).project;
    const secondState = await cli(["project", "get", String(second.id), "--server", server.origin]);
    assert.equal(JSON.parse(secondState.stdout).archived, true);
    const fresh = await inFolder(["ticket", "create", "--title", "Uses replacement"]);
    assert.equal(JSON.parse(fresh.stdout).project_id, third.id);
    const oldTicket = await cli(["ticket", "get", String(ticket.id), "--project", String(first.id), "--server", server.origin]);
    assert.equal(JSON.parse(oldTicket.stdout).archived, true, "Existing tickets must remain intact");
    await assert.rejects(stat(join(homeContext.getStore(), ".docket", "init.lock")), { code: "ENOENT" });
  } finally {
    try { await stopServer(server.child); } finally { await rm(directory, { recursive: true, force: true }); }
  }
});

isolatedTest("CLI project init: interactive default and replacement choices, marker validation, and safe overrides", { timeout: 60000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-init-prompt-"));
  const folder = join(directory, "work");
  await mkdir(folder);
  const server = await startServer(directory);
  const command = (arguments_, options = {}) => cli(arguments_, { cwd: folder, ...options });
  try {
    const initial = await command(["project", "init", "--server", server.origin, "--name", "First", "--key", "FIRST"]);
    assert.equal(initial.code, 0, initial.stderr);
    const first = JSON.parse(initial.stdout).project;
    const marker = join(homeContext.getStore(), ".docket", "registry.json");
    const original = await readFile(marker, "utf8");
    const keep = await command(["project", "init", "--interactive"], { stdin: "\n" });
    assert.equal(keep.code, 0, keep.stderr);
    assert.match(keep.stderr, /Keep existing \[default\]/);
    assert.match(keep.stderr, /0 tickets/);
    assert.equal(JSON.parse(keep.stdout).project.id, first.id);
    assert.equal(await readFile(marker, "utf8"), original);
    const invalid = await command(["project", "init", "--interactive"], { stdin: "invalid\n" });
    assert.equal(invalid.code, 2);
    assert.equal(await readFile(marker, "utf8"), original);
    const create = await command(["project", "init", "--interactive"], { stdin: "2\nSecond\nSECOND\n" });
    assert.equal(create.code, 0, create.stderr);
    assert.equal(JSON.parse(create.stdout).project.key, "SECOND");
    const archive = await command(["project", "init", "--interactive"], { stdin: "3\nThird\nTHIRD\n" });
    assert.equal(archive.code, 0, archive.stderr);
    const third = JSON.parse(archive.stdout).project;
    const markerThird = await readFile(marker, "utf8");
    const duplicate = await command(["project", "init", "--on-existing", "create", "--name", "Duplicate", "--key", "THIRD"]);
    assert.equal(duplicate.code, 4);
    assert.equal(await readFile(marker, "utf8"), markerThird);
    await assert.rejects(stat(join(homeContext.getStore(), ".docket", "init.lock")), { code: "ENOENT" });
    const retarget = await command(["ticket", "create", "--title", "Do not retarget", "--server", "http://127.0.0.1:0"]);
    assert.equal(retarget.code, 2);
    const wrongIdentity = JSON.parse(markerThird);
    wrongIdentity.bindings[0].project_created_at = "2000-01-01T00:00:00Z";
    await writeFile(marker, JSON.stringify(wrongIdentity), { mode: 0o600 });
    const stale = await command(["ticket", "create", "--title", "Do not create on changed identity"]);
    assert.equal(stale.code, 2);
    const direct = await command(["ticket", "list", "--project", String(third.id), "--server", server.origin]);
    assert.equal(direct.code, 0);
    assert.equal(JSON.parse(direct.stdout).items.length, 0);
    await writeFile(marker, markerThird, { mode: 0o600 });
    const childRepo = join(folder, "nested-repo");
    await mkdir(join(childRepo, ".git"), { recursive: true });
    const boundary = await cli(["ticket", "list"], { cwd: childRepo });
    assert.equal(boundary.code, 2, "Nested Git roots must not inherit outer bindings");
    await chmod(marker, 0o644);
    assert.equal((await command(["ticket", "list"])).code, 2);
    await chmod(marker, 0o600);
    await rm(marker);
    await symlink(join(directory, "not-a-marker"), marker);
    assert.equal((await command(["project", "init", "--name", "Unsafe", "--key", "UNSAFE"])).code, 2);
  } finally {
    try { await stopServer(server.child); } finally { await rm(directory, { recursive: true, force: true }); }
  }
});

async function forwardInitializationRequest(request, origin) {
  const requestBytesMax = 65536;
  request.setTimeout(commandTimeoutMs, () => request.destroy());
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > requestBytesMax) throw new Error("Fixture request exceeded input bound");
    chunks.push(chunk);
  }
  return fetch(`${origin}${request.url}`, {
    method: request.method,
    headers: { "Content-Type": "application/json", ...(request.headers["if-match"] ? { "If-Match": request.headers["if-match"] } : {}) },
    body: request.method === "GET" ? undefined : Buffer.concat(chunks),
    signal: AbortSignal.timeout(commandTimeoutMs),
  });
}

isolatedTest("CLI project init: lost creation response retains recovery journal and prevents duplicate writes", { timeout: 60000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-init-lost-"));
  const folder = join(directory, "work");
  await mkdir(folder);
  const server = await startServer(directory);
  let mutations = 0;
  const proxy = http.createServer(async (request, response) => {
    try {
      const upstream = await forwardInitializationRequest(request, server.origin);
      const payload = await upstream.text();
      if (request.method === "POST") {
        mutations++;
        assert.equal(upstream.status, 201);
        response.destroy();
      } else { response.writeHead(upstream.status, { "Content-Type": "application/json" }); response.end(payload); }
    } catch { response.destroy(); }
  });
  await new Promise(resolve => proxy.listen(0, "127.0.0.1", resolve));
  const origin = `http://127.0.0.1:${proxy.address().port}`;
  try {
    const otherFolder = join(directory,"other");
    await mkdir(otherFolder);
    const other = await cli(["project","init","--server",server.origin,"--name","Other","--key","OTHERINIT"],{cwd:otherFolder});
    assert.equal(other.code,0,other.stderr);
    const args = ["project", "init", "--server", origin, "--name", "Lost reply", "--key", "LOSTINIT"];
    const lost = await cli(args, { cwd: folder });
    assert.equal(lost.code, 6, lost.stderr);
    assert.equal(mutations, 1);
    const journal = JSON.parse(await readFile(join(homeContext.getStore(), ".docket", "init.lock"), "utf8"));
    assert.equal(journal.stage, "creating");
    assert.equal(journal.requested_key, "LOSTINIT");
    await assert.rejects(stat(join(folder, ".docket")), { code: "ENOENT" });
    const again = await cli(args, { cwd: folder });
    assert.equal(again.code, 2);
    const implicit = await cli(["ticket", "create", "--title", "Must not guess"], { cwd: folder });
    assert.equal(implicit.code, 2);
    assert.equal(mutations, 1, "Pending initialization must not be retried");
    const projects = await cli(["project", "list", "--server", server.origin], { cwd: directory });
    assert.equal(projects.code, 0, projects.stderr);
    assert.equal(JSON.parse(projects.stdout).items.filter(project=>project.key==="LOSTINIT").length,1,"Exactly one lost-response creation committed");
    const unrelated = await cli(["ticket","create","--title","Other folder remains usable"],{cwd:otherFolder});
    assert.equal(unrelated.code,0,unrelated.stderr);
    assert.equal(JSON.parse(unrelated.stdout).project_id,JSON.parse(other.stdout).project.id);
  } finally {
    try { await new Promise(resolve => proxy.close(resolve)); } finally {
      try { await stopServer(server.child); } finally { await rm(directory, { recursive: true, force: true }); }
    }
  }
});

isolatedTest("CLI project init: concurrent attempts create only one project", { timeout: 60000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-init-concurrent-"));
  const folder = join(directory, "work");
  await mkdir(folder);
  const server = await startServer(directory);
  try {
    const results = await Promise.all(["FIRST", "SECOND"].map(key => cli(["project", "init", "--server", server.origin, "--name", key, "--key", key], { cwd: folder })));
    assert.deepEqual(results.map(result => result.code).sort(), [0, 2]);
    const projects = await cli(["project", "list", "--server", server.origin], { cwd: directory });
    assert.equal(projects.code, 0, projects.stderr);
    assert.equal(JSON.parse(projects.stdout).items.length, 1);
    const listed = await cli(["ticket", "list"], { cwd: folder });
    assert.equal(listed.code, 0, listed.stderr);
    await assert.rejects(stat(join(homeContext.getStore(), ".docket", "init.lock")), { code: "ENOENT" });
  } finally {
    try { await stopServer(server.child); } finally { await rm(directory, { recursive: true, force: true }); }
  }
});

for (const archiveFailure of ["stale", "invalid-success"]) {
isolatedTest(`CLI project init: ${archiveFailure} archival retains original marker and records created replacement for recovery`, { timeout: 60000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-init-partial-"));
  const folder = join(directory, "work");
  await mkdir(folder);
  const server = await startServer(directory);
  let conflict = false;
  let original;
  const proxy = http.createServer(async (request, response) => {
    try {
      if (conflict && request.method === "PATCH") {
        conflict = false;
        if (archiveFailure === "invalid-success") { response.writeHead(200, { "Content-Type": "application/json" }); response.end("{}"); return; }
        const edit = await fetch(`${server.origin}/api/projects/${original.id}`, {
          method: "PATCH", headers: { "Content-Type": "application/json", "If-Match": `"${original.version}"` },
          body: JSON.stringify({ name: "Concurrent edit", statuses: original.statuses }), signal: AbortSignal.timeout(commandTimeoutMs),
        });
        assert.equal(edit.status, 200);
        await edit.text();
      }
      const upstream = await forwardInitializationRequest(request, server.origin);
      response.writeHead(upstream.status, { "Content-Type": "application/json" });
      response.end(await upstream.text());
    } catch { response.destroy(); }
  });
  await new Promise(resolve => proxy.listen(0, "127.0.0.1", resolve));
  const origin = `http://127.0.0.1:${proxy.address().port}`;
  try {
    const initial = await cli(["project", "init", "--server", origin, "--name", "Original", "--key", "ORIGINAL"], { cwd: folder });
    assert.equal(initial.code, 0, initial.stderr);
    original = JSON.parse(initial.stdout).project;
    const marker = await readFile(join(homeContext.getStore(), ".docket", "registry.json"), "utf8");
    conflict = true;
    const replaced = await cli(["project", "init", "--on-existing", "archive", "--name", "Replacement", "--key", "REPLACE"], { cwd: folder });
    assert.equal(replaced.code, 6, replaced.stderr);
    assert.equal(JSON.parse(replaced.stderr).error.code, "init_incomplete");
    assert.equal(conflict, false, "The real stale-write path must have been exercised");
    assert.equal(await readFile(join(homeContext.getStore(), ".docket", "registry.json"), "utf8"), marker);
    const journal = JSON.parse(await readFile(join(homeContext.getStore(), ".docket", "init.lock"), "utf8"));
    assert.equal(journal.stage, "archiving_previous");
    assert.match(journal.created_project_url, /\/projects\/2$/);
    const projects = await cli(["project", "list", "--server", server.origin], { cwd: directory });
    const items = JSON.parse(projects.stdout).items;
    assert.equal(items.length, 2);
    assert.equal(items.find(project => project.id === original.id).name, archiveFailure === "stale" ? "Concurrent edit" : "Original");
    assert.equal(items.find(project => project.id === original.id).archived, false);
    assert.equal((await cli(["ticket", "create", "--title", "Must not guess"], { cwd: folder })).code, 2);
  } finally {
    try { await new Promise(resolve => proxy.close(resolve)); } finally {
      try { await stopServer(server.child); } finally { await rm(directory, { recursive: true, force: true }); }
    }
  }
});

}

isolatedTest("central registry keeps independent folder boards in HOME and migrates legacy markers without creating boards", { timeout: 60000 }, async (_context, home) => {
  const directory = await mkdtemp(join(tmpdir(), "docket-central-registry-"));
  const firstFolder = join(directory,"first");
  const otherFolder = join(directory,"other");
  const legacyFolder = join(directory,"legacy");
  await Promise.all([firstFolder,otherFolder,legacyFolder].map(folder => mkdir(folder)));
  const server = await startServer(directory);
  try {
    const first = await cli(["project","init","--name","First","--key","FIRST","--server",server.origin],{cwd:firstFolder});
    assert.equal(first.code,0,first.stderr);
    const registryPath = join(home,".docket","registry.json");
    const registry = JSON.parse(await readFile(registryPath,"utf8"));
    assert.equal(registry.bindings.length,1);
    assert.equal(registry.bindings[0].folder,await realpath(firstFolder));
    await assert.rejects(stat(join(firstFolder,".docket")),{code:"ENOENT"});
    await assert.rejects(stat(join(firstFolder,".docket.lock")),{code:"ENOENT"});
    const other = await cli(["project","init","--name","Other","--key","OTHER","--server",server.origin],{cwd:otherFolder});
    assert.equal(other.code,0,other.stderr);
    assert.equal(JSON.parse(await readFile(registryPath,"utf8")).bindings.length,2);
    for (const [folder,project] of [[firstFolder,JSON.parse(first.stdout).project],[otherFolder,JSON.parse(other.stdout).project]]) {
      const created = await cli(["ticket","create","--title","Bound to this folder"],{cwd:folder});
      assert.equal(created.code,0,created.stderr);
      assert.equal(JSON.parse(created.stdout).project_id,project.id);
      assert.deepEqual(await readdir(folder),[]);
    }
    const alias=join(directory,"alias");
    await symlink(firstFolder,alias);
    const aliasResult=await cli(["ticket","list"],{cwd:alias});
    assert.equal(aliasResult.code,0,aliasResult.stderr);
    assert.equal(JSON.parse(aliasResult.stdout).items[0].project_id,JSON.parse(first.stdout).project.id);
    const unregistered=join(directory,"first-other");
    await mkdir(unregistered);
    assert.equal((await cli(["ticket","list"],{cwd:unregistered})).code,2,"Similar folder prefixes must not inherit another board");
    const projectResult = await cli(["project","create","--name","Legacy","--key","LEGACY","--server",server.origin]);
    assert.equal(projectResult.code,0,projectResult.stderr);
    const project = JSON.parse(projectResult.stdout);
    const legacyPath = join(legacyFolder,".docket");
    await writeFile(legacyPath,JSON.stringify({version:1,project_url:`${server.origin}/projects/${project.id}`,project_key:project.key,project_created_at:project.created_at}),{mode:0o600});
    const imported = await cli(["project","init","--on-existing","keep"],{cwd:legacyFolder});
    assert.equal(imported.code,0,imported.stderr);
    assert.equal(JSON.parse(imported.stdout).project.id,project.id);
    await assert.rejects(stat(legacyPath),{code:"ENOENT"});
    const finalRegistry = JSON.parse(await readFile(registryPath,"utf8"));
    assert.equal(finalRegistry.bindings.length,3);
    const projects = await cli(["project","list","--server",server.origin]);
    assert.equal(JSON.parse(projects.stdout).items.length,3,"Migration must not create another remote board");
  } finally { try { await stopServer(server.child); } finally { await rm(directory,{recursive:true,force:true}); } }
});

isolatedTest("central registry serializes concurrent folder initialization without losing unrelated bindings", { timeout: 60000 }, async (_context,home) => {
  const directory = await mkdtemp(join(tmpdir(),"docket-registry-concurrent-"));
  const folders = [join(directory,"first"),join(directory,"other")];
  await Promise.all(folders.map(folder=>mkdir(folder)));
  const server = await startServer(directory);
  try {
    const argumentsFor = index => ["project","init","--server",server.origin,"--name",`Folder ${index}`,"--key",index===0 ? "FIRST" : "OTHER"];
    const results=await Promise.all(folders.map((folder,index)=>cli(argumentsFor(index),{cwd:folder})));
    for (let i=0;i<results.length;i++) {
      if(results[i].code===2) results[i]=await cli(argumentsFor(i),{cwd:folders[i]});
      assert.equal(results[i].code,0,results[i].stderr);
    }
    const registry=JSON.parse(await readFile(join(home,".docket","registry.json"),"utf8"));
    assert.equal(registry.bindings.length,2);
    for(let i=0;i<folders.length;i++) {
      const canonicalFolder=await realpath(folders[i]);
      assert.equal(registry.bindings.find(binding=>binding.folder===canonicalFolder).project_key,i===0 ? "FIRST" : "OTHER");
      assert.deepEqual(await readdir(folders[i]),[]);
    }
    const projects=await cli(["project","list","--server",server.origin]);
    assert.equal(JSON.parse(projects.stdout).items.length,2,"Lock conflicts must not create extra boards");
  } finally { try { await stopServer(server.child); } finally { await rm(directory,{recursive:true,force:true}); } }
});

isolatedTest("central and legacy binding conflicts fail closed without rewriting either location", { timeout: 60000 }, async (_context,home) => {
  const directory=await mkdtemp(join(tmpdir(),"docket-registry-conflict-"));
  const folder=join(directory,"work");
  await mkdir(folder);
  const server=await startServer(directory);
  try {
    const initial=await cli(["project","init","--name","Initial","--key","INITIAL","--server",server.origin],{cwd:folder});
    assert.equal(initial.code,0,initial.stderr);
    const another=await cli(["project","create","--name","Foreign","--key","FOREIGN","--server",server.origin]);
    assert.equal(another.code,0,another.stderr);
    const foreign=JSON.parse(another.stdout);
    const legacy=JSON.stringify({version:1,project_url:`${server.origin}/projects/${foreign.id}`,project_key:foreign.key,project_created_at:foreign.created_at});
    await writeFile(join(folder,".docket"),legacy,{mode:0o600});
    const path=join(home,".docket","registry.json");
    const before=await readFile(path,"utf8");
    const refused=await cli(["project","init","--on-existing","keep"],{cwd:folder});
    assert.equal(refused.code,2);
    assert.match(JSON.parse(refused.stderr).error.message,/disagree/);
    assert.equal((await cli(["ticket","create","--title","Must not choose"],{cwd:folder})).code,2);
    assert.equal(await readFile(path,"utf8"),before);
    assert.equal(await readFile(join(folder,".docket"),"utf8"),legacy);
    const explicit=await cli(["ticket","list","--project",String(foreign.id),"--server",server.origin],{cwd:folder});
    assert.equal(JSON.parse(explicit.stdout).items.length,0);
  } finally { try { await stopServer(server.child); } finally { await rm(directory,{recursive:true,force:true}); } }
});

isolatedTest("central registry retains external edits made during a remote init write", { timeout: 60000 }, async (_context,home) => {
  const directory=await mkdtemp(join(tmpdir(),"docket-registry-edited-"));
  const folder=join(directory,"work");
  await mkdir(folder);
  const server=await startServer(directory);
  const path=join(home,".docket","registry.json");
  let edit=false;
  let external;
  const proxy=http.createServer(async(request,response)=>{
    try {
      const upstream=await forwardInitializationRequest(request,server.origin);
      const payload=await upstream.text();
      if(edit && request.method==="POST") {
        edit=false;
        external=(await readFile(path,"utf8"))+"\n";
        await writeFile(path,external,{mode:0o600});
      }
      response.writeHead(upstream.status,{"Content-Type":"application/json"});
      response.end(payload);
    } catch { response.destroy(); }
  });
  await new Promise(resolve=>proxy.listen(0,"127.0.0.1",resolve));
  const origin=`http://127.0.0.1:${proxy.address().port}`;
  try {
    const first=await cli(["project","init","--server",origin,"--name","Original","--key","ORIGINAL"],{cwd:folder});
    assert.equal(first.code,0,first.stderr);
    edit=true;
    const replacement=await cli(["project","init","--on-existing","create","--name","Another","--key","ANOTHER"],{cwd:folder});
    assert.equal(replacement.code,6,replacement.stderr);
    assert.equal(edit,false);
    assert.equal(await readFile(path,"utf8"),external,"External metadata must not be overwritten");
    const journal=JSON.parse(await readFile(join(home,".docket","init.lock"),"utf8"));
    assert.equal(journal.stage,"binding");
    assert.match(journal.created_project_url,/\/projects\/2$/);
    const projects=await cli(["project","list","--server",server.origin]);
    assert.equal(JSON.parse(projects.stdout).items.length,2,"Exactly one replacement was created");
    assert.equal((await cli(["ticket","list"],{cwd:folder})).code,2,"Affected implicit scope must stop for reconciliation");
  } finally { try { await new Promise(resolve=>proxy.close(resolve)); } finally { try { await stopServer(server.child); } finally { await rm(directory,{recursive:true,force:true}); } } }
});
