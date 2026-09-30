import test from "node:test";
import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import { mkdtemp, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ConcordClient } from "../src/client.js";
import { generateKeyPairSync } from "node:crypto";
import { digest } from "../src/ids.js";
import { signPayload, syncEnvelope, verificationLinkEnvelope } from "../src/crypto.js";

const wait = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const rid = (n) => `agt_${String(n).padStart(32,"0")}`;
const tid = "ten_00000000000000000000000000000001";

function assertion(n, value) {
  return {
    schema:"world-assertion/0.1",
    assertion_id:`ast_${String(n).padStart(32,"0")}`,
    subject:"Jonas-Lenovo",
    predicate:"reachability",
    value_or_ref:value,
    epistemic:"observed",
    currentness:"current",
    source_refs:[`probe:${n}`],
    evidence_refs:[`receipt:${n}`],
    observed_at:"2026-09-30T12:00:00Z",
    evidence_observed_at:"2026-09-30T12:00:00Z",
    asserted_at:"2026-09-30T12:00:01Z",
    valid_until:"2099-12-31T23:59:59Z",
    tenant_id:tid,
    domain:"infrastructure",
    classification:"internal",
    consent_record:"consent:test",
    consent_version:"1",
    purpose:"e2e",
    evidence_requirement:"current_observed"
  };
}

test("HTTP E2E: Friday -> Fo -> conflict HOLD -> explicit resolve", async (t) => {
  const dir = await mkdtemp(join(tmpdir(), "concord-e2e-"));
  const port = 18787 + Math.floor(Math.random() * 1000);
  const child = spawn(process.execPath, ["src/server.js"], {
    cwd: process.cwd(),
    env: { ...process.env, CONCORD_HOST:"127.0.0.1", CONCORD_PORT:String(port), CONCORD_DATA_PATH:join(dir,"state.json") },
    stdio:["ignore","pipe","pipe"]
  });
  t.after(async () => {
    child.kill("SIGTERM");
    await rm(dir, { recursive:true, force:true });
  });

  const client = new ConcordClient(`http://127.0.0.1:${port}`);
  let ready = false;
  for (let i=0; i<50; i++) {
    try { await client.health(); ready = true; break; } catch { await wait(50); }
  }
  assert.equal(ready, true, "server failed to become healthy");

  await client.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await client.registerReplica({ replica_id:rid(2), name:"Fo", kind:"agent" });

  const first = await client.push({ replica_id:rid(1), assertions:[assertion(1,"reachable")] });
  assert.equal(first.ok, true);

  const fo = await client.pull(rid(2), 0);
  assert.equal(fo.assertions.some((a) => a.value_or_ref === "reachable"), true);

  const divergent = await client.push({ replica_id:rid(2), base_revision:fo.revision, assertions:[assertion(2,"unreachable")] });
  assert.equal(divergent.conflicts.length, 1);
  assert.equal(divergent.conflicts[0].state, "HOLD");

  const conflict = divergent.conflicts[0];
  const resolved = await client.resolve(conflict.conflict_id, { resolution:"select", winner_assertion_id:assertion(2,"unreachable").assertion_id });
  assert.equal(resolved.ok, true);

  const state = await client.state();
  assert.equal(state.conflicts.find((c) => c.conflict_id === conflict.conflict_id).state, "RESOLVED");
  assert.equal(state.assertions.find((a) => a.assertion_id === assertion(2,"unreachable").assertion_id).currentness, "current");
});

test("HTTP strict mode: signed governance + independent verifier link", async (t) => {
  const dir = await mkdtemp(join(tmpdir(), "concord-e2e-strict-"));
  const port = 19887 + Math.floor(Math.random() * 1000);
  const child = spawn(process.execPath, ["src/server.js"], {
    cwd: process.cwd(),
    env: {
      ...process.env,
      CONCORD_HOST:"127.0.0.1",
      CONCORD_PORT:String(port),
      CONCORD_DATA_PATH:join(dir,"state.json"),
      CONCORD_GOVERNANCE_MODE:"required",
      CONCORD_VERIFICATION_MODE:"registered-signed"
    },
    stdio:["ignore","pipe","pipe"]
  });
  t.after(async () => {
    child.kill("SIGTERM");
    await rm(dir, { recursive:true, force:true });
  });

  const client = new ConcordClient(`http://127.0.0.1:${port}`);
  let ready = false;
  for (let i=0; i<50; i++) {
    try { await client.health(); ready = true; break; } catch { await wait(50); }
  }
  assert.equal(ready, true, "strict server failed to become healthy");

  const fridayKeys = generateKeyPairSync("ed25519");
  const sentinelKeys = generateKeyPairSync("ed25519");
  const fridayPublic = fridayKeys.publicKey.export({ type:"spki", format:"pem" });
  const fridayPrivate = fridayKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const sentinelPublic = sentinelKeys.publicKey.export({ type:"spki", format:"pem" });
  const sentinelPrivate = sentinelKeys.privateKey.export({ type:"pkcs8", format:"pem" });

  await client.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent", trust_mode:"signed", public_key_pem:fridayPublic });
  await client.registerReplica({ replica_id:rid(2), name:"Sentinel", kind:"agent", trust_mode:"signed", public_key_pem:sentinelPublic });

  const a = assertion(11,"verified-value");
  const governance = {
    schema:"concord-governance-binding/1",
    operation:"sync.push",
    correlation:{
      schema:"correlation/1.0",
      execution_context_id:"ctx_11111111111111111111111111111111",
      tenant_id:tid,
      principal_id:"prn_22222222222222222222222222222222",
      mission_id:"mission:strict-e2e",
      authority_lease_id:"auth_33333333333333333333333333333333",
      work_id:"wrk_44444444444444444444444444444444",
      admission_decision_id:"pdr_55555555555555555555555555555555",
      trace_id:"trc_66666666666666666666666666666666",
      action_id:"act_77777777777777777777777777777777"
    },
    authority_ref:"trust-gateway:authority:auth_33333333333333333333333333333333",
    capability_ref:"trust-gateway:capability:worker",
    decision_ref:"trust-gateway:decision:pdr_55555555555555555555555555555555",
    decision:"allow",
    observed_at:"2026-09-30T12:29:00Z",
    valid_until:"2099-12-31T23:59:59Z"
  };
  const signature = signPayload(syncEnvelope({
    replica_id:rid(1),
    base_revision:0,
    assertions:[a],
    governance
  }), fridayPrivate);

  const pushed = await client.push({
    replica_id:rid(1),
    base_revision:0,
    assertions:[a],
    governance,
    signature
  });
  assert.equal(pushed.ok, true);
  assert.equal(pushed.receipt.governance_binding_sha256, digest(governance));

  const link = {
    schema:"concord-verification-link/1",
    link_id:"vln_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
    subject_type:"assertion",
    subject_id:a.assertion_id,
    subject_sha256:digest(a),
    verifier_ref:`replica:${rid(2)}`,
    verdict:"pass",
    receipt_ref:"sentinel:receipt:e2e-001",
    verified_at:"2026-09-30T12:31:00Z",
    correlation:governance.correlation
  };
  const verifierSignature = signPayload(verificationLinkEnvelope(link), sentinelPrivate);
  const verified = await client.addVerificationLink(link, verifierSignature);
  assert.equal(verified.ok, true);

  const state = await client.state();
  assert.equal(state.verification_links.length, 1);
  assert.equal(state.verification_links[0].verdict, "pass");
});
