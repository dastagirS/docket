import test from "node:test";
import assert from "node:assert/strict";
import { execFile } from "node:child_process";
import { promisify } from "node:util";
import { mkdtemp, readFile, rm, stat, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

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
  assert.match(workflow,/--verify-tag --draft/);
});
