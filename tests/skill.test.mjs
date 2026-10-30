import test from "node:test";
import assert from "node:assert/strict";
import { readFile, stat, mkdtemp, copyFile, chmod, rm } from "node:fs/promises";
import { dirname, resolve, sep, join } from "node:path";
import { tmpdir } from "node:os";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execute = promisify(execFile);
const skillDirectory = resolve("skills/docket-cli");
const skillFiles = ["SKILL.md", "references/commands.md", "references/safety.md"];
const commandTimeoutMs = 5000;
const outputBytesMax = 1048576;

test("CLI skill has discoverable metadata and self-contained reference links", async () => {
  const skill = await readFile(resolve(skillDirectory, "SKILL.md"), "utf8");
  const metadata = skill.match(/^---\n([\s\S]*?)\n---\n/);
  assert.ok(metadata, "Skill must begin with frontmatter like the agent-browser skill");
  assert.match(metadata[1], /^name: docket-cli$/m);
  assert.match(metadata[1], /^description: >/m);
  assert.match(metadata[1], /^allowed-tools: Bash\(docket:\*\)$/m);
  for (const file of skillFiles) {
    const path = resolve(skillDirectory, file);
    const content = await readFile(path, "utf8");
    for (const link of content.matchAll(/\[[^\]]+\]\(([^)]+)\)/g)) {
      const target = resolve(dirname(path), link[1].split("#")[0]);
      assert.ok(target.startsWith(skillDirectory + sep), "Skill references must remain portable with the folder");
      assert.ok((await stat(target)).isFile(), `Broken reference in ${file}`);
    }
  }
});

test("CLI skill command families match the installed/built CLI help without server requests", { timeout: 30000 }, async () => {
  const binary = process.env.DOCKET_BINARY ? resolve(process.env.DOCKET_BINARY) : "docket";
  const commands = [
    ["doctor"],
    ...["list", "create", "get", "update", "archive", "restore"].map(action => ["project", action]),
    ...["list", "create", "get", "update", "archive", "restore", "history"].map(action => ["ticket", action]),
    ["comment", "list"], ["comment", "add"], ["events", "list"],
  ];
  for (const command of commands) {
    const result = await execute(binary, [...command, "--help"], { timeout: commandTimeoutMs, maxBuffer: outputBytesMax, env: { ...process.env, DOCKET_URL: "http://127.0.0.1:0", DOCKET_ACTOR: "" } });
    assert.match(result.stdout, /Usage: docket/);
    assert.equal(result.stderr, "", "Discovery must not emit failures or start a server");
    if (["ticket", "comment", "events"].includes(command[0])) assert.match(result.stdout, /-project/);
    if (["update", "archive", "restore", "add"].includes(command[1])) assert.match(result.stdout, /-version/);
  }
});

test("Bundled skill and named references work from a copied binary outside the repo without a server", { timeout: 30000 }, async () => {
  const directory = await mkdtemp(join(tmpdir(), "docket-bundled-skill-"));
  try {
    const source = process.env.DOCKET_BINARY ? resolve(process.env.DOCKET_BINARY) : (await execute("which", ["docket"], { timeout: commandTimeoutMs })).stdout.trim();
    const binary = join(directory, "docket");
    await copyFile(source, binary);
    await chmod(binary, 0o755);
    const options = { cwd: directory, timeout: commandTimeoutMs, maxBuffer: outputBytesMax, env: { ...process.env, DOCKET_URL: "http://user:private-password@127.0.0.1:0", DOCKET_ACTOR: "private-label" } };
    const skill = await execute(binary, ["--skill"], options);
    assert.equal(skill.stdout, await readFile(resolve(skillDirectory, "SKILL.md"), "utf8"));
    assert.equal(skill.stderr, "");
    const references = await execute(binary, ["--skill", "--references"], options);
    assert.match(references.stdout, /commands\.md/);
    assert.match(references.stdout, /safety\.md/);
    assert.equal(references.stderr, "");
    for (const name of ["commands.md", "safety.md"]) {
      const reference = await execute(binary, ["--skill", "--references", name], options);
      assert.equal(reference.stdout, await readFile(resolve(skillDirectory, "references", name), "utf8"));
      assert.equal(reference.stderr, "");
    }
    for (const name of ["../SKILL.md", "/etc/passwd", "references/commands.md", "missing.md"]) {
      await assert.rejects(execute(binary, ["--skill", "--references", name], options), failure => {
        assert.equal(failure.code, 2);
        assert.equal(failure.stdout, "");
        assert.ok(JSON.parse(failure.stderr).error.code);
        assert.doesNotMatch(failure.stderr, /private-password|private-label/);
        return true;
      });
    }
    await assert.rejects(stat(join(directory, "data")), { code: "ENOENT" });
  } finally { await rm(directory, { recursive: true, force: true }); }
});
