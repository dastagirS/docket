import { execFile } from "node:child_process";
import { writeSync } from "node:fs";
import { promisify } from "node:util";
import { access, realpath } from "node:fs/promises";
import { dirname, resolve } from "node:path";
import { fileURLToPath } from "node:url";

const execute = promisify(execFile);
const repositoryDirectory = resolve(dirname(fileURLToPath(import.meta.url)), "..");
const localTimeoutMs = 15000;
const remoteTimeoutMs = 60000;
const confirmationTimeoutMs = 120000;
const releaseTimeoutMs = 600000;
const outputBytesMax = 1048576;
const confirmationBytesMax = 1024;
const referencesCountMax = 10000;
const versionBytesMax = 64;
const stableVersionPattern = /^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$/;
const objectPattern = /^(?:[0-9a-f]{40}|[0-9a-f]{64})$/;
const cancellation = new AbortController();
const signal = AbortSignal.any([cancellation.signal, AbortSignal.timeout(releaseTimeoutMs)]);
let createdTag = "";
let pushAttempted = false;

// Synchronous writes surface broken output before release work can silently continue.
function writeOutput(message) {
  const line = message + "\n";
  if (writeSync(process.stdout.fd, line) !== Buffer.byteLength(line)) throw new Error("Release output was incomplete.");
}

async function git(arguments_, remote = false) {
  try {
    const result = await execute("git", arguments_, {
      cwd: repositoryDirectory,
      env: { ...process.env, GIT_TERMINAL_PROMPT: "0", GIT_NO_REPLACE_OBJECTS: "1" },
      timeout: remote ? remoteTimeoutMs : localTimeoutMs,
      maxBuffer: outputBytesMax,
      signal,
    });
    return result.stdout.trimEnd();
  } catch (error) {
    // Git diagnostics can contain credential-bearing URLs; never echo them.
    const failure = new Error(`Git ${arguments_[0]} failed, timed out, or was interrupted.`);
    failure.exitCode = error.code;
    throw failure;
  }
}

function lines(value) {
  const result = value === "" ? [] : value.split("\n");
  if (result.length > referencesCountMax) throw new Error("Git reference count exceeds the release limit.");
  return result;
}

function highestVersion(references) {
  let highest = [0n, 0n, 0n];
  for (const reference of references) {
    const match = stableVersionPattern.exec(reference.replace(/^refs\/tags\//, ""));
    if (!match) continue;
    if (match[0].length > versionBytesMax) throw new Error("A stable tag exceeds the release version limit.");
    const version = match.slice(1).map(value => BigInt(value));
    for (let i = 0; i < version.length; i++) {
      if (version[i] === highest[i]) continue;
      if (version[i] > highest[i]) highest = version;
      break;
    }
  }
  return highest;
}

async function remoteReferences() {
  const records = lines(await git(["ls-remote", "--refs", "origin", "refs/heads/master", "refs/tags/*"], true));
  const references = new Map();
  for (const record of records) {
    const fields = record.split("\t");
    if (fields.length !== 2 || !objectPattern.test(fields[0]) || references.has(fields[1]) ||
        (fields[1] !== "refs/heads/master" && !fields[1].startsWith("refs/tags/"))) {
      throw new Error("Origin returned invalid or duplicate references.");
    }
    references.set(fields[1], fields[0]);
  }
  return references;
}

async function cleanMaster() {
  if (await git(["symbolic-ref", "--quiet", "--short", "HEAD"]) !== "master") {
    throw new Error("Release from master, not a feature branch or detached HEAD.");
  }
  if (await git(["status", "--porcelain=v1", "--untracked-files=all"]) !== "") {
    throw new Error("Commit or stash working-tree changes before releasing. Nothing is auto-committed.");
  }
  for (const operation of ["MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply", "BISECT_LOG"]) {
    const path = await git(["rev-parse", "--git-path", operation]);
    try {
      await access(resolve(repositoryDirectory, path));
    } catch (error) {
      if (error.code === "ENOENT") continue;
      throw new Error("Cannot inspect the repository operation state.");
    }
    throw new Error("Finish the active merge, rebase, cherry-pick, revert, or bisect before releasing.");
  }
  const commit = await git(["rev-parse", "HEAD"]);
  if (!objectPattern.test(commit)) throw new Error("Cannot identify the release commit.");
  return commit;
}

async function validateOrigin() {
  const origin = await git(["config", "--get-all", "remote.origin.url"]);
  const allowed = /^(?:git@github\.com:|https:\/\/github\.com\/|ssh:\/\/git@github\.com\/)dastagirs\/docket(?:\.git)?$/i;
  if (!allowed.test(origin)) throw new Error("Origin must be the Docket GitHub repository with no embedded credentials.");
  let pushURL = "";
  try {
    pushURL = await git(["config", "--get-all", "remote.origin.pushurl"]);
  } catch (error) {
    if (error.exitCode !== 1) throw error;
  }
  if (pushURL !== "" && pushURL !== origin) {
    throw new Error("Origin fetch and push URLs must match so version discovery and publication use the same repository.");
  }
  const fetchDestination = await git(["remote", "get-url", "--all", "origin"]);
  const pushDestination = await git(["remote", "get-url", "--push", "--all", "origin"]);
  if (lines(fetchDestination).length !== 1 || fetchDestination !== pushDestination) {
    throw new Error("Resolved fetch and push destinations must match.");
  }
}

async function confirm() {
  const question = "Push this commit and tag to origin, triggering release publication? [y/N] ";
  if (writeSync(process.stdout.fd, question) !== Buffer.byteLength(question)) throw new Error("Cannot write the complete release confirmation.");
  return new Promise((resolveConfirmation, reject) => {
    let input = "";
    const timer = setTimeout(() => finish(new Error("Release confirmation timed out.")), confirmationTimeoutMs);
    const finish = (error, answer = false) => {
      clearTimeout(timer);
      process.stdin.off("data", receive);
      process.stdin.off("end", ended);
      process.stdin.off("error", failed);
      signal.removeEventListener("abort", aborted);
      process.stdin.pause();
      if (error) reject(error);
      else resolveConfirmation(answer);
    };
    const receive = chunk => {
      if (Buffer.byteLength(input) + chunk.length > confirmationBytesMax) {
        finish(new Error("Release confirmation exceeded its input limit."));
        return;
      }
      input += chunk.toString("utf8");
      if (input.includes("\n")) finish(null, /^(?:y|yes)$/i.test(input.split("\n", 1)[0].trim()));
    };
    const ended = () => finish(null, /^(?:y|yes)$/i.test(input.trim()));
    const failed = () => finish(new Error("Cannot read release confirmation."));
    const aborted = () => finish(new Error("Release interrupted."));
    process.stdin.on("data", receive);
    process.stdin.once("end", ended);
    process.stdin.once("error", failed);
    signal.addEventListener("abort", aborted, { once: true });
    if (signal.aborted) aborted();
    else process.stdin.resume();
  });
}

async function main() {
  const arguments_ = process.argv.slice(2);
  if (arguments_.length === 1 && ["--help", "-h"].includes(arguments_[0])) {
    writeOutput("Usage: ./scripts/release.sh [patch|minor|major]\n\nDefault: patch. Preview and confirm an atomic master+tag push to origin.\nRequires Node 24+ and Git. No auto-commit, force-push, tag reuse, or retry.\nA pushed tag triggers CI; publication still requires both platform jobs to pass.");
    return;
  }
  const bump = arguments_[0] ?? "patch";
  if (arguments_.length > 1 || !["patch", "minor", "major"].includes(bump)) {
    throw new Error("Use ./scripts/release.sh [patch|minor|major].");
  }
  if (Number(process.versions.node.split(".")[0]) < 24) throw new Error("Release requires Node 24+.");
  if (await realpath(await git(["rev-parse", "--show-toplevel"])) !== await realpath(repositoryDirectory)) {
    throw new Error("Run the release script from its own Git checkout.");
  }
  await validateOrigin();
  const commit = await cleanMaster();
  const localTags = lines(await git(["for-each-ref", "--format=%(refname)", "refs/tags"]));
  const remote = await remoteReferences();
  const remoteHead = remote.get("refs/heads/master");
  if (remoteHead) {
    try {
      await git(["merge-base", "--is-ancestor", remoteHead, commit]);
    } catch {
      throw new Error("Local master does not contain origin/master. Fetch and reconcile it before releasing; no force-push is used.");
    }
  }
  const highest = highestVersion([...localTags, ...remote.keys()]);
  const version = [...highest];
  const index = { major: 0, minor: 1, patch: 2 }[bump];
  version[index] += 1n;
  for (let i = index + 1; i < version.length; i++) version[i] = 0n;
  const tag = "v" + version.join(".");
  if (tag.length > versionBytesMax) throw new Error("The next version exceeds the release version limit.");
  writeOutput(`Release: ${tag}\nCommit:  ${commit}\nBranch:  master\nRemote:  origin (dastagirS/docket)\nLocal and remote stable tags, including failed releases, reserve their versions.`);
  if (!await confirm()) {
    writeOutput("Cancelled. No tag created or push attempted.");
    return;
  }
  await validateOrigin();
  if (await cleanMaster() !== commit || (await git(["for-each-ref", "--format=%(refname)", "refs/tags"])) !== localTags.join("\n")) {
    throw new Error("The checkout or local tags changed during confirmation. Nothing was pushed; rerun to review a fresh preview.");
  }
  const refreshed = await remoteReferences();
  if (refreshed.get("refs/heads/master") !== remoteHead ||
      highestVersion([...refreshed.keys()]).join(".") !== highestVersion([...remote.keys()]).join(".")) {
    throw new Error("Origin changed during confirmation. Nothing was pushed; rerun to review a fresh preview.");
  }
  await git(["tag", "-a", tag, "-m", `Docket ${tag}`, commit]);
  createdTag = tag;
  const tagObject = await git(["rev-parse", `refs/tags/${tag}`]);
  if (!objectPattern.test(tagObject) || await git(["rev-parse", `refs/tags/${tag}^{commit}`]) !== commit) {
    throw new Error("The new local tag changed unexpectedly. No push attempted; inspect it before retrying.");
  }
  pushAttempted = true;
  let pushFailed = false;
  try {
    // User-configured push.followTags must not publish unrelated release tags.
    await git(["push", "--atomic", "--no-follow-tags", "--porcelain", "origin", `${commit}:refs/heads/master`, `${tagObject}:refs/tags/${tag}`], true);
  } catch {
    pushFailed = true;
  }
  const published = await remoteReferences();
  if (published.get(`refs/tags/${tag}`) !== tagObject || published.get("refs/heads/master") !== commit) {
    throw new Error("Atomic push failed or could not be confirmed. Check origin permissions, branch protection, and Actions before retrying. No non-atomic fallback was attempted.");
  }
  if (pushFailed) writeOutput("Git reported a push error, but origin confirms the exact release tag. No push was retried.");
  writeOutput(`Pushed ${tag} at ${commit}.\nGitHub Actions will build and publish only after all release gates pass.\nFollow: https://github.com/dastagirS/docket/actions`);
}

const interrupt = () => cancellation.abort();
process.once("SIGINT", interrupt);
process.once("SIGTERM", interrupt);
try {
  await main();
} catch (error) {
  try {
    writeSync(process.stderr.fd, `Release stopped: ${error.message}\n`);
    if (createdTag) writeSync(process.stderr.fd, `Local tag ${createdTag} was retained; ${pushAttempted ? "push outcome may be uncertain" : "no push was attempted"}. Inspect origin and Actions before rerunning. No automatic retry or tag deletion occurs.\n`);
    else writeSync(process.stderr.fd, "No push was attempted. If tag creation was interrupted, inspect local tags before rerunning.\n");
  } catch {
    // A closed diagnostics pipe must not turn an uncertain publication into success.
  }
  process.exitCode = 1;
} finally {
  process.off("SIGINT", interrupt);
  process.off("SIGTERM", interrupt);
}
