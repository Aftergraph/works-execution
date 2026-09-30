import { createHash, createPrivateKey, createPublicKey, sign, verify } from "node:crypto";
import { stableStringify } from "./ids.js";

export function signingBytes(payload) {
  return Buffer.from(stableStringify(payload), "utf8");
}

export function signPayload(payload, privateKeyPem) {
  const key = createPrivateKey(privateKeyPem);
  return sign(null, signingBytes(payload), key).toString("base64");
}

export function verifyPayload(payload, signatureBase64, publicKeyPem) {
  try {
    const key = createPublicKey(publicKeyPem);
    return verify(null, signingBytes(payload), key, Buffer.from(signatureBase64, "base64"));
  } catch {
    return false;
  }
}

export function syncEnvelope({ replica_id, base_revision = 0, assertions = [], governance = null }) {
  const envelope = {
    schema: "concord-sync-envelope/1",
    replica_id,
    base_revision,
    assertions
  };
  if (governance) envelope.governance = governance;
  return envelope;
}

export function verificationLinkEnvelope(link) {
  return {
    schema: "concord-verification-envelope/1",
    link
  };
}

export function checkpointAckEnvelope({ replica_id, revision, sha256 }) {
  return {
    schema: "concord-checkpoint-ack/1",
    replica_id,
    revision,
    sha256
  };
}

export function publicKeyFingerprint(publicKeyPem) {
  const key = createPublicKey(publicKeyPem);
  const der = key.export({ type:"spki", format:"der" });
  return createHash("sha256").update(der).digest("hex");
}

export function keyRotationEnvelope(rotation) {
  return {
    schema:"concord-key-rotation-envelope/1",
    rotation
  };
}
