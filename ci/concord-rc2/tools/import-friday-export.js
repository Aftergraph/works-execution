#!/usr/bin/env node
import { readFile, writeFile } from "node:fs/promises";
import { join } from "node:path";
import { createHash } from "node:crypto";

const root = process.argv[2];
const output = process.argv[3] ?? "friday-bootstrap-assertions.json";
if (!root) {
  console.error("usage: node tools/import-friday-export.js <export-directory> [output.json]");
  process.exit(2);
}

const tenantId = process.env.CONCORD_TENANT_ID;
const consentRecord = process.env.CONCORD_CONSENT_RECORD;
const consentVersion = process.env.CONCORD_CONSENT_VERSION ?? "1";
if (!/^ten_[a-f0-9]{32}$/.test(tenantId ?? "")) throw new Error("CONCORD_TENANT_ID must be ten_<32 hex>");
if (!consentRecord) throw new Error("CONCORD_CONSENT_RECORD is required");

const generatedAt = new Date().toISOString();
const docs = [
  ["profile", "profile_and_preferences.json"],
  ["durable-knowledge", "durable_knowledge.json"],
  ["project-checkpoints", "project_checkpoints.json"],
  ["open-items", "open_items.json"]
];

function stable(value) {
  if (Array.isArray(value)) return `[${value.map(stable).join(",")}]`;
  if (value && typeof value === "object") return `{${Object.keys(value).sort().map((k) => JSON.stringify(k)+":"+stable(value[k])).join(",")}}`;
  return JSON.stringify(value);
}
function hash(value) { return createHash("sha256").update(stable(value)).digest("hex"); }
function assertionId(seed) { return "ast_" + hash(seed).slice(0,32); }

const assertions = [];
for (const [domain, filename] of docs) {
  const value = JSON.parse(await readFile(join(root, filename), "utf8"));
  const observedAt = value.generated_at ?? generatedAt;
  const contentHash = hash(value);
  assertions.push({
    schema:"world-assertion/0.1",
    assertion_id:assertionId({domain,contentHash}),
    subject:"friday-sync-export",
    predicate:`snapshot.${domain}`,
    value_or_ref:`sha256:${contentHash}`,
    epistemic:"observed",
    currentness:"current",
    source_refs:[`file:${filename}`],
    evidence_refs:[`sha256:${contentHash}`],
    observed_at:observedAt,
    evidence_observed_at:observedAt,
    asserted_at:generatedAt,
    valid_until:"2099-12-31T23:59:59Z",
    tenant_id:tenantId,
    domain:"agent-continuity",
    classification:"private",
    consent_record:consentRecord,
    consent_version:consentVersion,
    purpose:"friday-fo-context-bootstrap",
    confidence:1,
    evidence_requirement:"none"
  });
}

await writeFile(output, JSON.stringify({ schema:"concord-bootstrap/1", generated_at:generatedAt, assertions }, null, 2) + "\n");
console.log(JSON.stringify({ output, assertions:assertions.length, excluded:["sensitive_context.json","operations_context.json","recent_conversation_index.jsonl"] }, null, 2));
