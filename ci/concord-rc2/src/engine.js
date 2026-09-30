import { claimKey, validateAssertion, validateReplica } from "./protocol.js";
import { digest, id } from "./ids.js";
import { checkpointAckEnvelope, keyRotationEnvelope, publicKeyFingerprint, signPayload, syncEnvelope, verificationLinkEnvelope, verifyPayload } from "./crypto.js";
import { validateGovernanceBinding } from "./governance.js";
import { subjectCorrelation, validateVerificationLink } from "./verification.js";

function publicAssertion(assertion) {
  return Object.fromEntries(Object.entries(assertion).filter(([key]) => !key.startsWith("_")));
}

export class ConcordEngine {
  constructor(store, clock = () => new Date(), options = {}) {
    this.store = store;
    this.clock = clock;
    this.receiptPrivateKeyPem = options.receiptPrivateKeyPem ?? null;
    this.receiptKeyId = options.receiptKeyId ?? null;
    this.governanceMode = options.governanceMode ?? "optional";
    this.verificationMode = options.verificationMode ?? "reference";
    this.authorityAdapter = options.authorityAdapter ?? null;
    this.verifierAdapter = options.verifierAdapter ?? null;
    this.listeners = new Set();
  }

  subscribe(listener) {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  emit(event) {
    for (const listener of this.listeners) listener(event);
  }

  async registerReplica(input) {
    const errors = validateReplica(input);
    if (errors.length) return { ok: false, errors };
    return this.store.transact(async (state) => {
      const now = this.clock().toISOString();
      const existing = state.replicas[input.replica_id];
      if (existing?.trust_mode === "signed") {
        if ((input.trust_mode && input.trust_mode !== "signed") ||
            (input.public_key_pem && input.public_key_pem !== existing.public_key_pem)) {
          return { ok:false, status:409, errors:["signed replica identity cannot be downgraded or rotated through registration"] };
        }
      }
      const trustMode = input.trust_mode ?? existing?.trust_mode ?? "local";
      let keyFingerprint = existing?.key_fingerprint ?? null;
      if (trustMode === "signed") {
        try {
          keyFingerprint = publicKeyFingerprint(input.public_key_pem ?? existing?.public_key_pem);
        } catch {
          return { ok:false, status:400, errors:["invalid signed replica public key"] };
        }
      }
      state.replicas[input.replica_id] = {
        ...existing,
        ...input,
        created_at: existing?.created_at ?? now,
        updated_at: now,
        last_seen_revision: existing?.last_seen_revision ?? 0,
        last_ack_revision: existing?.last_ack_revision ?? 0,
        trust_mode: trustMode,
        key_fingerprint: keyFingerprint,
        key_version: existing?.key_version ?? (trustMode === "signed" ? 1 : 0)
      };
      const event = this.appendEvent(state, "replica.registered", { replica_id: input.replica_id });
      queueMicrotask(() => this.emit(event));
      return { ok: true, replica: state.replicas[input.replica_id], revision: state.revision };
    });
  }

  async push({ replica_id, base_revision = 0, assertions = [], signature = null, governance = null }) {
    return this.store.transact(async (state) => {
      const replica = state.replicas[replica_id];
      if (!replica) return { ok: false, status: 404, errors: ["unknown replica"] };
      if (!Array.isArray(assertions) || assertions.length === 0) return { ok: false, status: 400, errors: ["assertions must be non-empty"] };
      if (this.governanceMode === "required" || this.governanceMode === "live" || governance) {
        const governanceErrors = validateGovernanceBinding(governance, "sync.push", assertions, this.clock());
        if (governanceErrors.length) return { ok:false, status:403, errors:governanceErrors };
      }
      let authorityEvidence = null;
      if (this.governanceMode === "live") {
        if (!this.authorityAdapter) return { ok:false, status:503, errors:["live governance adapter unavailable"] };
        try {
          authorityEvidence = await this.authorityAdapter.verifyGovernance(governance);
        } catch (error) {
          return { ok:false, status:503, errors:[`live governance verification unavailable: ${error.message}`] };
        }
        if (!authorityEvidence?.ok) return { ok:false, status:403, errors:[authorityEvidence?.reason ?? "live governance rejected"] };
      }
      if (replica.trust_mode === "signed") {
        const envelope = syncEnvelope({ replica_id, base_revision, assertions, governance });
        if (!signature || !verifyPayload(envelope, signature, replica.public_key_pem)) {
          return { ok: false, status: 401, errors: ["invalid or missing replica signature"] };
        }
      }

      const startRevision = state.revision;
      const accepted = [];
      const rejected = [];
      const conflicts = [];
      const now = this.clock();

      for (const incomingRaw of assertions) {
        const incoming = structuredClone(incomingRaw);
        const errors = validateAssertion(incoming, now);
        if (errors.length) {
          rejected.push({ assertion_id: incoming?.assertion_id ?? null, errors });
          continue;
        }

        if (state.assertions[incoming.assertion_id]) {
          const existingDigest = digest(publicAssertion(state.assertions[incoming.assertion_id]));
          const incomingDigest = digest(incoming);
          if (existingDigest !== incomingDigest) {
            rejected.push({ assertion_id: incoming.assertion_id, errors:["assertion_id collision with different payload"] });
          } else {
            accepted.push({ assertion_id: incoming.assertion_id, disposition: "idempotent" });
          }
          continue;
        }

        const key = claimKey(incoming);
        const peers = Object.values(state.assertions).filter((a) =>
          a._claim_key === key && a.currentness === "current"
        );

        const same = peers.find((a) => a.value_or_ref === incoming.value_or_ref);
        const different = peers.filter((a) => a.value_or_ref !== incoming.value_or_ref);

        incoming._claim_key = key;
        incoming._origin_replica_id = replica_id;
        incoming._ingested_at = now.toISOString();
        incoming._correlation = governance?.correlation ?? null;
        incoming._governance_binding_sha256 = governance ? digest(governance) : null;

        if (incoming.currentness === "current" && different.length) {
          incoming.currentness = "disputed";
          for (const peer of different) {
            peer.currentness = "disputed";
            const peerEvent = this.appendEvent(state, "assertion.currentness_changed", {
              assertion_id:peer.assertion_id,
              currentness:"disputed",
              reason:"concurrent_semantic_divergence"
            });
            peer._last_revision = peerEvent.revision;
          }
          const conflict_id = id("cfl");
          const conflict = {
            schema: "concord-conflict/1",
            conflict_id,
            claim_key: key,
            assertion_ids: [...different.map((a) => a.assertion_id), incoming.assertion_id],
            state: "HOLD",
            reason: "concurrent_semantic_divergence",
            created_at: now.toISOString()
          };
          state.conflicts[conflict_id] = conflict;
          conflicts.push(conflict);
        } else if (incoming.currentness === "current" && same) {
          same.currentness = "superseded";
          const sameEvent = this.appendEvent(state, "assertion.currentness_changed", {
            assertion_id:same.assertion_id,
            currentness:"superseded",
            reason:"same_value_superseded"
          });
          same._last_revision = sameEvent.revision;
        }

        state.assertions[incoming.assertion_id] = incoming;
        accepted.push({
          assertion_id: incoming.assertion_id,
          disposition: incoming.currentness === "disputed" ? "held_conflict" : "accepted"
        });
        const ingestEvent = this.appendEvent(state, "assertion.ingested", {
          replica_id,
          assertion_id: incoming.assertion_id,
          currentness: incoming.currentness
        });
        incoming._last_revision = ingestEvent.revision;
      }

      state.replicas[replica_id].updated_at = now.toISOString();

      const receipt_id = id("rcp");
      const receiptBody = {
        schema: "concord-sync-receipt/1",
        receipt_id,
        replica_id,
        base_revision,
        resulting_revision: state.revision + 1,
        accepted,
        rejected,
        conflict_ids: conflicts.map((c) => c.conflict_id),
        recorded_at: now.toISOString(),
        governance_binding_sha256: governance ? digest(governance) : null,
        correlation: governance?.correlation ?? null,
        authority_ref: governance?.authority_ref ?? null,
        decision_ref: governance?.decision_ref ?? null,
        authority_revalidation: authorityEvidence
      };
      const receipt = { ...receiptBody, sha256: digest(receiptBody) };
      if (this.receiptPrivateKeyPem) {
        receipt.signature_alg = "Ed25519";
        receipt.signer_key_id = this.receiptKeyId ?? "concord-node";
        receipt.signature = signPayload(receiptBody, this.receiptPrivateKeyPem);
      }
      state.receipts[receipt.receipt_id] = receipt;
      const receiptEvent = this.appendEvent(state, "sync.receipted", {
        replica_id,
        receipt_id: receipt.receipt_id,
        accepted: accepted.length,
        rejected: rejected.length,
        conflicts: conflicts.length
      });
      state.replicas[replica_id].last_seen_revision = state.revision;
      queueMicrotask(() => this.emit(receiptEvent));

      return {
        ok: rejected.length === 0,
        status: rejected.length ? 207 : 200,
        drifted: base_revision !== 0 && base_revision !== startRevision,
        receipt,
        conflicts
      };
    });
  }

  async pull(replica_id, since = 0) {
    const state = await this.store.read();
    if (!state.replicas[replica_id]) return { ok: false, status: 404, errors: ["unknown replica"] };
    return {
      ok: true,
      revision: state.revision,
      events: state.events.filter((event) => event.revision > since),
      assertions: Object.values(state.assertions).filter((a) => {
        if (Number.isInteger(a._last_revision)) return a._last_revision > since;
        const ev = state.events.find((e) => e.payload?.assertion_id === a.assertion_id);
        return ev ? ev.revision > since : since === 0;
      }),
      conflicts: Object.values(state.conflicts).filter((c) => c.state === "HOLD"),
      checkpoint: {
        schema: "concord-checkpoint/1",
        replica_id,
        revision: state.revision,
        sha256: digest({ replica_id, revision: state.revision })
      }
    };
  }

  async acknowledgeCheckpoint({ replica_id, revision, sha256, signature = null }) {
    return this.store.transact(async (state) => {
      const replica = state.replicas[replica_id];
      if (!replica) return { ok: false, status: 404, errors: ["unknown replica"] };
      if (replica.trust_mode === "signed") {
        const envelope = checkpointAckEnvelope({ replica_id, revision, sha256 });
        if (!signature || !verifyPayload(envelope, signature, replica.public_key_pem)) {
          return { ok:false, status:401, errors:["invalid or missing checkpoint signature"] };
        }
      }
      if (!Number.isInteger(revision) || revision < 0 || revision > state.revision) {
        return { ok: false, status: 400, errors: ["invalid checkpoint revision"] };
      }
      const expected = digest({ replica_id, revision });
      if (sha256 !== expected) return { ok: false, status: 400, errors: ["checkpoint digest mismatch"] };
      replica.last_ack_revision = Math.max(replica.last_ack_revision ?? 0, revision);
      replica.updated_at = this.clock().toISOString();
      const event = this.appendEvent(state, "checkpoint.acknowledged", { replica_id, revision });
      queueMicrotask(() => this.emit(event));
      return { ok: true, replica_id, acknowledged_revision: replica.last_ack_revision, revision: state.revision };
    });
  }

  async resolveConflict({ conflict_id, resolution, winner_assertion_id, governance = null }) {
    return this.store.transact(async (state) => {
      const conflict = state.conflicts[conflict_id];
      if (!conflict) return { ok: false, status: 404, errors: ["unknown conflict"] };
      if (this.governanceMode === "required" || this.governanceMode === "live" || governance) {
        const governanceErrors = validateGovernanceBinding(governance, "conflict.resolve", [], this.clock());
        if (governanceErrors.length) return { ok:false, status:403, errors:governanceErrors };
      }
      let authorityEvidence = null;
      if (this.governanceMode === "live") {
        if (!this.authorityAdapter) return { ok:false, status:503, errors:["live governance adapter unavailable"] };
        try {
          authorityEvidence = await this.authorityAdapter.verifyGovernance(governance);
        } catch (error) {
          return { ok:false, status:503, errors:[`live governance verification unavailable: ${error.message}`] };
        }
        if (!authorityEvidence?.ok) return { ok:false, status:403, errors:[authorityEvidence?.reason ?? "live governance rejected"] };
      }
      if (conflict.state !== "HOLD") return { ok: true, conflict };

      if (resolution === "select") {
        if (!conflict.assertion_ids.includes(winner_assertion_id)) {
          return { ok: false, status: 400, errors: ["winner_assertion_id is not in conflict"] };
        }
        for (const assertion_id of conflict.assertion_ids) {
          const assertion = state.assertions[assertion_id];
          assertion.currentness = assertion_id === winner_assertion_id ? "current" : "superseded";
          const transition = this.appendEvent(state, "assertion.currentness_changed", {
            assertion_id,
            currentness:assertion.currentness,
            reason:"conflict_resolution"
          });
          assertion._last_revision = transition.revision;
        }
      } else if (resolution === "invalidate") {
        for (const assertion_id of conflict.assertion_ids) {
          const assertion = state.assertions[assertion_id];
          assertion.currentness = "superseded";
          const transition = this.appendEvent(state, "assertion.currentness_changed", {
            assertion_id,
            currentness:"superseded",
            reason:"conflict_invalidation"
          });
          assertion._last_revision = transition.revision;
        }
      } else {
        return { ok: false, status: 400, errors: ["resolution must be select or invalidate"] };
      }

      conflict.state = "RESOLVED";
      conflict.resolution = resolution;
      conflict.winner_assertion_id = winner_assertion_id ?? null;
      conflict.resolved_at = this.clock().toISOString();
      const event = this.appendEvent(state, "conflict.resolved", { conflict_id, resolution, winner_assertion_id, governance_binding_sha256: governance ? digest(governance) : null, authority_revalidation: authorityEvidence });
      queueMicrotask(() => this.emit(event));
      return { ok: true, conflict };
    });
  }

  async rotateReplicaKey({ rotation, old_signature = null, new_signature = null }) {
    return this.store.transact(async (state) => {
      if (!rotation || typeof rotation !== "object") {
        return { ok:false, status:400, errors:["rotation is required"] };
      }
      if (rotation.schema !== "concord-replica-key-rotation/1") {
        return { ok:false, status:400, errors:["rotation schema must be concord-replica-key-rotation/1"] };
      }
      if (!/^rot_[a-f0-9]{32}$/.test(rotation.rotation_id ?? "")) {
        return { ok:false, status:400, errors:["invalid rotation_id"] };
      }

      const existingRecord = state.key_rotations?.[rotation.rotation_id];
      if (existingRecord) {
        if (digest(existingRecord.rotation) !== digest(rotation)) {
          return { ok:false, status:409, errors:["rotation_id collision with different payload"] };
        }
        return { ok:true, disposition:"idempotent", rotation:existingRecord };
      }

      const replica = state.replicas[rotation.replica_id];
      if (!replica) return { ok:false, status:404, errors:["unknown replica"] };
      if (replica.trust_mode !== "signed" || !replica.public_key_pem) {
        return { ok:false, status:409, errors:["key rotation requires an existing signed replica"] };
      }

      const governanceErrors = validateGovernanceBinding(rotation.governance, "replica.key.rotate", [], this.clock());
      if (governanceErrors.length) return { ok:false, status:403, errors:governanceErrors };

      if (!rotation.requested_at || Number.isNaN(Date.parse(rotation.requested_at))) {
        return { ok:false, status:400, errors:["requested_at must be an ISO-compatible datetime"] };
      }
      const ageMs = Math.abs(this.clock().getTime() - Date.parse(rotation.requested_at));
      if (ageMs > 5 * 60 * 1000) {
        return { ok:false, status:403, errors:["key rotation request is outside the freshness window"] };
      }

      let currentFingerprint;
      let newFingerprint;
      try {
        currentFingerprint = replica.key_fingerprint ?? publicKeyFingerprint(replica.public_key_pem);
        newFingerprint = publicKeyFingerprint(rotation.new_public_key_pem);
      } catch {
        return { ok:false, status:400, errors:["invalid key material"] };
      }

      if (rotation.old_key_fingerprint !== currentFingerprint) {
        return { ok:false, status:409, errors:["old_key_fingerprint does not match current replica key"] };
      }
      if (rotation.new_key_fingerprint !== newFingerprint) {
        return { ok:false, status:400, errors:["new_key_fingerprint does not match new_public_key_pem"] };
      }
      if (newFingerprint === currentFingerprint) {
        return { ok:false, status:409, errors:["new key must differ from current key"] };
      }

      const envelope = keyRotationEnvelope(rotation);
      if (!old_signature || !verifyPayload(envelope, old_signature, replica.public_key_pem)) {
        return { ok:false, status:401, errors:["invalid or missing old-key rotation signature"] };
      }
      if (!new_signature || !verifyPayload(envelope, new_signature, rotation.new_public_key_pem)) {
        return { ok:false, status:401, errors:["invalid or missing new-key rotation signature"] };
      }

      const previousVersion = replica.key_version ?? 1;
      replica.public_key_pem = rotation.new_public_key_pem;
      replica.key_fingerprint = newFingerprint;
      replica.key_version = previousVersion + 1;
      replica.updated_at = this.clock().toISOString();

      state.key_rotations ??= {};
      const record = {
        schema:"concord-key-rotation-record/1",
        rotation:structuredClone(rotation),
        previous_key_fingerprint:currentFingerprint,
        current_key_fingerprint:newFingerprint,
        previous_key_version:previousVersion,
        current_key_version:replica.key_version,
        applied_at:this.clock().toISOString()
      };
      state.key_rotations[rotation.rotation_id] = record;
      const event = this.appendEvent(state, "replica.key_rotated", {
        rotation_id:rotation.rotation_id,
        replica_id:rotation.replica_id,
        previous_key_fingerprint:currentFingerprint,
        current_key_fingerprint:newFingerprint,
        key_version:replica.key_version,
        governance_binding_sha256:digest(rotation.governance)
      });
      queueMicrotask(() => this.emit(event));
      return { ok:true, rotation:record, revision:state.revision };
    });
  }

  async addVerificationLink(link, signature = null) {
    return this.store.transact(async (state) => {
      if (state.verification_links[link?.link_id]) {
        const existing = state.verification_links[link.link_id];
        if (digest(existing) !== digest(link)) {
          return { ok:false, status:409, errors:["verification link_id collision with different payload"] };
        }
        return { ok:true, link:existing, disposition:"idempotent" };
      }
      const errors = validateVerificationLink(link, state);
      if (errors.length) return { ok:false, status:400, errors };

      if (this.verificationMode === "registered-signed") {
        if (!subjectCorrelation(state, link.subject_type, link.subject_id)) {
          return { ok:false, status:409, errors:["registered-signed verification requires subject correlation lineage"] };
        }
        const match = /^replica:(agt_[a-f0-9]{32})$/.exec(link.verifier_ref ?? "");
        if (!match) return { ok:false, status:403, errors:["registered-signed verification requires verifier_ref=replica:<signed replica id>"] };
        const verifier = state.replicas[match[1]];
        if (!verifier || verifier.trust_mode !== "signed" || !verifier.public_key_pem) {
          return { ok:false, status:403, errors:["verifier replica is not registered as signed"] };
        }
        if (!signature || !verifyPayload(verificationLinkEnvelope(link), signature, verifier.public_key_pem)) {
          return { ok:false, status:401, errors:["invalid or missing verifier signature"] };
        }
      }

      let verifierEvidence = null;
      if (this.verificationMode === "live") {
        if (!this.verifierAdapter) return { ok:false, status:503, errors:["live verifier adapter unavailable"] };
        try {
          verifierEvidence = await this.verifierAdapter.verify(link);
        } catch (error) {
          return { ok:false, status:503, errors:[`live verifier unavailable: ${error.message}`] };
        }
        if (!verifierEvidence?.ok) return { ok:false, status:403, errors:[verifierEvidence?.reason ?? "live verifier rejected"] };
      }

      state.verification_links[link.link_id] = {
        ...structuredClone(link),
        provider_evidence: verifierEvidence
      };
      const event = this.appendEvent(state, "verification.linked", {
        link_id: link.link_id,
        subject_type: link.subject_type,
        subject_id: link.subject_id,
        verdict: link.verdict
      });
      queueMicrotask(() => this.emit(event));
      return { ok:true, link:state.verification_links[link.link_id], revision:state.revision };
    });
  }

  async snapshot() {
    const state = await this.store.read();
    return {
      schema: "concord-snapshot/1",
      generated_at: this.clock().toISOString(),
      revision: state.revision,
      replicas: Object.values(state.replicas),
      assertions: Object.values(state.assertions),
      conflicts: Object.values(state.conflicts),
      receipts: Object.values(state.receipts),
      verification_links: Object.values(state.verification_links ?? {}),
      key_rotations: Object.values(state.key_rotations ?? {}),
      merkle_hint_sha256: digest({
        revision: state.revision,
        assertions: Object.keys(state.assertions).sort(),
        receipts: Object.keys(state.receipts).sort()
      })
    };
  }

  appendEvent(state, type, payload) {
    state.revision += 1;
    const event = {
      schema: "concord-event/1",
      event_id: id("evt"),
      revision: state.revision,
      type,
      occurred_at: this.clock().toISOString(),
      payload
    };
    state.events.push(event);
    return event;
  }
}
