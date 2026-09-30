import { digest } from "./ids.js";
import { validateCorrelation } from "./governance.js";

function publicAssertion(assertion) {
  return Object.fromEntries(Object.entries(assertion).filter(([key]) => !key.startsWith("_")));
}

export function expectedSubjectDigest(state, subject_type, subject_id) {
  if (subject_type === "assertion") {
    const subject = state.assertions[subject_id];
    return subject ? digest(publicAssertion(subject)) : null;
  }
  if (subject_type === "receipt") {
    const subject = state.receipts[subject_id];
    return subject?.sha256 ?? null;
  }
  return null;
}

export function subjectCorrelation(state, subject_type, subject_id) {
  if (subject_type === "assertion") return state.assertions[subject_id]?._correlation ?? null;
  if (subject_type === "receipt") return state.receipts[subject_id]?.correlation ?? null;
  return null;
}

export function subjectOriginReplica(state, subject_type, subject_id) {
  if (subject_type === "assertion") return state.assertions[subject_id]?._origin_replica_id ?? null;
  if (subject_type === "receipt") return state.receipts[subject_id]?.replica_id ?? null;
  return null;
}

export function validateVerificationLink(link, state) {
  const errors = [];
  if (!link || typeof link !== "object") return ["verification link is required"];
  if (link.schema !== "concord-verification-link/1") errors.push("schema must be concord-verification-link/1");
  if (!/^vln_[a-f0-9]{32}$/.test(link.link_id ?? "")) errors.push("invalid link_id");
  if (!["assertion","receipt"].includes(link.subject_type)) errors.push("subject_type must be assertion or receipt");
  if (!link.subject_id || typeof link.subject_id !== "string") errors.push("subject_id is required");
  if (!/^[a-f0-9]{64}$/.test(link.subject_sha256 ?? "")) errors.push("invalid subject_sha256");
  if (!["pass","fail","needs_review"].includes(link.verdict)) errors.push("invalid verdict");
  if (!link.verifier_ref || typeof link.verifier_ref !== "string") errors.push("verifier_ref is required");
  if (!link.receipt_ref || typeof link.receipt_ref !== "string") errors.push("receipt_ref is required");
  if (!link.verified_at || Number.isNaN(Date.parse(link.verified_at))) errors.push("verified_at must be an ISO-compatible datetime");
  errors.push(...validateCorrelation(link.correlation));

  const expected = expectedSubjectDigest(state, link.subject_type, link.subject_id);
  if (!expected) errors.push("verification subject does not exist");
  else if (expected !== link.subject_sha256) errors.push("verification subject digest mismatch");

  const correlation = subjectCorrelation(state, link.subject_type, link.subject_id);
  if (correlation && digest(correlation) !== digest(link.correlation)) {
    errors.push("verification correlation does not match subject correlation");
  }

  const origin = subjectOriginReplica(state, link.subject_type, link.subject_id);
  if (origin && link.verifier_ref === `replica:${origin}`) errors.push("producer replica cannot independently verify its own subject");

  return errors;
}
