import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

test("token protects state export and metrics while health stays public", async (t) => {
  const dir = await mkdtemp(join(tmpdir(), "concord-security-"));
  const port = 21000 + Math.floor(Math.random() * 1000);
  const token = "test-concord-token";
  const child = spawn(process.execPath, ["src/server.js"], {
    cwd:process.cwd(),
    env:{
      ...process.env,
      CONCORD_HOST:"127.0.0.1",
      CONCORD_PORT:String(port),
      CONCORD_DATA_PATH:join(dir,"state.json"),
      CONCORD_TOKEN:token
    },
    stdio:["ignore","pipe","pipe"]
  });
  t.after(async () => {
    child.kill("SIGTERM");
    await rm(dir,{recursive:true,force:true});
  });

  const base = `http://127.0.0.1:${port}`;
  let ready = false;
  for (let i=0;i<50;i++) {
    try {
      const r = await fetch(base + "/health");
      if (r.ok) { ready = true; break; }
    } catch {}
    await wait(50);
  }
  assert.equal(ready,true);

  const health = await fetch(base + "/health");
  assert.equal(health.status,200);
  assert.equal(health.headers.get("x-frame-options"),"DENY");

  for (const path of ["/v1/state","/v1/export","/metrics"]) {
    const denied = await fetch(base + path);
    assert.equal(denied.status,401,path);
    const allowed = await fetch(base + path,{headers:{authorization:`Bearer ${token}`}});
    assert.equal(allowed.status,200,path);
  }
});

test("non-loopback binding without token fails before listening", async () => {
  const child = spawn(process.execPath, ["src/server.js"], {
    cwd:process.cwd(),
    env:{
      ...process.env,
      CONCORD_HOST:"0.0.0.0",
      CONCORD_PORT:"0",
      CONCORD_TOKEN:"",
      CONCORD_ALLOW_UNAUTHENTICATED_REMOTE:"0"
    },
    stdio:["ignore","pipe","pipe"]
  });
  const code = await new Promise((resolve) => child.once("close",resolve));
  assert.notEqual(code,0);
});
