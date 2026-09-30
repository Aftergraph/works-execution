const ID = {
  execution_context_id: /^ctx_[a-f0-9]{32}$/,
  tenant_id: /^ten_[a-f0-9]{32}$/,
  principal_id: /^prn_[a-f0-9]{32}$/,
  authority_lease_id: /^auth_[a-f0-9]{32}$/,
  work_id: /^wrk_[a-f0-9]{32}$/,
  admission_decision_id: /^pdr_[a-f0-9]{32}$/,
  trace_id: /^trc_[a-f0-9]{32}$/,
  action_id: /^act_[a-f0-9]{32}$/
};

export function validateCorrelation(correlation) {
  const errors = [];
  if (!correlation || typeof correlation !== "object") return ["correlation is required"];
  if (correlation.schema !== "correlation/1.0") errors.push("correlation.schema must be correlation/1.0");
  for (const [field, pattern] of Object.entries(ID)) {
    if (!pattern.test(correlation[field] ?? "")) errors.push(`invalid correlation.${field}`);
  }
  if (!correlation.mission_id || typeof correlation.mission_id !== "string") errors.push("correlation.mission_id is required");
  return errors;
}

export function validateGovernanceBinding(binding, operation, assertions = [], now = new Date()) {
  const errors = [];
  if (!binding || typeof binding !== "object") return ["governance binding is required"];
  if (binding.schema !== "concord-governance-binding/1") errors.push("schema must be concord-governance-binding/1");
  if (binding.operation !== operation) errors.push(`operation must be ${operation}`);
  errors.push(...validateCorrelation(binding.correlation));
  for (const field of ["authority_ref","capability_ref","decision_ref","observed_at","valid_until"]) {
    if (!binding[field] || typeof binding[field] !== "string") errors.push(`${field} is required`);
  }
  if (binding.decision !== "allow") errors.push("governance decision must be allow");
  if (binding.correlation?.authority_lease_id && binding.authority_ref &&
      !binding.authority_ref.endsWith(`:${binding.correlation.authority_lease_id}`)) {
    errors.push("authority_ref must bind the correlation authority_lease_id");
  }
  if (binding.correlation?.admission_decision_id && binding.decision_ref &&
      !binding.decision_ref.endsWith(`:${binding.correlation.admission_decision_id}`)) {
    errors.push("decision_ref must bind the correlation admission_decision_id");
  }
  for (const field of ["observed_at","valid_until"]) {
    if (binding[field] && Number.isNaN(Date.parse(binding[field]))) errors.push(`${field} must be an ISO-compatible datetime`);
  }
  if (binding.valid_until && Date.parse(binding.valid_until) < now.getTime()) errors.push("governance binding is expired");
  if (binding.correlation?.tenant_id) {
    for (const assertion of assertions) {
      if (assertion?.tenant_id !== binding.correlation.tenant_id) errors.push("assertion tenant_id must match correlation tenant_id");
    }
  }
  return errors;
}
