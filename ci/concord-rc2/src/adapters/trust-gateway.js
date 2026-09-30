import { createHash } from "node:crypto";
import { JsonHttpClient } from "./http.js";

function admissionDecisionIdForEntry(entry) {
  if (!Number.isInteger(entry?.seq) || !/^[a-f0-9]{64}$/i.test(entry?.hash ?? "")) return null;
  return "pdr_" + createHash("sha256")
    .update(`trust-gateway\0action_decision\0${entry.seq}\0${entry.hash}`, "utf8")
    .digest("hex")
    .slice(0, 32);
}

function field(payload, name) {
  return payload?.correlation?.[name] ?? payload?.[name] ?? payload?.context?.[name] ?? null;
}

export class TrustGatewayAuditAdapter {
  constructor({ baseUrl, token, timeoutMs = 5000, maxPages = 50, pageSize = 500 } = {}) {
    this.http = new JsonHttpClient({ baseUrl, token, timeoutMs });
    this.maxPages = maxPages;
    this.pageSize = pageSize;
  }

  async verifyGovernance(binding) {
    const chain = await this.http.request("/v1/audit/verify");
    if (chain?.ok !== true) {
      return { ok:false, reason:"trust_gateway_audit_chain_invalid", evidence:chain };
    }

    const c = binding?.correlation ?? {};
    const expectedDecision = c.admission_decision_id;
    let since = 0;

    for (let page = 0; page < this.maxPages; page += 1) {
      const audit = await this.http.request(`/v1/audit?since=${since}&limit=${this.pageSize}`);
      const entries = Array.isArray(audit?.entries) ? audit.entries : [];

      for (const entry of entries) {
        if (entry?.payload?.type !== "action_decision") continue;
        const decisionId = entry.payload.admission_decision_id ?? admissionDecisionIdForEntry(entry);
        if (decisionId !== expectedDecision) continue;

        const checks = [
          ["tenant_id", c.tenant_id],
          ["principal_id", c.principal_id],
          ["mission_id", c.mission_id],
          ["authority_lease_id", c.authority_lease_id],
          ["action_id", c.action_id]
        ];
        for (const [name, expected] of checks) {
          const actual = field(entry.payload, name);
          if (!actual) return { ok:false, reason:`trust_gateway_missing_${name}`, decision_id:decisionId };
          if (actual !== expected) return { ok:false, reason:`trust_gateway_${name}_mismatch`, expected, actual, decision_id:decisionId };
        }

        const decision = entry.payload.decision ?? entry.payload.result ?? entry.payload.verdict;
        if (decision !== "allow") return { ok:false, reason:"trust_gateway_decision_not_allow", decision, decision_id:decisionId };

        if (!String(binding?.decision_ref ?? "").endsWith(decisionId)) {
          return { ok:false, reason:"decision_ref_does_not_bind_admission_decision", decision_id:decisionId };
        }
        if (!String(binding?.authority_ref ?? "").endsWith(c.authority_lease_id)) {
          return { ok:false, reason:"authority_ref_does_not_bind_authority_lease" };
        }

        return {
          ok:true,
          decision_id:decisionId,
          audit_seq:entry.seq,
          audit_hash:entry.hash,
          chain_head:chain.head,
          chain_length:chain.length
        };
      }

      if (entries.length === 0) break;
      const lastSeq = entries.at(-1)?.seq;
      const next = Number.isInteger(audit?.nextSince) ? audit.nextSince :
        (Number.isInteger(lastSeq) ? lastSeq + 1 : null);
      if (!Number.isInteger(next) || next <= since) break;
      since = next;
    }

    return { ok:false, reason:"trust_gateway_decision_not_found", decision_id:expectedDecision };
  }
}

export { admissionDecisionIdForEntry };
