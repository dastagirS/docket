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

test("manual release checks out the validated tag and retains all pre-publication gates", () => {
  assert.match(workflow,/ref: refs\/tags\/\$\{\{ steps\.version\.outputs\.tag \}\}/);
  assert.match(workflow,/RELEASE_TAG: \$\{\{ steps\.version\.outputs\.tag \}\}/);
  assert.match(workflow,/group: release-\$\{\{ inputs\.tag \|\| github\.ref_name \}\}/);
  for(const command of ["make test","make security","make verify-portable","tests/cli.test.mjs","tests/skill.test.mjs","tests/install.test.mjs","tests/workflow.test.mjs"]) assert.ok(workflow.includes(command),`Missing gate: ${command}`);
  assert.match(workflow,/needs: build/);
  assert.match(workflow,/current\['id'\] != release_identifier/);
});

const commitHexLength = 40;
const digestHexLength = 64;
const publicReleaseIdentifier = 101;
const otherDraftIdentifier = 202;
const createdReleaseIdentifier = 303;
const sourceCommit = "1".repeat(commitHexLength);
const releaseTag = "v0.0.1";
const assetNames = ["docket-linux-amd64", "install.sh", "SHA256SUMS"];
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
  const payloads = new Map([[assetNames[0], Buffer.from("fixture executable")], [assetNames[1], Buffer.from("#!/bin/sh\nexit 0\n")]]);
  payloads.set("SHA256SUMS", Buffer.from([...payloads].map(([name, value]) => `${checksum(value)}  ${name}\n`).join("")));
  const asset = (name, value) => ({ name, size: value.length, state: "uploaded", digest: `sha256:${checksum(value)}` });
  if (!scenario.startsWith("new")) releases.set(publicReleaseIdentifier, { id: publicReleaseIdentifier, tag_name: releaseTag, draft: ["draft", "wrong-public-release", "ambiguous-draft"].includes(scenario), body: "Maintainer's release notes", assets: [] });
  // A same-tag draft must not intercept repairs to the already-published release.
  releases.set(otherDraftIdentifier, { id: otherDraftIdentifier, tag_name: ["existing", "partial", "matching", "conflict", "ambiguous-draft"].includes(scenario) ? releaseTag : "v9.9.9", draft: true, assets: [], body: "Unrelated draft" });
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
    for (const [name, payload] of payloads) await writeFile(join(directory, "release-assets", name), payload);
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
