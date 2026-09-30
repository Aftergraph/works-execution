import test from "node:test";
import assert from "node:assert/strict";
import { createServer } from "node:http";
import { once } from "node:events";
import { TrustGatewayAuditAdapter, admissionDecisionIdForEntry } from "../src/adapters/trust-gateway.js";
import { HttpVerifierProviderAdapter } from "../src/adapters/verifier-provider.js";

async function withServer(handler, fn) {
  const server = createServer(handler);
  server.listen(0, "127.0.0.1");
  await once(server, "listening");
  const addr = server.address();
  try {
    await fn(`http://127.0.0.1:${addr.port}`);
  } finally {
    server.close();
    await once(server, "close");
  }
}

const correlation = {
  schema:"correlation/1.0",
  execution_context_id:"ctx_11111111111111111111111111111111",
  tenant_id:"ten_22222222222222222222222222222222",
  principal_id:"prn_33333333333333333333333333333333",
  mission_id:"mission:test",
  authority_lease_id:"auth_44444444444444444444444444444444",
  work_id:"wrk_55555555555555555555555555555555",
  admission_decision_id:"",
  trace_id:"trc_66666666666666666666666666666666",
  action_id:"act_77777777777777777777777777777777"
};

test("TrustGatewayAuditAdapter verifies sealed exact-correlated allow decision", async () => {
  const entry = {
    seq:7,
    hash:"a".repeat(64),
    payload:{
      type:"action_decision",
      decision:"allow",
      correlation:{ ...correlation }
    }
  };
  const decisionId = admissionDecisionIdForEntry(entry);
  entry.payload.correlation.admission_decision_id = decisionId;

  await withServer((req,res) => {
    res.setHeader("content-type","application/json");
    if (req.url === "/v1/audit/verify") return res.end(JSON.stringify({ok:true,length:8,head:"b".repeat(64)}));
    if (req.url.startsWith("/v1/audit?")) return res.end(JSON.stringify({entries:[entry]}));
    res.statusCode=404; res.end(JSON.stringify({error:"not_found"}));
  }, async (baseUrl) => {
    const adapter = new TrustGatewayAuditAdapter({baseUrl,token:"test-token"});
    const binding = {
      schema:"concord-governance-binding/1",
      operation:"sync.push",
      correlation:{ ...entry.payload.correlation },
      authority_ref:`trust-gateway:authority:${correlation.authority_lease_id}`,
      capability_ref:"trust-gateway:capability:worker",
      decision_ref:`trust-gateway:decision:${decisionId}`,
      decision:"allow",
      observed_at:"2026-10-01T00:00:00Z",
      valid_until:"2099-12-31T23:59:59Z"
    };
    const result = await adapter.verifyGovernance(binding);
    assert.equal(result.ok,true);
    assert.equal(result.decision_id,decisionId);
    assert.equal(result.audit_seq,7);
  });
});

test("TrustGatewayAuditAdapter fails closed on broken chain", async () => {
  await withServer((req,res) => {
    res.setHeader("content-type","application/json");
    if (req.url === "/v1/audit/verify") return res.end(JSON.stringify({ok:false,reason:"hash_mismatch"}));
    res.end(JSON.stringify({entries:[]}));
  }, async (baseUrl) => {
    const adapter = new TrustGatewayAuditAdapter({baseUrl,token:"test-token"});
    const result = await adapter.verifyGovernance({correlation:{}});
    assert.equal(result.ok,false);
    assert.equal(result.reason,"trust_gateway_audit_chain_invalid");
  });
});

test("HttpVerifierProviderAdapter binds exact digest verdict receipt and correlation", async () => {
  const link = {
    subject_sha256:"c".repeat(64),
    verdict:"pass",
    receipt_ref:"sentinel:receipt:123",
    correlation:{schema:"correlation/1.0",action_id:"act_77777777777777777777777777777777"}
  };
  await withServer(async (req,res) => {
    const chunks=[]; for await (const c of req) chunks.push(c);
    const body=JSON.parse(Buffer.concat(chunks).toString("utf8"));
    assert.deepEqual(body,{link});
    res.setHeader("content-type","application/json");
    res.end(JSON.stringify({
      ok:true,
      subject_sha256:link.subject_sha256,
      verdict:link.verdict,
      receipt_ref:link.receipt_ref,
      correlation:link.correlation
    }));
  }, async (baseUrl) => {
    const adapter = new HttpVerifierProviderAdapter({baseUrl,token:"verifier-token"});
    const result = await adapter.verify(link);
    assert.equal(result.ok,true);
  });
});

test("HttpVerifierProviderAdapter rejects mismatched verifier result", async () => {
  const link = {
    subject_sha256:"d".repeat(64),
    verdict:"pass",
    receipt_ref:"sentinel:receipt:456",
    correlation:{schema:"correlation/1.0",action_id:"act_77777777777777777777777777777777"}
  };
  await withServer((req,res) => {
    res.setHeader("content-type","application/json");
    res.end(JSON.stringify({
      ok:true,
      subject_sha256:"e".repeat(64),
      verdict:"pass",
      receipt_ref:link.receipt_ref,
      correlation:link.correlation
    }));
  }, async (baseUrl) => {
    const adapter = new HttpVerifierProviderAdapter({baseUrl});
    const result = await adapter.verify(link);
    assert.equal(result.ok,false);
    assert.equal(result.reason,"verifier_subject_digest_mismatch");
  });
});
