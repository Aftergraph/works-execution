import test from "node:test";
import assert from "node:assert/strict";
import { ConcordEngine } from "../src/engine.js";
import { generateKeyPairSync } from "node:crypto";
import { checkpointAckEnvelope, keyRotationEnvelope, publicKeyFingerprint, signPayload, syncEnvelope, verificationLinkEnvelope, verifyPayload } from "../src/crypto.js";
import { digest } from "../src/ids.js";

class MemoryStore {
  constructor() {
    this.state = { schema:"concord-state/1", revision:0, replicas:{}, assertions:{}, conflicts:{}, receipts:{}, verification_links:{}, events:[] };
  }
  async read() { return structuredClone(this.state); }
  async transact(fn) {
    const result = await fn(this.state);
    return structuredClone(result);
  }
}

const rid = (n) => `agt_${String(n).padStart(32,"0")}`;
const tid = "ten_00000000000000000000000000000001";
const at = (n, value, extra={}) => ({
  schema:"world-assertion/0.1",
  assertion_id:`ast_${String(n).padStart(32,"0")}`,
  subject:"Aftergraph/fihim-vnext",
  predicate:"latest_verified_release",
  value_or_ref:value,
  epistemic:"observed",
  currentness:"current",
  source_refs:[`github:commit:${n}`],
  evidence_refs:[`ci:run:${n}`],
  observed_at:"2026-09-30T12:00:00Z",
  evidence_observed_at:"2026-09-30T12:00:00Z",
  asserted_at:"2026-09-30T12:00:01Z",
  valid_until:"2026-10-01T12:00:00Z",
  tenant_id:tid,
  domain:"engineering",
  classification:"internal",
  consent_record:"consent:jonas",
  consent_version:"1",
  purpose:"agent-continuity",
  evidence_requirement:"current_observed",
  ...extra
});

function engine(options = {}) {
  return new ConcordEngine(new MemoryStore(), () => new Date("2026-09-30T12:30:00Z"), options);
}

const correlation = (tenant_id = tid) => ({
  schema:"correlation/1.0",
  execution_context_id:"ctx_11111111111111111111111111111111",
  tenant_id,
  principal_id:"prn_22222222222222222222222222222222",
  mission_id:"mission:concord-test",
  authority_lease_id:"auth_33333333333333333333333333333333",
  work_id:"wrk_44444444444444444444444444444444",
  admission_decision_id:"pdr_55555555555555555555555555555555",
  trace_id:"trc_66666666666666666666666666666666",
  action_id:"act_77777777777777777777777777777777"
});

const governance = (operation = "sync.push", tenant_id = tid, extra = {}) => ({
  schema:"concord-governance-binding/1",
  operation,
  correlation:correlation(tenant_id),
  authority_ref:"trust-gateway:authority:auth_33333333333333333333333333333333",
  capability_ref:"trust-gateway:capability:worker",
  decision_ref:"trust-gateway:decision:pdr_55555555555555555555555555555555",
  decision:"allow",
  observed_at:"2026-09-30T12:29:00Z",
  valid_until:"2026-09-30T13:30:00Z",
  ...extra
});

test("registers independent replicas and syncs an assertion", async () => {
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.registerReplica({ replica_id:rid(2), name:"Fo", kind:"agent" });
  const pushed = await e.push({ replica_id:rid(1), assertions:[at(1,"v0.25.0")] });
  assert.equal(pushed.ok, true);
  const pulled = await e.pull(rid(2), 0);
  assert.equal(pulled.assertions[0].value_or_ref, "v0.25.0");
  assert.equal(pulled.conflicts.length, 0);
});

test("replay is idempotent", async () => {
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  const a = at(1,"v0.25.0");
  await e.push({ replica_id:rid(1), assertions:[a] });
  const second = await e.push({ replica_id:rid(1), assertions:[a] });
  assert.equal(second.receipt.accepted[0].disposition, "idempotent");
  const snap = await e.snapshot();
  assert.equal(snap.assertions.length, 1);
});

test("semantic divergence fails closed into HOLD", async () => {
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.registerReplica({ replica_id:rid(2), name:"Fo", kind:"agent" });
  await e.push({ replica_id:rid(1), assertions:[at(1,"reachable")] });
  const pushed = await e.push({ replica_id:rid(2), assertions:[at(2,"unreachable")] });
  assert.equal(pushed.conflicts.length, 1);
  assert.equal(pushed.conflicts[0].state, "HOLD");
  const snap = await e.snapshot();
  assert.equal(snap.assertions.every((a) => a.currentness === "disputed"), true);
});

test("expired current assertions are rejected", async () => {
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  const result = await e.push({
    replica_id:rid(1),
    assertions:[at(1,"green",{valid_until:"2026-09-29T12:00:00Z"})]
  });
  assert.equal(result.ok, false);
  assert.match(result.receipt.rejected[0].errors.join(" "), /past valid_until/);
});

test("conflict can only be resolved to a participating assertion", async () => {
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.registerReplica({ replica_id:rid(2), name:"Fo", kind:"agent" });
  await e.push({ replica_id:rid(1), assertions:[at(1,"A")] });
  const pushed = await e.push({ replica_id:rid(2), assertions:[at(2,"B")] });
  const conflict_id = pushed.conflicts[0].conflict_id;
  const bad = await e.resolveConflict({ conflict_id, resolution:"select", winner_assertion_id:at(9,"X").assertion_id });
  assert.equal(bad.ok, false);
  const good = await e.resolveConflict({ conflict_id, resolution:"select", winner_assertion_id:at(2,"B").assertion_id });
  assert.equal(good.ok, true);
  const snap = await e.snapshot();
  assert.equal(snap.assertions.find((a) => a.assertion_id === at(2,"B").assertion_id).currentness, "current");
});

test("receipt resulting_revision equals persisted journal revision", async () => {
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  const pushed = await e.push({ replica_id:rid(1), assertions:[at(1,"v0.25.0")] });
  const snap = await e.snapshot();
  assert.equal(pushed.receipt.resulting_revision, snap.revision);
});

test("base revision drift is evaluated against the pre-push revision", async () => {
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  const before = (await e.snapshot()).revision;
  const pushed = await e.push({ replica_id:rid(1), base_revision:before, assertions:[at(1,"v0.25.0")] });
  assert.equal(pushed.drifted, false);
});

test("signed replica rejects missing and tampered signatures", async () => {
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const public_key_pem = publicKey.export({ type:"spki", format:"pem" });
  const private_key_pem = privateKey.export({ type:"pkcs8", format:"pem" });
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent", trust_mode:"signed", public_key_pem });
  const assertions = [at(1,"trusted")];
  const envelope = syncEnvelope({ replica_id:rid(1), base_revision:0, assertions });

  const missing = await e.push({ replica_id:rid(1), assertions });
  assert.equal(missing.status, 401);

  const signature = signPayload(envelope, private_key_pem);
  const tampered = await e.push({ replica_id:rid(1), assertions:[at(1,"tampered")], signature });
  assert.equal(tampered.status, 401);

  const accepted = await e.push({ replica_id:rid(1), assertions, signature });
  assert.equal(accepted.ok, true);
});

test("Concord signs receipts when a node signing key is configured", async () => {
  const replicaKeys = generateKeyPairSync("ed25519");
  const nodeKeys = generateKeyPairSync("ed25519");
  const public_key_pem = replicaKeys.publicKey.export({ type:"spki", format:"pem" });
  const private_key_pem = replicaKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const node_private_key_pem = nodeKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const node_public_key_pem = nodeKeys.publicKey.export({ type:"spki", format:"pem" });
  const e = new ConcordEngine(new MemoryStore(), () => new Date("2026-09-30T12:30:00Z"), {
    receiptPrivateKeyPem:node_private_key_pem,
    receiptKeyId:"node:test"
  });
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent", trust_mode:"signed", public_key_pem });
  const assertions=[at(1,"signed")];
  const signature=signPayload(syncEnvelope({replica_id:rid(1),base_revision:0,assertions}), private_key_pem);
  const pushed=await e.push({replica_id:rid(1),base_revision:0,assertions,signature});
  assert.equal(pushed.receipt.signature_alg, "Ed25519");
  assert.equal(pushed.receipt.signer_key_id, "node:test");
  const { signature:receiptSignature, signature_alg, signer_key_id, ...bodyWithHash } = pushed.receipt;
  const { sha256, ...receiptBody } = bodyWithHash;
  assert.equal(verifyPayload(receiptBody, receiptSignature, node_public_key_pem), true);
});

test("checkpoint acknowledgments are integrity checked and monotonic", async () => {
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.registerReplica({ replica_id:rid(2), name:"Fo", kind:"agent" });
  await e.push({ replica_id:rid(1), assertions:[at(1,"A")] });
  const pulled = await e.pull(rid(2), 0);

  const bad = await e.acknowledgeCheckpoint({ ...pulled.checkpoint, sha256:"0".repeat(64) });
  assert.equal(bad.ok, false);

  const good = await e.acknowledgeCheckpoint(pulled.checkpoint);
  assert.equal(good.ok, true);
  assert.equal(good.acknowledged_revision, pulled.checkpoint.revision);

  const snap = await e.snapshot();
  const fo = snap.replicas.find((r) => r.replica_id === rid(2));
  assert.equal(fo.last_ack_revision, pulled.checkpoint.revision);
});

test("signed replica identity cannot be silently downgraded or key-rotated", async () => {
  const first = generateKeyPairSync("ed25519");
  const second = generateKeyPairSync("ed25519");
  const e = engine();
  const public_key_pem = first.publicKey.export({ type:"spki", format:"pem" });
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent", trust_mode:"signed", public_key_pem });

  const downgrade = await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent", trust_mode:"local" });
  assert.equal(downgrade.status, 409);

  const rotate = await e.registerReplica({
    replica_id:rid(1),
    name:"Friday",
    kind:"agent",
    trust_mode:"signed",
    public_key_pem:second.publicKey.export({ type:"spki", format:"pem" })
  });
  assert.equal(rotate.status, 409);
});

test("signed replica checkpoint ACK requires a valid signature", async () => {
  const keys = generateKeyPairSync("ed25519");
  const public_key_pem = keys.publicKey.export({ type:"spki", format:"pem" });
  const private_key_pem = keys.privateKey.export({ type:"pkcs8", format:"pem" });
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.registerReplica({ replica_id:rid(2), name:"Fo", kind:"agent", trust_mode:"signed", public_key_pem });
  await e.push({ replica_id:rid(1), assertions:[at(1,"A")] });

  const pulled = await e.pull(rid(2), 0);
  const missing = await e.acknowledgeCheckpoint(pulled.checkpoint);
  assert.equal(missing.status, 401);

  const signature = signPayload(checkpointAckEnvelope(pulled.checkpoint), private_key_pem);
  const good = await e.acknowledgeCheckpoint({ ...pulled.checkpoint, signature });
  assert.equal(good.ok, true);
  assert.equal(good.acknowledged_revision, pulled.checkpoint.revision);
});

test("required governance fails closed when binding is absent", async () => {
  const e = engine({ governanceMode:"required" });
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  const result = await e.push({ replica_id:rid(1), assertions:[at(1,"A")] });
  assert.equal(result.status, 403);
  assert.match(result.errors.join(" "), /governance binding is required/);
});

test("governance binding enforces freshness and tenant correlation", async () => {
  const e = engine({ governanceMode:"required" });
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });

  const expired = await e.push({
    replica_id:rid(1),
    assertions:[at(1,"A")],
    governance:governance("sync.push", tid, { valid_until:"2026-09-30T12:00:00Z" })
  });
  assert.equal(expired.status, 403);
  assert.match(expired.errors.join(" "), /expired/);

  const otherTenant = "ten_ffffffffffffffffffffffffffffffff";
  const mismatch = await e.push({
    replica_id:rid(1),
    assertions:[at(1,"A")],
    governance:governance("sync.push", otherTenant)
  });
  assert.equal(mismatch.status, 403);
  assert.match(mismatch.errors.join(" "), /tenant_id must match/);
});

test("signed governed push cryptographically binds governance payload", async () => {
  const keys = generateKeyPairSync("ed25519");
  const public_key_pem = keys.publicKey.export({ type:"spki", format:"pem" });
  const private_key_pem = keys.privateKey.export({ type:"pkcs8", format:"pem" });
  const e = engine({ governanceMode:"required" });
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent", trust_mode:"signed", public_key_pem });
  const assertions = [at(1,"A")];
  const goodGovernance = governance();
  const signature = signPayload(syncEnvelope({
    replica_id:rid(1),
    base_revision:0,
    assertions,
    governance:goodGovernance
  }), private_key_pem);

  const tampered = await e.push({
    replica_id:rid(1),
    assertions,
    governance:{ ...goodGovernance, capability_ref:"trust-gateway:capability:operator" },
    signature
  });
  assert.equal(tampered.status, 401);

  const accepted = await e.push({
    replica_id:rid(1),
    assertions,
    governance:goodGovernance,
    signature
  });
  assert.equal(accepted.ok, true);
  assert.equal(accepted.receipt.correlation.tenant_id, tid);
  assert.equal(accepted.receipt.governance_binding_sha256, digest(goodGovernance));
});

test("registered-signed verification accepts exact-subject independently signed links", async () => {
  const verifierKeys = generateKeyPairSync("ed25519");
  const verifierPublic = verifierKeys.publicKey.export({ type:"spki", format:"pem" });
  const verifierPrivate = verifierKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const e = engine({ verificationMode:"registered-signed" });

  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.registerReplica({ replica_id:rid(2), name:"Sentinel", kind:"agent", trust_mode:"signed", public_key_pem:verifierPublic });
  const assertion = at(1,"A");
  await e.push({ replica_id:rid(1), assertions:[assertion], governance:governance() });

  const link = {
    schema:"concord-verification-link/1",
    link_id:"vln_88888888888888888888888888888888",
    subject_type:"assertion",
    subject_id:assertion.assertion_id,
    subject_sha256:digest(assertion),
    verifier_ref:`replica:${rid(2)}`,
    verdict:"pass",
    receipt_ref:"sentinel:receipt:001",
    verified_at:"2026-09-30T12:31:00Z",
    correlation:correlation()
  };
  const signature = signPayload(verificationLinkEnvelope(link), verifierPrivate);
  const result = await e.addVerificationLink(link, signature);
  assert.equal(result.ok, true);

  const snap = await e.snapshot();
  assert.equal(snap.verification_links.length, 1);
  assert.equal(snap.verification_links[0].verdict, "pass");
});

test("verification rejects fake digest, unsigned verifier, and self-verification", async () => {
  const producerKeys = generateKeyPairSync("ed25519");
  const producerPublic = producerKeys.publicKey.export({ type:"spki", format:"pem" });
  const producerPrivate = producerKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const verifierKeys = generateKeyPairSync("ed25519");
  const verifierPublic = verifierKeys.publicKey.export({ type:"spki", format:"pem" });
  const verifierPrivate = verifierKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const e = engine({ verificationMode:"registered-signed" });

  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent", trust_mode:"signed", public_key_pem:producerPublic });
  await e.registerReplica({ replica_id:rid(2), name:"Sentinel", kind:"agent", trust_mode:"signed", public_key_pem:verifierPublic });

  const assertion = at(1,"A");
  const subjectGovernance = governance();
  const pushSignature = signPayload(syncEnvelope({
    replica_id:rid(1),
    base_revision:0,
    assertions:[assertion],
    governance:subjectGovernance
  }), producerPrivate);
  await e.push({ replica_id:rid(1), assertions:[assertion], governance:subjectGovernance, signature:pushSignature });

  const baseLink = {
    schema:"concord-verification-link/1",
    link_id:"vln_99999999999999999999999999999999",
    subject_type:"assertion",
    subject_id:assertion.assertion_id,
    subject_sha256:digest(assertion),
    verifier_ref:`replica:${rid(2)}`,
    verdict:"pass",
    receipt_ref:"sentinel:receipt:002",
    verified_at:"2026-09-30T12:31:00Z",
    correlation:correlation()
  };

  const badDigest = { ...baseLink, subject_sha256:"0".repeat(64) };
  const badDigestSig = signPayload(verificationLinkEnvelope(badDigest), verifierPrivate);
  const mismatch = await e.addVerificationLink(badDigest, badDigestSig);
  assert.equal(mismatch.ok, false);
  assert.match(mismatch.errors.join(" "), /digest mismatch/);

  const unsigned = await e.addVerificationLink(baseLink);
  assert.equal(unsigned.status, 401);

  const selfLink = {
    ...baseLink,
    link_id:"vln_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
    verifier_ref:`replica:${rid(1)}`
  };
  const selfSig = signPayload(verificationLinkEnvelope(selfLink), producerPrivate);
  const self = await e.addVerificationLink(selfLink, selfSig);
  assert.equal(self.ok, false);
  assert.match(self.errors.join(" "), /cannot independently verify/);
});

test("verification rejects correlation from another governed action", async () => {
  const verifierKeys = generateKeyPairSync("ed25519");
  const verifierPublic = verifierKeys.publicKey.export({ type:"spki", format:"pem" });
  const verifierPrivate = verifierKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const e = engine({ governanceMode:"required", verificationMode:"registered-signed" });

  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.registerReplica({ replica_id:rid(2), name:"Sentinel", kind:"agent", trust_mode:"signed", public_key_pem:verifierPublic });

  const assertion = at(1,"A");
  const g = governance();
  await e.push({ replica_id:rid(1), assertions:[assertion], governance:g });

  const wrongCorrelation = { ...correlation(), action_id:"act_ffffffffffffffffffffffffffffffff" };
  const link = {
    schema:"concord-verification-link/1",
    link_id:"vln_cccccccccccccccccccccccccccccccc",
    subject_type:"assertion",
    subject_id:assertion.assertion_id,
    subject_sha256:digest(assertion),
    verifier_ref:`replica:${rid(2)}`,
    verdict:"pass",
    receipt_ref:"sentinel:receipt:other-action",
    verified_at:"2026-09-30T12:31:00Z",
    correlation:wrongCorrelation
  };
  const signature = signPayload(verificationLinkEnvelope(link), verifierPrivate);
  const result = await e.addVerificationLink(link, signature);
  assert.equal(result.ok, false);
  assert.match(result.errors.join(" "), /correlation does not match/);
});

test("governance refs must bind the exact authority lease and admission decision", async () => {
  const e = engine({ governanceMode:"required" });
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });

  const wrongAuthority = governance("sync.push", tid, {
    authority_ref:"trust-gateway:authority:auth_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  });
  const authorityResult = await e.push({
    replica_id:rid(1),
    assertions:[at(21,"authority-mismatch")],
    governance:wrongAuthority
  });
  assert.equal(authorityResult.status, 403);
  assert.match(authorityResult.errors.join(" "), /authority_ref must bind/);

  const wrongDecision = governance("sync.push", tid, {
    decision_ref:"trust-gateway:decision:pdr_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  });
  const decisionResult = await e.push({
    replica_id:rid(1),
    assertions:[at(22,"decision-mismatch")],
    governance:wrongDecision
  });
  assert.equal(decisionResult.status, 403);
  assert.match(decisionResult.errors.join(" "), /decision_ref must bind/);
});

test("assertion idempotency rejects same id with different payload", async () => {
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.push({ replica_id:rid(1), assertions:[at(31,"A")] });
  const collision = await e.push({ replica_id:rid(1), assertions:[at(31,"B")] });
  assert.equal(collision.ok, false);
  assert.match(collision.receipt.rejected[0].errors.join(" "), /collision/);
  const snap = await e.snapshot();
  assert.equal(snap.assertions.length, 1);
  assert.equal(snap.assertions[0].value_or_ref, "A");
});

test("verification link idempotency rejects same id with different payload", async () => {
  const verifierKeys = generateKeyPairSync("ed25519");
  const verifierPublic = verifierKeys.publicKey.export({ type:"spki", format:"pem" });
  const verifierPrivate = verifierKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const e = engine({ verificationMode:"registered-signed" });
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.registerReplica({ replica_id:rid(2), name:"Sentinel", kind:"agent", trust_mode:"signed", public_key_pem:verifierPublic });
  const assertion = at(32,"A");
  const subjectGovernance = governance();
  await e.push({ replica_id:rid(1), assertions:[assertion], governance:subjectGovernance });

  const link = {
    schema:"concord-verification-link/1",
    link_id:"vln_cccccccccccccccccccccccccccccccc",
    subject_type:"assertion",
    subject_id:assertion.assertion_id,
    subject_sha256:digest(assertion),
    verifier_ref:`replica:${rid(2)}`,
    verdict:"pass",
    receipt_ref:"sentinel:receipt:collision-1",
    verified_at:"2026-09-30T12:31:00Z",
    correlation:subjectGovernance.correlation
  };
  const signature = signPayload(verificationLinkEnvelope(link), verifierPrivate);
  const first = await e.addVerificationLink(link, signature);
  assert.equal(first.ok, true);

  const changed = { ...link, verdict:"fail" };
  const changedSignature = signPayload(verificationLinkEnvelope(changed), verifierPrivate);
  const collision = await e.addVerificationLink(changed, changedSignature);
  assert.equal(collision.status, 409);
  assert.match(collision.errors.join(" "), /collision/);
});

test("strict verification rejects subjects without correlation lineage", async () => {
  const verifierKeys = generateKeyPairSync("ed25519");
  const verifierPublic = verifierKeys.publicKey.export({ type:"spki", format:"pem" });
  const verifierPrivate = verifierKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const e = engine({ verificationMode:"registered-signed" });
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.registerReplica({ replica_id:rid(2), name:"Sentinel", kind:"agent", trust_mode:"signed", public_key_pem:verifierPublic });
  const assertion = at(33,"ungoverned");
  await e.push({ replica_id:rid(1), assertions:[assertion] });
  const link = {
    schema:"concord-verification-link/1",
    link_id:"vln_dddddddddddddddddddddddddddddddd",
    subject_type:"assertion",
    subject_id:assertion.assertion_id,
    subject_sha256:digest(assertion),
    verifier_ref:`replica:${rid(2)}`,
    verdict:"pass",
    receipt_ref:"sentinel:receipt:ungoverned",
    verified_at:"2026-09-30T12:31:00Z",
    correlation:correlation()
  };
  const signature = signPayload(verificationLinkEnvelope(link), verifierPrivate);
  const result = await e.addVerificationLink(link, signature);
  assert.equal(result.status, 409);
  assert.match(result.errors.join(" "), /correlation lineage/);
});

test("incremental pull includes existing assertions whose currentness changed", async () => {
  const e = engine();
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await e.registerReplica({ replica_id:rid(2), name:"Fo", kind:"agent" });
  await e.registerReplica({ replica_id:rid(3), name:"Observer", kind:"agent" });

  await e.push({ replica_id:rid(1), assertions:[at(41,"reachable")] });
  const checkpoint = (await e.snapshot()).revision;

  const divergent = await e.push({ replica_id:rid(2), base_revision:checkpoint, assertions:[at(42,"unreachable")] });
  const delta = await e.pull(rid(3), checkpoint);
  const states = Object.fromEntries(delta.assertions.map((a) => [a.assertion_id, a.currentness]));
  assert.equal(states[at(41,"reachable").assertion_id], "disputed");
  assert.equal(states[at(42,"unreachable").assertion_id], "disputed");

  const afterConflict = delta.revision;
  await e.resolveConflict({
    conflict_id:divergent.conflicts[0].conflict_id,
    resolution:"select",
    winner_assertion_id:at(42,"unreachable").assertion_id
  });
  const resolvedDelta = await e.pull(rid(3), afterConflict);
  const resolvedStates = Object.fromEntries(resolvedDelta.assertions.map((a) => [a.assertion_id, a.currentness]));
  assert.equal(resolvedStates[at(41,"reachable").assertion_id], "superseded");
  assert.equal(resolvedStates[at(42,"unreachable").assertion_id], "current");
});

test("governed dual-proof key rotation replaces the active replica key", async () => {
  const oldKeys = generateKeyPairSync("ed25519");
  const newKeys = generateKeyPairSync("ed25519");
  const oldPublic = oldKeys.publicKey.export({ type:"spki", format:"pem" });
  const oldPrivate = oldKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const newPublic = newKeys.publicKey.export({ type:"spki", format:"pem" });
  const newPrivate = newKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const e = engine({ governanceMode:"required" });

  await e.registerReplica({
    replica_id:rid(1),
    name:"Friday",
    kind:"agent",
    trust_mode:"signed",
    public_key_pem:oldPublic
  });

  const rotation = {
    schema:"concord-replica-key-rotation/1",
    rotation_id:"rot_11111111111111111111111111111111",
    replica_id:rid(1),
    old_key_fingerprint:publicKeyFingerprint(oldPublic),
    new_key_fingerprint:publicKeyFingerprint(newPublic),
    new_public_key_pem:newPublic,
    requested_at:"2026-09-30T12:30:00Z",
    governance:governance("replica.key.rotate")
  };
  const envelope = keyRotationEnvelope(rotation);
  const rotated = await e.rotateReplicaKey({
    rotation,
    old_signature:signPayload(envelope, oldPrivate),
    new_signature:signPayload(envelope, newPrivate)
  });
  assert.equal(rotated.ok, true);
  assert.equal(rotated.rotation.current_key_version, 2);

  const assertions = [at(51,"after-rotation")];
  const pushGovernance = governance("sync.push");
  const sync = syncEnvelope({ replica_id:rid(1), base_revision:0, assertions, governance:pushGovernance });

  const oldAttempt = await e.push({
    replica_id:rid(1),
    assertions,
    governance:pushGovernance,
    signature:signPayload(sync, oldPrivate)
  });
  assert.equal(oldAttempt.status, 401);

  const newAttempt = await e.push({
    replica_id:rid(1),
    assertions,
    governance:pushGovernance,
    signature:signPayload(sync, newPrivate)
  });
  assert.equal(newAttempt.ok, true);
});

test("key rotation fails closed on missing proof, stale request, and replay collision", async () => {
  const oldKeys = generateKeyPairSync("ed25519");
  const newKeys = generateKeyPairSync("ed25519");
  const otherKeys = generateKeyPairSync("ed25519");
  const oldPublic = oldKeys.publicKey.export({ type:"spki", format:"pem" });
  const oldPrivate = oldKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const newPublic = newKeys.publicKey.export({ type:"spki", format:"pem" });
  const newPrivate = newKeys.privateKey.export({ type:"pkcs8", format:"pem" });
  const e = engine({ governanceMode:"required" });
  await e.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent", trust_mode:"signed", public_key_pem:oldPublic });

  const base = {
    schema:"concord-replica-key-rotation/1",
    rotation_id:"rot_22222222222222222222222222222222",
    replica_id:rid(1),
    old_key_fingerprint:publicKeyFingerprint(oldPublic),
    new_key_fingerprint:publicKeyFingerprint(newPublic),
    new_public_key_pem:newPublic,
    requested_at:"2026-09-30T12:30:00Z",
    governance:governance("replica.key.rotate")
  };
  const envelope = keyRotationEnvelope(base);

  const missingNew = await e.rotateReplicaKey({
    rotation:base,
    old_signature:signPayload(envelope, oldPrivate)
  });
  assert.equal(missingNew.status, 401);

  const stale = { ...base, rotation_id:"rot_33333333333333333333333333333333", requested_at:"2026-09-30T12:00:00Z" };
  const staleEnvelope = keyRotationEnvelope(stale);
  const staleResult = await e.rotateReplicaKey({
    rotation:stale,
    old_signature:signPayload(staleEnvelope, oldPrivate),
    new_signature:signPayload(staleEnvelope, newPrivate)
  });
  assert.equal(staleResult.status, 403);
  assert.match(staleResult.errors.join(" "), /freshness window/);

  const accepted = await e.rotateReplicaKey({
    rotation:base,
    old_signature:signPayload(envelope, oldPrivate),
    new_signature:signPayload(envelope, newPrivate)
  });
  assert.equal(accepted.ok, true);

  const replay = await e.rotateReplicaKey({
    rotation:base,
    old_signature:signPayload(envelope, oldPrivate),
    new_signature:signPayload(envelope, newPrivate)
  });
  assert.equal(replay.disposition, "idempotent");

  const otherPublic = otherKeys.publicKey.export({ type:"spki", format:"pem" });
  const collision = {
    ...base,
    new_public_key_pem:otherPublic,
    new_key_fingerprint:publicKeyFingerprint(otherPublic)
  };
  const collisionResult = await e.rotateReplicaKey({ rotation:collision });
  assert.equal(collisionResult.status, 409);
  assert.match(collisionResult.errors.join(" "), /rotation_id collision/);
});

test("live governance mode fails closed without adapter and records accepted adapter evidence", async () => {
  const noAdapter = engine({ governanceMode:"live" });
  await noAdapter.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  const blocked = await noAdapter.push({ replica_id:rid(1), assertions:[at(1,"A")], governance:governance() });
  assert.equal(blocked.status, 503);

  const rejected = engine({
    governanceMode:"live",
    authorityAdapter:{ async verifyGovernance() { return {ok:false,reason:"revoked"}; } }
  });
  await rejected.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  const denied = await rejected.push({ replica_id:rid(1), assertions:[at(1,"A")], governance:governance() });
  assert.equal(denied.status, 403);
  assert.match(denied.errors.join(" "), /revoked/);

  const accepted = engine({
    governanceMode:"live",
    authorityAdapter:{ async verifyGovernance() {
      return {
        ok:true,
        decision_id:"pdr_55555555555555555555555555555555",
        audit_seq:42,
        audit_hash:"a".repeat(64),
        chain_head:"b".repeat(64),
        chain_length:43
      };
    } }
  });
  await accepted.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  const result = await accepted.push({ replica_id:rid(1), assertions:[at(1,"A")], governance:governance() });
  assert.equal(result.ok,true);
  assert.equal(result.receipt.authority_revalidation.audit_seq,42);
});

test("live verifier mode fails closed and persists provider evidence on success", async () => {
  const assertion = at(1,"A");

  const unavailable = engine({ verificationMode:"live" });
  await unavailable.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await unavailable.push({ replica_id:rid(1), assertions:[assertion] });
  const link = {
    schema:"concord-verification-link/1",
    link_id:"vln_dddddddddddddddddddddddddddddddd",
    subject_type:"assertion",
    subject_id:assertion.assertion_id,
    subject_sha256:digest(assertion),
    verifier_ref:"sentinel:verifier:live",
    verdict:"pass",
    receipt_ref:"sentinel:receipt:live-001",
    verified_at:"2026-09-30T12:31:00Z",
    correlation:correlation()
  };
  const blocked = await unavailable.addVerificationLink(link);
  assert.equal(blocked.status,503);

  const live = engine({
    verificationMode:"live",
    verifierAdapter:{ async verify(input) {
      assert.equal(input.link_id,link.link_id);
      return {ok:true,evidence:{provider:"sentinel",receipt_ref:link.receipt_ref}};
    } }
  });
  await live.registerReplica({ replica_id:rid(1), name:"Friday", kind:"agent" });
  await live.push({ replica_id:rid(1), assertions:[assertion] });
  const verified = await live.addVerificationLink(link);
  assert.equal(verified.ok,true);
  const snap = await live.snapshot();
  assert.equal(snap.verification_links[0].provider_evidence.evidence.provider,"sentinel");
});
