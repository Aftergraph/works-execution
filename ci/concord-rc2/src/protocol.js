const ID = /^[a-z]{3,8}_[a-f0-9]{32}$/;

export function validateReplica(replica) {
  const errors = [];
  if (!replica || typeof replica !== "object") errors.push("replica must be an object");
  if (!replica?.replica_id || !ID.test(replica.replica_id)) errors.push("replica_id must be a prefixed 32-hex id");
  if (!replica?.name || typeof replica.name !== "string") errors.push("name is required");
  if (!["agent","service","human-tool"].includes(replica?.kind)) errors.push("kind must be agent, service, or human-tool");
  const trustMode = replica?.trust_mode ?? "local";
  if (!["local","signed"].includes(trustMode)) errors.push("trust_mode must be local or signed");
  if (trustMode === "signed" && (!replica?.public_key_pem || !String(replica.public_key_pem).includes("BEGIN PUBLIC KEY"))) {
    errors.push("signed replicas require public_key_pem");
  }
  return errors;
}

export function validateAssertion(a, now = new Date()) {
  const errors = [];
  const required = [
    "schema","assertion_id","subject","predicate","value_or_ref","epistemic","currentness",
    "source_refs","evidence_refs","observed_at","evidence_observed_at","asserted_at","valid_until",
    "tenant_id","domain","classification","consent_record","consent_version","purpose","evidence_requirement"
  ];
  for (const key of required) if (a?.[key] === undefined || a?.[key] === null || a?.[key] === "") errors.push(`missing ${key}`);
  if (a?.schema !== "world-assertion/0.1") errors.push("schema must be world-assertion/0.1");
  if (!/^ast_[a-f0-9]{32}$/.test(a?.assertion_id ?? "")) errors.push("invalid assertion_id");
  if (!/^ten_[a-f0-9]{32}$/.test(a?.tenant_id ?? "")) errors.push("invalid tenant_id");
  if (!["observed","inferred","predicted","unknown"].includes(a?.epistemic)) errors.push("invalid epistemic");
  if (!["current","stale","disputed","superseded"].includes(a?.currentness)) errors.push("invalid currentness");
  if (!Array.isArray(a?.source_refs) || a.source_refs.length === 0) errors.push("source_refs must be non-empty");
  if (!Array.isArray(a?.evidence_refs) || a.evidence_refs.length === 0) errors.push("evidence_refs must be non-empty");
  if (!["none","current_observed"].includes(a?.evidence_requirement)) errors.push("invalid evidence_requirement");
  for (const key of ["observed_at","evidence_observed_at","asserted_at","valid_until"]) {
    if (a?.[key] && Number.isNaN(Date.parse(a[key]))) errors.push(`${key} must be an ISO-compatible datetime`);
  }
  if (a?.valid_until && Date.parse(a.valid_until) < now.getTime() && a.currentness === "current") {
    errors.push("current assertion is past valid_until");
  }
  if (a?.evidence_requirement === "current_observed" && a?.epistemic !== "observed") {
    errors.push("current_observed evidence requires epistemic=observed");
  }
  return errors;
}

export function claimKey(a) {
  return `${a.tenant_id}\u0000${a.domain}\u0000${a.subject}\u0000${a.predicate}`;
}
