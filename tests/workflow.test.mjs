import test from "node:test";
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, mkdir, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createServer } from "node:http";
import { once } from "node:events";
import { createHash } from "node:crypto";

const execute = promisify(execFile);
const workflow = await readFile(".github/workflows/release.yml","utf8");
const checksWorkflow = await readFile(".github/workflows/checks.yml", "utf8");
const validationWorkflow = await readFile(".github/workflows/validation.yml", "utf8");
const commandTimeoutMs = 5000;
const outputBytesMax = 65536;

test("release workflow exposes a required manual tag input while retaining tag-push releases", () => {
  assert.match(workflow,/workflow_dispatch:\n    inputs:\n      tag:/);
  assert.match(workflow,/tag:\n        description:.*\n        required: true\n        type: string/);
  assert.match(workflow,/push:\n    tags:\n      - 'v\*'/);
});

test("release workflow executes safe tag selection for manual and tag-push events", { timeout: 30000 }, async () => {
  const directory=await mkdtemp(join(tmpdir(),"docket-workflow-"));
  try {
    const start=workflow.indexOf("      - name: Validate stable release tag\n");
    assert.notEqual(start,-1);
    const match=workflow.slice(start).match(/        run: \|\n((?:          [^\n]*\n)+)/);
    assert.ok(match,"Cannot locate the exact workflow selection script");
    const script=match[1].replace(/^          /gm,"");
    const output=join(directory,"outputs");
    const marker=join(directory,"must-not-exist");
    const cases=[
      {event:"push",type:"tag",ref:"v0.1.0",tag:"",expected:"v0.1.0"},
      {event:"workflow_dispatch",type:"branch",ref:"master",tag:"v1.2.3",expected:"v1.2.3"},
      {event:"workflow_dispatch",type:"branch",ref:"main",tag:"v2.0.0",expected:"v2.0.0"},
      {event:"workflow_dispatch",type:"branch",ref:"v9.9.9",tag:"",expected:null},
      {event:"push",type:"branch",ref:"v1.2.3",tag:"",expected:null},
      {event:"pull_request",type:"branch",ref:"v1.2.3",tag:"",expected:null},
      {event:"workflow_dispatch",type:"branch",ref:"main",tag:"v01.2.3",expected:null},
      {event:"workflow_dispatch",type:"branch",ref:"main",tag:"v1.2.3-beta",expected:null},
      {event:"workflow_dispatch",type:"branch",ref:"main",tag:`v1.2.3$(touch ${marker})`,expected:null},
      {event:"workflow_dispatch",type:"branch",ref:"main",tag:"v1.2.3\ninvalid",expected:null},
    ];
    for (const entry of cases) {
      await writeFile(output,"");
      let code=0;
      try {
        await execute("bash",["-e","-o","pipefail","-c",script],{timeout:commandTimeoutMs,maxBuffer:outputBytesMax,env:{...process.env,EVENT_NAME:entry.event,REF_TYPE:entry.type,REF_NAME:entry.ref,REQUESTED_TAG:entry.tag,GITHUB_OUTPUT:output}});
      } catch(failure) { if(typeof failure.code!=="number") throw failure; code=failure.code; }
      if(entry.expected) {
        assert.equal(code,0,JSON.stringify(entry));
        assert.equal(await readFile(output,"utf8"),`tag=${entry.expected}\n`);
      } else {
        assert.notEqual(code,0,JSON.stringify(entry));
        assert.equal(await readFile(output,"utf8"),"");
      }
    }
    await assert.rejects(stat(marker),{code:"ENOENT"});
  } finally { await rm(directory,{recursive:true,force:true}); }
});

test("release gates the exact tag commit before starting native builds", () => {
  assert.match(workflow, /  validation:\n    if: github\.repository == 'dastagirS\/docket'/);
  assert.match(workflow, /permissions:\n      contents: read\n      actions: read/);
  assert.match(workflow, /ref: refs\/tags\/\$\{\{ steps\.version\.outputs\.tag \}\}/);
  assert.match(workflow, /  build:\n    needs: validation\n    uses: \.\/\.github\/workflows\/checks\.yml\n    with:\n      tag: \$\{\{ needs\.validation\.outputs\.tag \}\}\n      commit: \$\{\{ needs\.validation\.outputs\.commit \}\}\n      upload_release_assets: true/);
  assert.match(checksWorkflow, /ref: \$\{\{ inputs\.commit \|\| github\.sha \}\}/);
  assert.match(workflow, /RELEASE_TAG: \$\{\{ needs\.build\.outputs\.tag \}\}/);
  assert.match(workflow, /SOURCE_COMMIT: \$\{\{ needs\.build\.outputs\.commit \}\}/);
  assert.match(workflow, /group: release-\$\{\{ inputs\.tag \|\| github\.ref_name \}\}/);
  assert.match(checksWorkflow, /BUILD_VERSION: \$\{\{ inputs\.tag \|\| 'dev' \}\}/);
  assert.match(checksWorkflow, /DOCKET_EXPECTED_BUILD_VERSION: \$\{\{ inputs\.tag \|\| 'dev' \}\}/);
  assert.match(checksWorkflow, /tag:\n        value: \$\{\{ jobs\.build\.outputs\.tag \}\}/);
  assert.match(checksWorkflow, /commit:\n        value: \$\{\{ jobs\.build\.outputs\.commit \}\}/);
  assert.match(checksWorkflow, /commit: \$\{\{ steps\.checkout\.outputs\.commit \}\}/);
  assert.match(checksWorkflow, /fail-fast: true/);
  assert.match(workflow, /needs: build/);
  assert.match(workflow, /current\['id'\] != release_identifier/);
});

test("push checks remain required but release builds do not rerun them", async () => {
  for (const [name, operatingSystem] of [
    ["Native Apple Silicon Go checks", "macOS"],
    ["Native macOS vulnerability checks", "macOS"],
    ["Go tests, race detection, vet, and frontend type checks", "Linux"],
    ["Build and verify Linux validation executable", "Linux"],
  ]) assert.ok(checksWorkflow.includes(`      - name: ${name}\n        if: runner.os == '${operatingSystem}' && !inputs.upload_release_assets\n`), `${name} must run on pushes only`);
  assert.ok(checksWorkflow.includes("      - name: Real CLI and embedded-skill checks\n        if: ${{ !inputs.upload_release_assets }}\n"));
  for (const command of ["make test", "make --jobs=1 build security verify-portable", "tests/cli.test.mjs", "tests/skill.test.mjs", "tests/install.test.mjs", "tests/workflow.test.mjs", "tests/release.test.mjs"]) assert.ok(checksWorkflow.includes(command), `Missing push gate: ${command}`);
  assert.match(checksWorkflow, /RUN_CHECKS: \$\{\{ !inputs\.upload_release_assets \}\}/);
  assert.match(checksWorkflow, /npm --prefix frontend run build:assets/);
  assert.equal(JSON.parse(await readFile("frontend/package.json", "utf8")).scripts["build:assets"], "vite build");
  assert.match(checksWorkflow, /cache: npm\n          cache-dependency-path: frontend\/package-lock\.json/);
  assert.match(checksWorkflow, /if: runner.os == 'Linux' && inputs\.upload_release_assets[\s\S]*?run: make build FRONTEND_CHECK=false/);
  const { stdout } = await execute("make", ["--dry-run", "build", "BUILD_VERSION=v0.0.4", "FRONTEND_CHECK=false", "BUILD_DIRECTORY=release-command-proof"], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax });
  assert.match(stdout, /--target artifact/);
  assert.match(stdout, /--build-arg FRONTEND_CHECK="false"/);
  assert.doesNotMatch(stdout, /--target (?:test|security|portable-test)/);
  const dockerfile = await readFile("Dockerfile", "utf8");
  assert.match(dockerfile, /false\) npm run build:assets/);
  assert.match(dockerfile, /qemu-x86_64 -cpu qemu64 \/build\/docket[^\n]*--backup-to/);
  assert.doesNotMatch(dockerfile, /go test -exec.*qemu/);
});

test("validation checks every branch push read-only, cancels stale runs, and never opts into release assets", () => {
  assert.match(validationWorkflow, /'on':\n  push:\n    branches:\n      - '\*\*'/);
  assert.doesNotMatch(validationWorkflow, /\b(?:tags|pull_request|workflow_dispatch):/);
  assert.match(validationWorkflow, /permissions:\n  contents: read/);
  assert.match(validationWorkflow, /if: github\.event\.deleted != true/);
  assert.match(validationWorkflow, /group: validation-\$\{\{ github\.ref \}\}\n  cancel-in-progress: true/);
  assert.match(validationWorkflow, /uses: \.\/\.github\/workflows\/checks\.yml\n    with:\n      upload_release_assets: false/);
  assert.doesNotMatch(validationWorkflow, /\bpublish:|contents: write|secrets:|upload-artifact@/);
  assert.match(checksWorkflow, /workflow_call:/);
  assert.match(checksWorkflow, /upload_release_assets:\n        description:[^\n]*\n        type: boolean\n        default: false/);
  assert.match(checksWorkflow, /permissions:\n  contents: read/);
  assert.doesNotMatch(checksWorkflow, /contents: write|GH_TOKEN|secrets:/);
  for (const step of ["Package release assets"]) {
    assert.ok(checksWorkflow.includes(`      - name: ${step}\n        if: inputs.upload_release_assets\n`), `${step} must be release-only`);
  }
  assert.match(checksWorkflow, /- if: inputs\.upload_release_assets\n        uses: actions\/upload-artifact@[a-f0-9]{40}/);
  assert.match(checksWorkflow, /persist-credentials: false/);
  assert.match(checksWorkflow, /timeout-minutes: 30/);
});

test("Linux release build, security, and portability share the selected version", { timeout: 15000 }, async () => {
  const version = "v0.0.4";
  const { stdout } = await execute("make", ["--dry-run", "--jobs=1", "build", "security", "verify-portable", `BUILD_VERSION=${version}`, "BUILD_DIRECTORY=release-command-proof"], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax });
  const commands = stdout.trim().split("\n");
  assert.deepEqual(commands.map(command => command.match(/--target ([^ ]+)/)?.[1]), ["artifact", "security", "portable-test"]);
  for (const command of commands) assert.ok(command.includes(`--build-arg BUILD_VERSION="${version}"`), "Every release target must use the same stamped build: " + command);
});

const commitHexLength = 40;
const gateRequestsMax = 2;
const gateResponseBytesMax = 1024 * 1024;

async function verifyValidationGate(scenario) {
  const commit = "1".repeat(commitHexLength);
  const attempt = 2;
  const run = { id: 101, run_attempt: attempt, head_sha: commit, path: ".github/workflows/validation.yml", event: "push", head_repository: { full_name: "dastagirS/docket" }, status: "completed", conclusion: "success" };
  const required = [
    ["docket-linux-amd64", ["Go tests, race detection, vet, and frontend type checks", "Build and verify Linux validation executable", "Real CLI and embedded-skill checks"]],
    ["docket-darwin-arm64", ["Native Apple Silicon Go checks", "Native macOS vulnerability checks", "Real CLI and embedded-skill checks"]],
  ];
  const jobs = required.map(([name, names]) => ({ name: `checks / ${name}`, head_sha: commit, run_attempt: attempt, status: "completed", conclusion: "success", steps: names.map(name => ({ name, status: "completed", conclusion: "success" })) }));
  for (const job of jobs) for (const step of job.steps) assert.ok(checksWorkflow.includes(`- name: ${step.name}\n`), "Gate fixture must match real workflow checks");
  if (["failure", "cancelled", "skipped", "timed_out", "neutral"].includes(scenario)) run.conclusion = scenario;
  if (["queued", "in_progress"].includes(scenario)) { run.status = scenario; run.conclusion = null; }
  if (scenario === "latest-failed") run.conclusion = "failure";
  if (scenario === "wrong-commit") run.head_sha = "2".repeat(commitHexLength);
  if (scenario === "wrong-workflow") run.path = ".github/workflows/unrelated.yml";
  if (scenario === "wrong-event") run.event = "pull_request";
  if (scenario === "wrong-repository") run.head_repository.full_name = "other/repository";
  if (scenario === "missing-native") jobs.pop();
  if (scenario === "duplicate-native") jobs.push(jobs[0]);
  if (scenario === "failed-native") jobs[0].conclusion = "failure";
  if (scenario === "skipped-step") jobs[0].steps[0].conclusion = "skipped";
  if (scenario === "missing-step") jobs[0].steps.shift();
  if (scenario === "old-attempt") jobs[0].run_attempt = attempt - 1;
  if (scenario === "wrong-job-commit") jobs[0].head_sha = "2".repeat(commitHexLength);
  if (scenario === "no-jobs") jobs.length = 0;
  const requests = [];
  let fixtureError;
  const server = createServer((request, response) => {
    try {
      assert.ok(requests.length < gateRequestsMax, "Gate must not retry or poll");
      assert.equal(request.method, "GET");
      assert.equal(request.headers.authorization, "Bearer fixture-token");
      const url = new URL(request.url, "http://localhost");
      requests.push(url.pathname);
      if (scenario === "api-error") { response.writeHead(403); response.end(); return; }
      if (scenario === "redirect") { response.writeHead(302, { Location: "/must-not-forward-token" }); response.end(); return; }
      if (scenario === "oversized") { response.end(" ".repeat(gateResponseBytesMax + 1)); return; }
      if (scenario === "invalid-json") { response.end("not JSON"); return; }
      if (requests.length === 1) {
        assert.equal(url.pathname, "/repos/dastagirS/docket/actions/workflows/validation.yml/runs");
        assert.equal(url.searchParams.get("head_sha"), commit);
        assert.equal(url.searchParams.get("event"), "push");
        assert.equal(url.searchParams.get("per_page"), "1");
        assert.equal(url.searchParams.has("status"), false, "Must not hide newer failed or pending runs by filtering for success");
        response.end(JSON.stringify({ total_count: scenario === "missing" ? 0 : 2, workflow_runs: scenario === "missing" ? [] : [run] }));
      } else {
        assert.equal(url.pathname, `/repos/dastagirS/docket/actions/runs/${run.id}/attempts/${attempt}/jobs`);
        assert.equal(url.searchParams.get("per_page"), "100");
        response.end(JSON.stringify({ total_count: jobs.length + (scenario === "incomplete-jobs" ? 1 : 0), jobs }));
      }
    } catch (error) { fixtureError = error; response.writeHead(500); response.end(); }
  });
  server.requestTimeout = commandTimeoutMs;
  server.headersTimeout = commandTimeoutMs;
  server.maxConnections = 2;
  try {
    const listening = once(server, "listening", { signal: AbortSignal.timeout(commandTimeoutMs) });
    server.listen(0, "127.0.0.1");
    await listening;
    const start = workflow.indexOf("      - name: Require successful push validation\n");
    assert.notEqual(start, -1);
    const match = workflow.slice(start).match(/        run: \|\n((?:(?:          [^\n]*)?\n)+)/);
    assert.ok(match, "Cannot locate the real validation gate script");
    const script = match[1].replace(/^          /gm, "").replaceAll("https://api.github.com", `http://127.0.0.1:${server.address().port}`);
    let result;
    try {
      result = { code: 0, ...await execute("bash", ["-e", "-o", "pipefail", "-c", script], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax, env: { ...process.env, GH_REPO: "dastagirS/docket", GH_TOKEN: "fixture-token", SOURCE_COMMIT: commit } }) };
    } catch (error) {
      if (typeof error.code !== "number") throw error;
      result = error;
    }
    if (fixtureError) throw fixtureError;
    assert.doesNotMatch(result.stdout + result.stderr, /fixture-token/);
    return { ...result, requests };
  } finally {
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
  }
}

test("release gate accepts only successful native checks for the exact commit and attempt", { timeout: 30000 }, async () => {
  const result = await verifyValidationGate("success");
  assert.equal(result.code, 0, result.stderr);
  assert.equal(result.requests.length, gateRequestsMax);
  assert.match(result.stdout, /successful push validation/);
});

test("release gate refuses missing, failed, pending, skipped, mismatched or unreadable checks without retries", { timeout: 30000 }, async () => {
  for (const scenario of ["missing", "failure", "cancelled", "skipped", "timed_out", "neutral", "queued", "in_progress", "latest-failed", "wrong-commit", "wrong-workflow", "wrong-event", "wrong-repository", "missing-native", "duplicate-native", "failed-native", "skipped-step", "missing-step", "old-attempt", "wrong-job-commit", "no-jobs", "incomplete-jobs", "api-error", "redirect", "oversized", "invalid-json"]) {
    const result = await verifyValidationGate(scenario);
    assert.equal(result.code, 1, scenario);
    assert.match(result.stderr, /Release blocked:/, scenario);
    assert.ok(result.requests.length <= gateRequestsMax, scenario);
  }
});

const digestHexLength = 64;
const publicReleaseIdentifier = 101;
const otherDraftIdentifier = 202;
const createdReleaseIdentifier = 303;
const sourceCommit = "1".repeat(commitHexLength);
const releaseTag = "v0.0.1";
const assetNames = ["docket-linux-amd64", "docket-darwin-arm64", "SHA256SUMS"];
const publisherTimeoutMs = 10000;
const testTimeoutMs = 30000;
const requestBytesMax = 1048576;
const requestsMax = 30;
const checksum = value => createHash("sha256").update(value).digest("hex");

async function verifyPublication(scenario, callback) {
  const directory = await mkdtemp(join(tmpdir(), "docket-publish-"));
  const requests = [];
  let tagLookups = 0;
  const releases = new Map();
  const payloads = new Map([[assetNames[0], Buffer.from("fixture executable")], [assetNames[1], Buffer.from("fixture macOS executable")]]);
  payloads.set("SHA256SUMS", Buffer.from([...payloads].map(([name, value]) => `${checksum(value)}  ${name}\n`).join("")));
  const asset = (name, value) => ({ name, size: value.length, state: "uploaded", digest: `sha256:${checksum(value)}` });
  if (!scenario.startsWith("new")) releases.set(publicReleaseIdentifier, { id: publicReleaseIdentifier, tag_name: releaseTag, draft: ["draft", "wrong-public-release", "ambiguous-draft"].includes(scenario), body: "Maintainer's release notes", assets: [] });
  // A same-tag draft must not intercept repairs to the already-published release.
  releases.set(otherDraftIdentifier, { id: otherDraftIdentifier, tag_name: ["existing", "partial", "matching", "conflict", "ambiguous-draft"].includes(scenario) ? releaseTag : "v9.9.9", draft: true, assets: [], body: "Unrelated draft" });
  const taggedInstaller = Buffer.from("#!/bin/sh\nexit 0\n");
  if (scenario === "tagged-installer") releases.get(publicReleaseIdentifier).assets = [...payloads, ["install.sh", taggedInstaller]].map(([name, value]) => asset(name, value));
  if (scenario === "partial") releases.get(publicReleaseIdentifier).assets = [asset(assetNames[0], payloads.get(assetNames[0]))];
  if (scenario === "matching") releases.get(publicReleaseIdentifier).assets = [...payloads].map(([name, value]) => asset(name, value));
  if (scenario === "conflict") releases.get(publicReleaseIdentifier).assets = [asset(assetNames[0], Buffer.from("different executable"))];
  const server = createServer(async (request, response) => {
    try {
      assert.ok(requests.length < requestsMax, "Unexpected request count");
      assert.equal(request.headers.authorization, "Bearer fixture-token");
      const url = new URL(request.url, "http://localhost");
      const path = decodeURIComponent(url.pathname);
      requests.push({ method: request.method, path });
      let length = 0;
      const chunks = [];
      for await (const chunk of request) {
        length += chunk.length;
        assert.ok(length <= requestBytesMax);
        chunks.push(chunk);
      }
      const body = Buffer.concat(chunks);
      const send = (status, value) => { response.writeHead(status, { "Content-Type": "application/json" }); response.end(JSON.stringify(value)); };
      if (scenario === "api-error") return send(403, { message: "Forbidden" });
      if (scenario === "api-redirect") { response.writeHead(302, { Location: "/must-not-forward-credentials" }); return response.end(); }
      if (path.includes("/commits/")) {
        tagLookups += 1;
        return send(200, { sha: scenario === "moved-tag" || (scenario === "new-tag-moved" && tagLookups > 1) ? "2".repeat(commitHexLength) : sourceCommit });
      }
      if (request.method === "GET" && path.endsWith("/releases")) {
        if (scenario === "new-list-error") return send(403, { message: "Forbidden" });
        if (scenario === "new-list-limit") return send(200, Array.from({ length: 100 }, () => ({ id: otherDraftIdentifier, tag_name: "v9.9.9", draft: true, assets: [] })));
        return send(200, [...releases.values()]);
      }
      if (request.method === "GET" && path.endsWith(`/releases/tags/${releaseTag}`)) {
        // GitHub documents this endpoint as returning published releases, not drafts.
        const release = [...releases.values()].find(value => value.tag_name === releaseTag && !value.draft);
        if (release?.assets.length === assetNames.length && scenario === "missing-public-asset") return send(200, { ...release, assets: release.assets.slice(1) });
        if (release && !release.draft && scenario === "wrong-public-release") return send(200, { ...releases.get(otherDraftIdentifier), draft: false });
        return release ? send(200, release) : send(404, { message: "Not Found" });
      }
      if (request.method === "POST" && path.endsWith("/releases")) {
        const value = JSON.parse(body);
        const release = { ...value, id: createdReleaseIdentifier, assets: [] };
        releases.set(createdReleaseIdentifier, release);
        return send(201, release);
      }
      const match = path.match(/\/releases\/(\d+)(\/assets)?$/);
      if (match) {
        const release = releases.get(Number(match[1]));
        assert.ok(release);
        if (request.method === "POST" && match[2]) {
          if (["upload-error", "new-upload-error"].includes(scenario) && release.assets.length === 1) return send(502, { message: "Upload failed" });
          const name = url.searchParams.get("name");
          assert.ok(assetNames.includes(name));
          assert.ok(!release.assets.some(value => value.name === name), "An existing asset must never be overwritten");
          const value = asset(name, body);
          if (scenario === "bad-digest") value.digest = `sha256:${"0".repeat(digestHexLength)}`;
          release.assets.push(value);
          return send(201, value);
        }
        if (request.method === "PATCH") {
          Object.assign(release, JSON.parse(body));
          return send(200, release);
        }
      }
      send(404, { message: "Unknown endpoint" });
    } catch (error) {
      response.writeHead(500); response.end("Fixture assertion failed");
      server.fixtureError = error;
    }
  });
  server.requestTimeout = publisherTimeoutMs;
  server.headersTimeout = publisherTimeoutMs;
  server.maxConnections = 4;
  try {
    await mkdir(join(directory, "release-assets"));
    for (const name of assetNames.slice(0, -1)) {
      const folder = join(directory, "release-assets", `release-${name}`);
      await mkdir(folder);
      await writeFile(join(folder, name), payloads.get(name));
      await writeFile(join(folder, "SHA256SUMS"), `${checksum(payloads.get(name))}  ${name}\n`);
      await writeFile(join(folder, "SOURCE_COMMIT"), `${scenario === "artifact-commit" && name === "docket-darwin-arm64" ? "2".repeat(commitHexLength) : sourceCommit}\n`);
      if (scenario.endsWith("tagged-installer")) await writeFile(join(folder, "install.sh"), taggedInstaller);
      if (scenario === "artifact-corrupt" && name === "docket-darwin-arm64") await writeFile(join(folder, name), "different artifact");
    }
    server.listen(0, "127.0.0.1"); await once(server, "listening");
    const origin = `http://127.0.0.1:${server.address().port}`;
    const start = workflow.indexOf("      - name: Verify artifacts and publish through a draft\n");
    assert.notEqual(start, -1);
    const match = workflow.slice(start).match(/        run: \|\n((?:(?:          [^\n]*)?\n)+)/);
    assert.ok(match, "Cannot find publication script");
    const script = match[1].replace(/^          /gm, "").replaceAll("https://api.github.com", origin).replaceAll("https://uploads.github.com", origin);
    let code = 0;
    try {
      await execute("bash", ["-e", "-o", "pipefail", "-c", script], {
        cwd: directory, timeout: publisherTimeoutMs, maxBuffer: outputBytesMax,
        env: { ...process.env, GH_TOKEN: "fixture-token", GH_REPO: "owner/repo", RELEASE_TAG: releaseTag, SOURCE_COMMIT: sourceCommit },
      });
    } catch (error) { if (typeof error.code !== "number") throw error; code = error.code; }
    if (server.fixtureError) throw server.fixtureError;
    await callback({ code, releases, requests, payloads });
  } finally {
    server.closeAllConnections();
    await new Promise(resolve => server.close(resolve));
    await rm(directory, { recursive: true, force: true });
  }
}

test("publisher attaches verified files to the existing public release, not a same-tag draft", { timeout: testTimeoutMs }, async () => {
  for (const scenario of ["existing", "partial"]) await verifyPublication(scenario, async ({ code, releases, payloads }) => {
    assert.equal(code, 0);
    assert.deepEqual(releases.get(publicReleaseIdentifier).assets.map(value => value.name).sort(), [...payloads.keys()].sort());
    assert.equal(releases.get(publicReleaseIdentifier).body, "Maintainer's release notes");
    assert.equal(releases.get(publicReleaseIdentifier).draft, false);
    assert.deepEqual(releases.get(otherDraftIdentifier).assets, []);
    assert.equal(releases.get(otherDraftIdentifier).draft, true);
  });
});

test("publisher creates or resumes a draft by exact ID and confirms its published assets", { timeout: testTimeoutMs }, async () => {
  for (const scenario of ["new", "draft"]) await verifyPublication(scenario, async ({ code, releases, requests }) => {
    assert.equal(code, 0);
    const release = releases.get(scenario === "new" ? createdReleaseIdentifier : publicReleaseIdentifier);
    assert.equal(release.draft, false);
    assert.equal(release.assets.length, assetNames.length);
    assert.ok(requests.some(request => request.method === "PATCH" && request.path.endsWith(`/releases/${release.id}`)));
  });
});

test("publisher preserves matching assets and rejects conflicting assets without writes", { timeout: testTimeoutMs }, async () => {
  for (const scenario of ["matching", "conflict", "moved-tag", "api-error", "api-redirect"]) await verifyPublication(scenario, async ({ code, requests }) => {
    assert.equal(code, scenario === "matching" ? 0 : 1);
    assert.ok(requests.every(request => request.method === "GET"));
  });
});

test("publisher fails rather than reporting success on partial uploads or invalid server digests", { timeout: testTimeoutMs }, async () => {
  for (const scenario of ["upload-error", "new-upload-error", "new-tag-moved", "bad-digest", "missing-public-asset", "wrong-public-release"]) await verifyPublication(scenario, async ({ code, requests }) => {
    assert.equal(code, 1);
    if (scenario !== "wrong-public-release") assert.ok(!requests.some(request => request.method === "PATCH"));
  });
});


test("publisher refuses ambiguous drafts, failed listings, or incomplete bounded lookup without writes", { timeout: testTimeoutMs }, async () => {
  for (const scenario of ["ambiguous-draft", "new-list-error", "new-list-limit"]) await verifyPublication(scenario, async ({ code, requests }) => {
    assert.equal(code, 1);
    assert.ok(requests.every(request => request.method === "GET"));
    assert.ok(requests.some(request => request.path.endsWith("/releases")));
  });
});



test("release workflow requires native Linux and Apple Silicon assets before publication", () => {
  assert.match(checksWorkflow, /runner: ubuntu-24\.04/);
  assert.match(checksWorkflow, /runner: macos-15/);
  assert.match(checksWorkflow, /asset: docket-darwin-arm64/);
  assert.match(checksWorkflow, /codesign --verify --strict/);
  assert.match(checksWorkflow, /SOURCE_COMMIT/);
  assert.match(workflow, /merge-multiple: false/);
});

test("publisher ignores surplus installer artifacts and leaves existing release files untouched", { timeout: testTimeoutMs }, async () => {
  for (const scenario of ["tagged-installer", "new-tagged-installer"]) await verifyPublication(scenario, async ({ code, releases, requests }) => {
    assert.equal(code, 0);
    if (scenario === "tagged-installer") {
      assert.ok(requests.every(request => request.method === "GET"));
      assert.ok(releases.get(publicReleaseIdentifier).assets.some(asset => asset.name === "install.sh"));
    } else assert.deepEqual(releases.get(createdReleaseIdentifier).assets.map(asset => asset.name).sort(), [...assetNames].sort());
  });
});

test("publisher rejects mixed-commit or corrupt platform artifacts before contacting GitHub", { timeout: testTimeoutMs }, async () => {
  for (const scenario of ["artifact-commit", "artifact-corrupt"]) await verifyPublication(scenario, async ({ code, requests }) => {
    assert.equal(code, 1);
    assert.deepEqual(requests, []);
  });
});
