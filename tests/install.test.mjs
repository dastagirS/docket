import test from "node:test";
import assert from "node:assert/strict";
import { execFile, spawn } from "node:child_process";
import { promisify } from "node:util";
import { createServer } from "node:http";
import { once } from "node:events";
import { setTimeout as delay } from "node:timers/promises";
import { createHash } from "node:crypto";
import { mkdtemp, mkdir, readFile, readdir, writeFile, rm, stat, open, symlink } from "node:fs/promises";
import { join, resolve } from "node:path";
import { tmpdir } from "node:os";

const execute = promisify(execFile);
const installer = resolve("install.sh");
const packager = resolve("scripts/package-release.sh");
const binary = resolve(process.env.DOCKET_BINARY ?? "bin/docket");
const commandTimeoutMs = 15000;
const outputBytesMax = 1048576;
const fixtureRequestsMax = 20;
const fixtureConnectionsMax = 8;
const manifestBytesMax = 16384;
const checksum = value => createHash("sha256").update(value).digest("hex");
const artifactName = "docket-linux-amd64";

async function runInstaller(fixture, environment = {}, arguments_ = [], input) {
  try {
    const pending = execute("sh", input === undefined ? [installer, ...arguments_] : arguments_, {
      cwd: fixture.directory, timeout: commandTimeoutMs, maxBuffer: outputBytesMax,
      env: { ...process.env, HOME: fixture.home, TMPDIR: fixture.temporary, PATH: `${fixture.tools}:${process.env.PATH}`, DOCKET_INSTALL_DIR: fixture.destination, DOCKET_VERSION: "latest", DOCKET_URL: "http://127.0.0.1:0", NO_PROXY: "127.0.0.1,localhost", no_proxy: "127.0.0.1,localhost", TEST_RELEASE_ORIGIN: fixture.origin, TEST_CURL: fixture.curl, ...environment },
    });
    pending.child.stdin.end(input);
    const result = await pending;
    return { ...result, code: 0 };
  } catch (failure) {
    if (typeof failure.code !== "number") throw failure;
    return { code: failure.code, stdout: failure.stdout, stderr: failure.stderr };
  }
}

async function fixtureFor(payload, manifest = `${checksum(payload)}  ${artifactName}\n`, responseMode = "normal") {
  const directory = await mkdtemp(join(tmpdir(), "docket-installer-"));
  const tools = join(directory, "tools");
  const home = join(directory, "home");
  const temporary = join(directory, "temporary");
  await Promise.all([tools,home,temporary].map(path => mkdir(path)));
  const curl = (await execute("sh", ["-c", "command -v curl"], { timeout: commandTimeoutMs })).stdout.trim();
  // Only the fixture transport is rewritten; production remains HTTPS-only with bounded downloads.
  await writeFile(join(tools, "curl"), `#!/usr/bin/env node
const {spawnSync}=require('node:child_process');
const arguments_=process.argv.slice(2);
if(arguments_.length===2 && arguments_[0]==='--disable' && arguments_[1]==='--version') {
 if(process.env.TEST_CURL_VERSION) { console.log('curl '+process.env.TEST_CURL_VERSION); process.exit(0); }
 const result=spawnSync(process.env.TEST_CURL,arguments_,{stdio:'inherit',timeout:${commandTimeoutMs}});
 if(result.error) throw result.error;
 process.exit(result.status??1);
}
for(const flag of ['--proto','--proto-redir']) {
 const index=arguments_.indexOf(flag);
 if(index<0 || arguments_[index+1]!=='=https') throw new Error('Installer lost HTTPS restriction');
 arguments_[index+1]='=http';
}
const location=new URL(arguments_[arguments_.length-1]);
if(location.origin!=='https://github.com' || !location.pathname.startsWith('/dastagirS/docket/releases/')) throw new Error('Unexpected release origin');
arguments_[arguments_.length-1]=process.env.TEST_RELEASE_ORIGIN+location.pathname;
const result=spawnSync(process.env.TEST_CURL,arguments_,{stdio:'inherit',timeout:${commandTimeoutMs}});
if(result.error) throw result.error;
process.exit(result.status??1);
`, { mode: 0o755 });
  const requests = [];
  const server = createServer((request,response) => {
    if (requests.length >= fixtureRequestsMax) { response.writeHead(429); response.end(); return; }
    requests.push(request.url);
    if(responseMode==="redirect") { response.writeHead(302,{Location:request.url}); response.end(); return; }
    if(responseMode==="oversized") { response.writeHead(200,{"Content-Length":manifestBytesMax+1}); response.end(); return; }
    const body = request.url.endsWith("/SHA256SUMS") ? manifest : request.url.endsWith(`/${artifactName}`) ? payload : null;
    if (body === null) { response.writeHead(404); response.end(); return; }
    response.writeHead(200, { "Content-Length": Buffer.byteLength(body), "Content-Type": "application/octet-stream" });
    response.end(body);
  });
  server.requestTimeout=commandTimeoutMs;
  server.headersTimeout=commandTimeoutMs;
  server.maxConnections=fixtureConnectionsMax;
  await new Promise((resolve,reject) => { server.once("error",reject); server.listen(0,"127.0.0.1",resolve); });
  return {
    directory, tools, home, temporary, curl, requests, destination: join(home,".local","bin"), origin:`http://127.0.0.1:${server.address().port}`,
    async close() { try { await new Promise((resolve,reject) => server.close(error => error ? reject(error) : resolve())); } finally { await rm(directory,{recursive:true,force:true}); } },
  };
}

test("release packaging produces only the real binary and verifiable checksums without overwriting output", { timeout: 30000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(),"docket-package-"));
  try {
    const output = join(directory,"release");
    await execute("sh",[packager,"v0.1.0",binary,output],{timeout:commandTimeoutMs});
    assert.deepEqual((await readdir(output)).sort(),["SHA256SUMS",artifactName].sort());
    assert.equal(checksum(await readFile(join(output,artifactName))),checksum(await readFile(binary)));
    assert.ok((await stat(installer)).isFile(), "Repository installer must remain available");
    await execute("sha256sum",["--check","SHA256SUMS"],{cwd:output,timeout:commandTimeoutMs});
    await assert.rejects(execute("sh",[packager,"v0.1.0",binary,output],{timeout:commandTimeoutMs}),/Output directory must not already exist/);
    for (const version of ["latest","v01.2.3","v1.2.3-beta","v1.2.3\ninvalid"]) {
      await assert.rejects(execute("sh",[packager,version,binary,join(directory,"invalid")],{timeout:commandTimeoutMs}));
    }
    await assert.rejects(stat(join(directory,"invalid")),{code:"ENOENT"});
  } finally { await rm(directory,{recursive:true,force:true}); }
});

test("installer latest and pinned releases atomically replace only the target and run the real embedded CLI", { timeout: 60000 }, async () => {
  const payload = await readFile(binary);
  for (const version of ["latest","v0.1.0"]) {
    const fixture = await fixtureFor(payload);
    let previous;
    if(version!=="latest") fixture.destination=join(fixture.home,"custom bin");
    try {
      await mkdir(fixture.destination,{recursive:true});
      const target = join(fixture.destination,"docket");
      await writeFile(target,"previous executable",{mode:0o755});
      const before = await stat(target);
      previous = await open(target,"r");
      const result = await runInstaller(fixture,{DOCKET_VERSION:version,DOCKET_INSTALL_DIR:version==="latest" ? "" : fixture.destination},[],version==="latest" ? await readFile(installer) : undefined);
      assert.equal(result.code,0,result.stderr);
      assert.equal(await previous.readFile("utf8"),"previous executable","Open old inode must not be truncated");
      assert.notEqual((await stat(target)).ino,before.ino,"Installation must replace atomically rather than truncate");
      assert.equal((await stat(target)).mode & 0o777,0o755);
      assert.equal(checksum(await readFile(target)),checksum(payload));
      const skill = await execute(target,["--skill"],{cwd:fixture.directory,timeout:commandTimeoutMs,env:{...process.env,DOCKET_URL:"http://127.0.0.1:0"}});
      assert.match(skill.stdout,/name: docket-cli/);
      assert.deepEqual(await readdir(fixture.temporary),[]);
      assert.deepEqual(await readdir(fixture.destination),["docket"]);
      assert.deepEqual(fixture.requests.map(path => path.split("/").slice(0,-1).join("/")),Array(2).fill(version==="latest" ? "/dastagirS/docket/releases/latest/download" : "/dastagirS/docket/releases/download/v0.1.0"));
      await assert.rejects(stat(join(fixture.directory,"data")),{code:"ENOENT"});
    } finally { if(previous) await previous.close(); await fixture.close(); }
  }
});

test("installer rejects corrupt, missing, duplicate, malformed checksums and non-ELF assets without replacing existing installs", { timeout: 60000 }, async () => {
  const invalid = Buffer.from("not a release binary");
  const digest = checksum(invalid);
  for (const [name,manifest] of [
    ["mismatch",`${"0".repeat(64)}  ${artifactName}\n`],
    ["missing",`${digest}  different-file\n`],
    ["duplicate",`${digest}  ${artifactName}\n${digest}  ${artifactName}\n`],
    ["malformed",`not-a-hash  ${artifactName}\n`],
    ["wrong-format",`${digest}  ${artifactName}\n`],
    ["http-error",null],
  ]) {
    const fixture = await fixtureFor(invalid,manifest);
    try {
      await mkdir(fixture.destination,{recursive:true});
      await writeFile(join(fixture.destination,"docket"),"keep existing");
      const result = await runInstaller(fixture);
      assert.notEqual(result.code,0,name);
      assert.equal(await readFile(join(fixture.destination,"docket"),"utf8"),"keep existing",name);
      assert.deepEqual(await readdir(fixture.temporary),[],name);
      if (["missing","duplicate","malformed"].includes(name)) assert.equal(fixture.requests.length,1,"Do not download binary after bad manifest");
    } finally { await fixture.close(); }
  }
});

test("installer rejects unsupported platforms, invalid versions and unsafe destinations before downloading", { timeout: 60000 }, async () => {
  const fixture = await fixtureFor(Buffer.from("unused"));
  try {
    for (const version of ["../other","v1.2.3;id","v01.2.3","v1.2.3\ninvalid","v1.2.3-beta"]) {
      assert.notEqual((await runInstaller(fixture,{DOCKET_VERSION:version})).code,0);
    }
    assert.notEqual((await runInstaller(fixture,{DOCKET_INSTALL_DIR:"relative/path"})).code,0);
    assert.notEqual((await runInstaller(fixture,{DOCKET_INSTALL_DIR:"/tmp/invalid\npath"})).code,0);
    await mkdir(fixture.destination,{recursive:true});
    const original = join(fixture.directory,"original");
    await writeFile(original,"do not change");
    await symlink(original,join(fixture.destination,"docket"));
    assert.notEqual((await runInstaller(fixture)).code,0);
    assert.equal(await readFile(original,"utf8"),"do not change");
    await rm(join(fixture.destination,"docket"));
    for (const [operatingSystem,architecture] of [["Darwin","x86_64"],["Linux","aarch64"]]) {
      await writeFile(join(fixture.tools,"uname"),`#!/bin/sh\ncase "$1" in -s) printf '%s\\n' '${operatingSystem}';; -m) printf '%s\\n' '${architecture}';; *) exit 1;; esac\n`,{mode:0o755});
      assert.notEqual((await runInstaller(fixture)).code,0);
    }
    await rm(join(fixture.tools,"uname"));
    const oldCurl = await runInstaller(fixture,{TEST_CURL_VERSION:"8.3.0"});
    assert.notEqual(oldCurl.code,0);
    assert.match(oldCurl.stderr,/curl 8.4 or newer/);
    assert.equal(fixture.requests.length,0);
    assert.deepEqual(await readdir(fixture.temporary),[]);
    const help = await runInstaller(fixture,{},["--help"]);
    assert.equal(help.code,0,help.stderr);
    assert.match(help.stdout,/Linux x86-64/);
    assert.equal(fixture.requests.length,0);
  } finally { await fixture.close(); }
});

test("installer download size and redirect bounds reject bad responses and wrong-architecture assets safely", { timeout: 60000 }, async () => {
  const machineOffsetBytes = 18;
  const machineSizeBytes = 2;
  const machineArm64 = 183;
  const wrongArchitecture = Buffer.alloc(machineOffsetBytes+machineSizeBytes);
  Buffer.from("7f454c4602010100","hex").copy(wrongArchitecture);
  wrongArchitecture.writeUInt16LE(machineArm64,machineOffsetBytes);
  for (const mode of ["oversized","redirect","wrong-architecture"]) {
    const fixture = await fixtureFor(wrongArchitecture,undefined,mode);
    try {
      const result = await runInstaller(fixture);
      assert.notEqual(result.code,0,mode);
      await assert.rejects(stat(fixture.destination),{code:"ENOENT"});
      assert.deepEqual(await readdir(fixture.temporary),[]);
      if(mode==="redirect") assert.equal(fixture.requests.length,4,"Exactly the original request plus three allowed redirects");
      if(mode==="oversized") assert.equal(fixture.requests.length,1,"Oversized manifest must stop before binary download");
      if(mode==="wrong-architecture") assert.match(result.stderr,/not a Linux x86-64 ELF binary/);
    } finally { await fixture.close(); }
  }
});

test("installer upgrades a running disposable Docket executable without restarting its server", { timeout: 30000 }, async () => {
  const fixture = await fixtureFor(await readFile(binary));
  let child;
  try {
    await mkdir(fixture.destination,{recursive:true});
    const target = join(fixture.destination,"docket");
    await writeFile(target,await readFile(binary),{mode:0o755});
    child = spawn(target,["serve","--database",join(fixture.directory,"fixture.sqlite"),"--listen","127.0.0.1:0"],{stdio:["ignore","ignore","pipe"]});
    let diagnostics = "";
    let failure;
    child.on("error",error => { failure=error; });
    child.stderr.on("data",chunk => { diagnostics=(diagnostics+chunk.toString()).slice(-65536); });
    let origin;
    const readinessAttemptsMax = 100;
    const readinessDelayMs = 50;
    for(let i=0;i<readinessAttemptsMax;i++) {
      if(failure) throw failure;
      const match=diagnostics.match(/url=(http:\/\/127\.0\.0\.1:\d+)/);
      if(match) { origin=match[1]; break; }
      if(child.exitCode!==null) throw new Error("Disposable server failed to start");
      await delay(readinessDelayMs);
    }
    assert.ok(origin,"Disposable server readiness timed out");
    const executableBefore=await stat(target);
    const installed=await runInstaller(fixture);
    assert.equal(installed.code,0,installed.stderr);
    assert.equal((await stat(`/proc/${child.pid}/exe`)).ino,executableBefore.ino,"Running server must retain its original executable inode");
    assert.equal(child.exitCode,null,"Existing server must remain running");
    assert.equal(child.signalCode,null);
    const health=await fetch(origin+"/healthz",{signal:AbortSignal.timeout(commandTimeoutMs)});
    assert.equal(health.status,200);
    assert.deepEqual(await health.json(),{status:"ok"});
  } finally {
    try {
      if(child && child.exitCode===null && child.signalCode===null) {
        const exited=once(child,"exit",{signal:AbortSignal.timeout(commandTimeoutMs)});
        child.kill("SIGTERM");
        try { await exited; } catch(failure) { child.kill("SIGKILL"); throw failure; }
      }
    } finally { await fixture.close(); }
  }
});

test("truncated piped installers do not run a partial installation body", { timeout: 30000 }, async () => {
  const fixture = await fixtureFor(Buffer.from("unused"));
  try {
    const source = await readFile(installer,"utf8");
    const definitionsOnly = source.slice(0,source.lastIndexOf('\nmain "$@"'));
    const partialBody = source.slice(0,source.indexOf("actual=$(sha256sum"));
    assert.ok(definitionsOnly.length>0 && partialBody.length>0);
    assert.equal((await runInstaller(fixture,{},[],definitionsOnly)).code,0);
    assert.notEqual((await runInstaller(fixture,{},[],partialBody)).code,0);
    assert.equal(fixture.requests.length,0);
    assert.deepEqual(await readdir(fixture.temporary),[]);
    await assert.rejects(stat(fixture.destination),{code:"ENOENT"});
  } finally { await fixture.close(); }
});
