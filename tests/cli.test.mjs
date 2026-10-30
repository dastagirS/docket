import test from "node:test";
import assert from "node:assert/strict";
import { spawn, execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, rm, writeFile, stat } from "node:fs/promises";
import { tmpdir } from "node:os";
import { resolve, join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import { once } from "node:events";
import http from "node:http";

const execute = promisify(execFile);
const binary = resolve(process.env.DOCKET_BINARY ?? "bin/docket");
const commandTimeoutMs = 15000;
const outputBytesMax = 1048576;
async function cli(arguments_, options = {}) {
  const { stdin, keepStdinOpen = false, ...executionOptions } = options;
  try {
    const pending = execute(binary, arguments_, { timeout: commandTimeoutMs, maxBuffer: outputBytesMax, env: { ...process.env, DOCKET_URL: "http://127.0.0.1:0", DOCKET_ACTOR: "" }, ...executionOptions });
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

test("CLI real service: doctor, project/ticket commands, subtickets, versions, archives, comments, and events", { timeout: 60000 }, async () => {
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

test("CLI help and invalid/unavailable requests never create a local database", { timeout: 30000 }, async () => {
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

test("CLI explicit serve, environment configuration, and legacy backup operate only on disposable files", { timeout: 30000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-cli-serve-"));
  const server = await startServer(directory, true);
  try {
    const result = await cli(["doctor"], { env: { ...process.env, DOCKET_URL: server.origin, DOCKET_ACTOR: "Test actor" } });
    assert.equal(result.code, 0, result.stderr);
    assert.equal(JSON.parse(result.stdout).status, "ok");
    const override = await cli(["doctor", "--server", "http://127.0.0.1:0"], { env: { ...process.env, DOCKET_URL: server.origin } });
    assert.equal(override.code, 5, "Explicit server must take precedence over environment");
    const help = await cli(["doctor", "--help"], { env: { ...process.env, DOCKET_URL: "http://user:private-password@localhost", DOCKET_ACTOR: "private-label" } });
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

test("CLI lost write response: real commit is not retried and is reported as uncertain", { timeout: 30000 }, async () => {
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

test("CLI closed stdout pipe: committed write returns an uncertainty diagnostic instead of SIGPIPE", { timeout: 30000 }, async () => {
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
