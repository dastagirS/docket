import test from "node:test";
import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, copyFile, writeFile, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const execute = promisify(execFile);
const timeoutMs = 15000;
const outputBytesMax = 1048576;
const origin = "git@github.com:dastagirS/docket.git";

async function fixture() {
  const directory = await mkdtemp(join(tmpdir(), "docket-release-script-"));
  try {
    const checkout = join(directory, "checkout");
    const remote = join(directory, "remote.git");
    const home = join(directory, "home");
    await Promise.all([checkout, home].map(path => mkdir(path)));
    const environment = { ...Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith("GIT_"))), HOME: home, XDG_CONFIG_HOME: home, GIT_CONFIG_NOSYSTEM: "1", GIT_TERMINAL_PROMPT: "0", GIT_ALLOW_PROTOCOL: "file" };
    const git = async (...arguments_) => (await execute("git", arguments_, { cwd: checkout, env: environment, timeout: timeoutMs, maxBuffer: outputBytesMax })).stdout.trim();
    await git("init", "--bare", "--initial-branch=master", remote);
    await git("init", "--initial-branch=master");
    await git("config", "user.name", "Release fixture");
    await git("config", "user.email", "fixture@example.invalid");
    await git("config", "url." + remote + ".insteadOf", origin);
    await git("remote", "add", "origin", origin);
    await mkdir(join(checkout, "scripts"));
    await copyFile(resolve("scripts/release.sh"), join(checkout, "scripts", "release.sh"));
    await copyFile(resolve("scripts/release.mjs"), join(checkout, "scripts", "release.mjs"));
    await writeFile(join(checkout, "README.md"), "release fixture\n");
    await git("add", ".");
    await git("commit", "-m", "Fixture base");
    await git("push", "origin", "HEAD:refs/heads/master");
    const base = await git("rev-parse", "HEAD");
    await writeFile(join(checkout, "README.md"), "release fixture ahead\n");
    await git("commit", "-am", "Fixture changes");
    const commit = await git("rev-parse", "HEAD");
    return { directory, checkout, remote, environment, git, base, commit, close: () => rm(directory, { recursive: true, force: true }) };
  } catch (error) {
    await rm(directory, { recursive: true, force: true });
    throw error;
  }
}

async function release(fixture, { arguments_: arguments_ = [], answer = "yes\n", preview, environment = {}, closeOutput = false } = {}) {
  const child = spawn(join(fixture.checkout, "scripts", "release.sh"), arguments_, {
    cwd: fixture.checkout, env: { ...fixture.environment, ...environment }, stdio: ["pipe", "pipe", "pipe"],
  });
  let stdout = "";
  let stderr = "";
  let confirmed = false;
  let failure;
  const timer = setTimeout(() => { failure = new Error("Release fixture timed out"); child.kill("SIGKILL"); }, timeoutMs);
  const consume = (data, diagnostics) => {
    if (Buffer.byteLength(stdout) + Buffer.byteLength(stderr) + data.length > outputBytesMax) {
      failure = new Error("Release fixture output exceeded its limit");
      child.kill("SIGKILL");
      return;
    }
    if (diagnostics) stderr += data;
    else stdout += data;
    if (!confirmed && stdout.includes("[y/N]")) {
      confirmed = true;
      Promise.resolve().then(() => preview?.()).then(() => child.stdin.end(answer)).catch(error => {
        failure = error;
        child.kill("SIGKILL");
      });
    }
  };
  child.stdout.on("data", data => consume(data, false));
  child.stderr.on("data", data => consume(data, true));
  if (closeOutput) child.stdout.destroy();
  child.stdin.on("error", error => { if (error.code !== "EPIPE") failure = error; });
  child.on("error", error => { failure = error; });
  const code = await new Promise(resolveExit => child.once("close", resolveExit));
  clearTimeout(timer);
  if (failure) throw failure;
  return { code, stdout, stderr };
}

async function remoteRefs(fixture) {
  return fixture.git("--git-dir", fixture.remote, "for-each-ref", "--format=%(refname) %(objectname)");
}

test("release script computes patch/minor/major from all local and remote stable tags", { timeout: 60000 }, async () => {
  for (const [arguments_, tag] of [[[], "v0.0.10"], [["minor"], "v0.1.0"], [["major"], "v1.0.0"]]) {
    const setup = await fixture();
    try {
      await setup.git("tag", "v0.0.2", setup.base);
      await setup.git("tag", "v0.0.5", setup.base);
      await setup.git("--git-dir", setup.remote, "update-ref", "refs/tags/v0.0.9", setup.base);
      await setup.git("--git-dir", setup.remote, "update-ref", "refs/tags/v9.0.0-beta", setup.base);
      const previousTags = await setup.git("show-ref", "--tags");
      const result = await release(setup, { arguments_ });
      assert.equal(result.code, 0, result.stderr);
      assert.match(result.stdout, new RegExp(`Release: ${tag.replaceAll(".", "\\.")}`));
      assert.match(result.stdout, /publication|publish/);
      assert.equal(await setup.git("--git-dir", setup.remote, "rev-parse", "refs/heads/master"), setup.commit);
      assert.equal(await setup.git("--git-dir", setup.remote, "rev-parse", `refs/tags/${tag}^{commit}`), setup.commit);
      assert.equal(await setup.git("cat-file", "-t", `refs/tags/${tag}`), "tag");
      assert.equal(await setup.git("rev-parse", "HEAD"), setup.commit, "Release must not auto-commit");
      for (const previous of previousTags.split("\n")) assert.ok((await setup.git("show-ref", "--tags")).includes(previous));
      assert.equal(await setup.git("--git-dir", setup.remote, "rev-parse", "refs/tags/v0.0.9"), setup.base);
    } finally { await setup.close(); }
  }
});

test("release script cancellation and EOF leave local tags and remote refs untouched", { timeout: 30000 }, async () => {
  const setup = await fixture();
  try {
    const before = await remoteRefs(setup);
    for (const answer of ["no\n", ""]) {
      const result = await release(setup, { answer });
      assert.equal(result.code, 0, result.stderr);
      assert.match(result.stdout, /Cancelled/);
      assert.equal(await setup.git("tag", "--list"), "");
      assert.equal(await remoteRefs(setup), before);
    }
  } finally { await setup.close(); }
});

test("release preflight blocks dirty, detached, wrong-branch, wrong-origin, and diverged checkouts", { timeout: 60000 }, async () => {
  for (const scenario of ["dirty", "detached", "branch", "origin", "split-origin", "active-operation", "diverged", "invalid-argument"]) {
    const setup = await fixture();
    try {
      if (scenario === "dirty") await writeFile(join(setup.checkout, "untracked"), "fixture");
      if (scenario === "detached") await setup.git("checkout", "--detach");
      if (scenario === "branch") await setup.git("checkout", "-b", "feature");
      if (scenario === "origin") await setup.git("remote", "set-url", "origin", "https://fixture:private-password@github.com/dastagirS/docket.git");
      if (scenario === "split-origin") await setup.git("config", "remote.origin.pushurl", "https://github.com/dastagirS/docket.git");
      if (scenario === "active-operation") await writeFile(join(setup.checkout, ".git", "CHERRY_PICK_HEAD"), setup.base + "\n");
      if (scenario === "diverged") {
        await setup.git("checkout", "-b", "other", setup.base);
        await writeFile(join(setup.checkout, "other"), "other branch");
        await setup.git("add", "other");
        await setup.git("commit", "-m", "Other branch");
        const other = await setup.git("rev-parse", "HEAD");
        await setup.git("push", "origin", `${other}:refs/heads/master`);
        await setup.git("checkout", "master");
      }
      const before = await remoteRefs(setup);
      const result = await release(setup, { arguments_: scenario === "invalid-argument" ? ["banana"] : [] });
      assert.notEqual(result.code, 0, scenario);
      assert.ok(!result.stdout.includes("[y/N]"), scenario);
      assert.ok(!result.stderr.includes("private-password"));
      assert.equal(await setup.git("tag", "--list"), "");
      assert.equal(await remoteRefs(setup), before);
    } finally { await setup.close(); }
  }
});

test("release aborts stale previews before creating a local tag", { timeout: 30000 }, async () => {
  for (const scenario of ["local-change", "remote-tag"]) {
    const setup = await fixture();
    try {
      const result = await release(setup, { preview: async () => {
        if (scenario === "local-change") await writeFile(join(setup.checkout, "README.md"), "changed while confirming");
        else await setup.git("--git-dir", setup.remote, "update-ref", "refs/tags/v0.0.1", setup.base);
      } });
      assert.notEqual(result.code, 0);
      assert.match(result.stderr, scenario === "local-change" ? /Commit or stash working-tree changes/ : /changed during confirmation/);
      assert.equal(await setup.git("tag", "--list"), "");
      assert.equal(await setup.git("--git-dir", setup.remote, "rev-parse", "refs/heads/master"), setup.base);
    } finally { await setup.close(); }
  }
});

test("atomic rejection or unsupported atomic push cannot partially update origin", { timeout: 30000 }, async () => {
  for (const scenario of ["reject-tag", "unsupported"]) {
    const setup = await fixture();
    try {
      if (scenario === "reject-tag") await writeFile(join(setup.remote, "hooks", "pre-receive"), "#!/bin/sh\nexit 1\n", { mode: 0o755 });
      else await setup.git("--git-dir", setup.remote, "config", "receive.advertiseAtomic", "false");
      const before = await remoteRefs(setup);
      const result = await release(setup);
      assert.notEqual(result.code, 0);
      assert.match(result.stderr, /No non-atomic fallback/);
      assert.match(result.stderr, /Local tag v0.0.1 was retained/);
      assert.equal(await remoteRefs(setup), before);
      assert.equal(await setup.git("rev-parse", "refs/tags/v0.0.1^{commit}"), setup.commit);
    } finally { await setup.close(); }
  }
});

test("a lost push result is resolved by reading the exact remote tag without retrying", { timeout: 30000 }, async () => {
  const setup = await fixture();
  try {
    const tools = join(setup.directory, "tools");
    await mkdir(tools);
    const actualGit = (await execute("sh", ["-c", "command -v git"], { timeout: timeoutMs })).stdout.trim();
    const marker = join(tools, "push-count");
    await writeFile(join(tools, "git"), '#!/bin/sh\n"$TEST_REAL_GIT" "$@"\nstatus=$?\nif [ "$1" = push ] && [ "$status" -eq 0 ]; then printf "push\\n" >> "$TEST_PUSH_COUNT"; exit 1; fi\nexit "$status"\n', { mode: 0o755 });
    const result = await release(setup, { environment: { PATH: `${tools}:${process.env.PATH}`, TEST_REAL_GIT: actualGit, TEST_PUSH_COUNT: marker } });
    assert.equal(result.code, 0, result.stderr);
    assert.match(result.stdout, /origin confirms the exact release tag/);
    assert.equal(await readFile(marker, "utf8"), "push\n");
    assert.equal(await setup.git("--git-dir", setup.remote, "rev-parse", "refs/tags/v0.0.1^{commit}"), setup.commit);
  } finally { await setup.close(); }
});


test("release pushes only the selected tag even when push.followTags is enabled", { timeout: 30000 }, async () => {
  const setup = await fixture();
  try {
    await setup.git("config", "push.followTags", "true");
    await setup.git("tag", "-a", "v0.0.7", "-m", "Unpublished older tag", setup.commit);
    const result = await release(setup);
    assert.equal(result.code, 0, result.stderr);
    assert.equal(await setup.git("--git-dir", setup.remote, "rev-parse", "refs/tags/v0.0.8^{commit}"), setup.commit);
    await assert.rejects(setup.git("--git-dir", setup.remote, "show-ref", "--verify", "--quiet", "refs/tags/v0.0.7"));
  } finally { await setup.close(); }
});


test("unconfirmed push outcomes retain the tag and never retry or claim success", { timeout: 30000 }, async () => {
  const setup = await fixture();
  try {
    const tools = join(setup.directory, "tools");
    await mkdir(tools);
    const actualGit = (await execute("sh", ["-c", "command -v git"], { timeout: timeoutMs })).stdout.trim();
    const marker = join(tools, "push-count");
    await writeFile(join(tools, "git"), '#!/bin/sh\nif [ "$1" = ls-remote ] && [ -f "$TEST_PUSH_COUNT" ]; then exit 1; fi\n"$TEST_REAL_GIT" "$@"\nstatus=$?\nif [ "$1" = push ]; then printf "push\\n" >> "$TEST_PUSH_COUNT"; fi\nexit "$status"\n', { mode: 0o755 });
    const result = await release(setup, { environment: { PATH: `${tools}:${process.env.PATH}`, TEST_REAL_GIT: actualGit, TEST_PUSH_COUNT: marker } });
    assert.notEqual(result.code, 0);
    assert.match(result.stderr, /push outcome may be uncertain/);
    assert.ok(!result.stdout.includes("Pushed v"));
    assert.equal(await readFile(marker, "utf8"), "push\n");
    assert.equal(await setup.git("rev-parse", "refs/tags/v0.0.1^{commit}"), setup.commit);
    assert.equal(await setup.git("--git-dir", setup.remote, "rev-parse", "refs/tags/v0.0.1^{commit}"), setup.commit);
  } finally { await setup.close(); }
});

test("version arithmetic is exact and oversized stable tags fail closed", { timeout: 30000 }, async () => {
  const setup = await fixture();
  try {
    await setup.git("tag", "v0.9007199254740993.9", setup.base);
    const result = await release(setup);
    assert.equal(result.code, 0, result.stderr);
    assert.match(result.stdout, /Release: v0\.9007199254740993\.10/);
    await setup.git("tag", "v" + "9".repeat(64) + ".0.0", setup.base);
    const before = await remoteRefs(setup);
    const refused = await release(setup);
    assert.notEqual(refused.code, 0);
    assert.match(refused.stderr, /exceeds the release version limit/);
    assert.equal(await remoteRefs(setup), before);
  } finally { await setup.close(); }
});


test("closed preview output aborts before creating or publishing a tag", { timeout: 30000 }, async () => {
  const setup = await fixture();
  try {
    const before = await remoteRefs(setup);
    const result = await release(setup, { closeOutput: true });
    assert.notEqual(result.code, 0);
    assert.match(result.stderr, /EPIPE|Release output was incomplete/);
    assert.equal(await setup.git("tag", "--list"), "");
    assert.equal(await remoteRefs(setup), before);
  } finally { await setup.close(); }
});

test("resolved Git URL rewrites cannot send a release to a different push repository", { timeout: 30000 }, async () => {
  for (const phase of ["preflight", "confirmation"]) {
    const setup = await fixture();
    try {
      const otherRemote = join(setup.directory, "other.git");
      await setup.git("init", "--bare", "--initial-branch=master", otherRemote);
      await setup.git("push", otherRemote, `${setup.base}:refs/heads/master`);
      const beforeFetch = await remoteRefs(setup);
      const beforePush = await setup.git("--git-dir", otherRemote, "for-each-ref", "--format=%(refname) %(objectname)");
      const redirect = () => setup.git("config", `url.${otherRemote}.pushInsteadOf`, origin);
      if (phase === "preflight") await redirect();
      const result = await release(setup, { preview: phase === "confirmation" ? redirect : undefined });
      assert.notEqual(result.code, 0);
      assert.equal(await setup.git("--git-dir", otherRemote, "for-each-ref", "--format=%(refname) %(objectname)"), beforePush, "The rewritten push repository must remain untouched");
      assert.equal(await remoteRefs(setup), beforeFetch);
      assert.equal(await setup.git("tag", "--list"), "");
      assert.match(result.stderr, /Resolved fetch and push destinations must match/);
    } finally { await setup.close(); }
  }
});
