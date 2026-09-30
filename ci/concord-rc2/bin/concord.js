#!/usr/bin/env node
import { readFile } from "node:fs/promises";
import { ConcordClient } from "../src/client.js";
import { checkpointAckEnvelope, keyRotationEnvelope, publicKeyFingerprint, signPayload, syncEnvelope, verificationLinkEnvelope } from "../src/crypto.js";
import { createPublicKey } from "node:crypto";
import { id } from "../src/ids.js";

const [command, ...args] = process.argv.slice(2);
const baseUrl = process.env.CONCORD_URL ?? "http://127.0.0.1:8787";
const client = new ConcordClient(baseUrl, process.env.CONCORD_TOKEN ?? "");

function out(value) {
  process.stdout.write(JSON.stringify(value, null, 2) + "\n");
}

async function readJson(path) {
  return JSON.parse(await readFile(path, "utf8"));
}

try {
  if (command === "health") out(await client.health());
  else if (command === "ready") out(await client.ready());
  else if (command === "metrics") out(await client.metrics());
  else if (command === "state") out(await client.state());
  else if (command === "export") out(await client.exportSnapshot());
  else if (command === "register") {
    const [replica_id, name, kind = "agent"] = args;
    if (!replica_id || !name) throw new Error("usage: concord register <replica_id> <name> [kind]");
    out(await client.registerReplica({ replica_id, name, kind }));
  } else if (command === "push") {
    const [replica_id, file, base = "0"] = args;
    if (!replica_id || !file) throw new Error("usage: concord push <replica_id> <assertions.json> [base_revision]");
    const parsed = await readJson(file);
    const assertions = Array.isArray(parsed) ? parsed : parsed.assertions;
    out(await client.push({ replica_id, base_revision:Number(base), assertions }));
  } else if (command === "push-signed") {
    const [replica_id, file, keyFile, base = "0"] = args;
    if (!replica_id || !file || !keyFile) throw new Error("usage: concord push-signed <replica_id> <assertions.json> <private-key.pem> [base_revision]");
    const parsed = await readJson(file);
    const assertions = Array.isArray(parsed) ? parsed : parsed.assertions;
    const base_revision = Number(base);
    const privateKeyPem = await readFile(keyFile, "utf8");
    const signature = signPayload(syncEnvelope({ replica_id, base_revision, assertions }), privateKeyPem);
    out(await client.push({ replica_id, base_revision, assertions, signature }));
  } else if (command === "push-governed") {
    const [replica_id, file, governanceFile, base = "0"] = args;
    if (!replica_id || !file || !governanceFile) throw new Error("usage: concord push-governed <replica_id> <assertions.json> <governance.json> [base_revision]");
    const parsed = await readJson(file);
    const assertions = Array.isArray(parsed) ? parsed : parsed.assertions;
    const governance = await readJson(governanceFile);
    out(await client.push({ replica_id, base_revision:Number(base), assertions, governance }));
  } else if (command === "push-signed-governed") {
    const [replica_id, file, governanceFile, keyFile, base = "0"] = args;
    if (!replica_id || !file || !governanceFile || !keyFile) throw new Error("usage: concord push-signed-governed <replica_id> <assertions.json> <governance.json> <private-key.pem> [base_revision]");
    const parsed = await readJson(file);
    const assertions = Array.isArray(parsed) ? parsed : parsed.assertions;
    const governance = await readJson(governanceFile);
    const base_revision = Number(base);
    const privateKeyPem = await readFile(keyFile, "utf8");
    const signature = signPayload(syncEnvelope({ replica_id, base_revision, assertions, governance }), privateKeyPem);
    out(await client.push({ replica_id, base_revision, assertions, governance, signature }));
  } else if (command === "pull") {
    const [replica_id, since = "0"] = args;
    if (!replica_id) throw new Error("usage: concord pull <replica_id> [since_revision]");
    out(await client.pull(replica_id, Number(since)));
  } else if (command === "ack") {
    const [replica_id, revision, sha256] = args;
    if (!replica_id || revision === undefined || !sha256) throw new Error("usage: concord ack <replica_id> <revision> <sha256>");
    out(await client.acknowledge({ replica_id, revision:Number(revision), sha256 }));
  } else if (command === "ack-signed") {
    const [replica_id, revision, sha256, keyFile] = args;
    if (!replica_id || revision === undefined || !sha256 || !keyFile) throw new Error("usage: concord ack-signed <replica_id> <revision> <sha256> <private-key.pem>");
    const payload = { replica_id, revision:Number(revision), sha256 };
    const privateKeyPem = await readFile(keyFile, "utf8");
    const signature = signPayload(checkpointAckEnvelope(payload), privateKeyPem);
    out(await client.acknowledge({ ...payload, signature }));
  } else if (command === "rotate-key") {
    const [replica_id, governanceFile, oldKeyFile, newKeyFile] = args;
    if (!replica_id || !governanceFile || !oldKeyFile || !newKeyFile) {
      throw new Error("usage: concord rotate-key <replica_id> <governance.json> <old-private-key.pem> <new-private-key.pem>");
    }
    const governance = await readJson(governanceFile);
    const oldPrivateKeyPem = await readFile(oldKeyFile, "utf8");
    const newPrivateKeyPem = await readFile(newKeyFile, "utf8");
    const oldPublicKeyPem = createPublicKey(oldPrivateKeyPem).export({ type:"spki", format:"pem" });
    const newPublicKeyPem = createPublicKey(newPrivateKeyPem).export({ type:"spki", format:"pem" });
    const rotation = {
      schema:"concord-replica-key-rotation/1",
      rotation_id:id("rot"),
      replica_id,
      old_key_fingerprint:publicKeyFingerprint(oldPublicKeyPem),
      new_key_fingerprint:publicKeyFingerprint(newPublicKeyPem),
      new_public_key_pem:newPublicKeyPem,
      requested_at:new Date().toISOString(),
      governance
    };
    const envelope = keyRotationEnvelope(rotation);
    out(await client.rotateReplicaKey(replica_id, {
      rotation,
      old_signature:signPayload(envelope, oldPrivateKeyPem),
      new_signature:signPayload(envelope, newPrivateKeyPem)
    }));
  } else if (command === "verify-link") {
    const [file] = args;
    if (!file) throw new Error("usage: concord verify-link <verification-link.json>");
    out(await client.addVerificationLink(await readJson(file)));
  } else if (command === "verify-link-signed") {
    const [file, keyFile] = args;
    if (!file || !keyFile) throw new Error("usage: concord verify-link-signed <verification-link.json> <private-key.pem>");
    const link = await readJson(file);
    const privateKeyPem = await readFile(keyFile, "utf8");
    const signature = signPayload(verificationLinkEnvelope(link), privateKeyPem);
    out(await client.addVerificationLink(link, signature));
  } else if (command === "resolve") {
    const [conflict_id, resolution, winner_assertion_id] = args;
    if (!conflict_id || !resolution) throw new Error("usage: concord resolve <conflict_id> <select|invalidate> [winner_assertion_id]");
    out(await client.resolve(conflict_id, { resolution, winner_assertion_id }));
  } else if (command === "resolve-governed") {
    const [conflict_id, resolution, governanceFile, winner_assertion_id] = args;
    if (!conflict_id || !resolution || !governanceFile) throw new Error("usage: concord resolve-governed <conflict_id> <select|invalidate> <governance.json> [winner_assertion_id]");
    out(await client.resolve(conflict_id, { resolution, winner_assertion_id, governance:await readJson(governanceFile) }));
  } else {
    throw new Error("commands: health | ready | metrics | state | export | register | push | push-signed | push-governed | push-signed-governed | pull | ack | ack-signed | rotate-key | verify-link | verify-link-signed | resolve | resolve-governed");
  }
} catch (error) {
  process.stderr.write(`concord: ${error.message}\n`);
  if (error.payload) process.stderr.write(JSON.stringify(error.payload, null, 2) + "\n");
  process.exitCode = 1;
}
