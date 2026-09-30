import { JsonHttpClient } from "./http.js";
import { digest } from "../ids.js";

export class HttpVerifierProviderAdapter {
  constructor({ baseUrl, token = "", path = "/v1/verify", timeoutMs = 10000 } = {}) {
    this.http = new JsonHttpClient({ baseUrl, token, timeoutMs });
    this.path = path;
  }

  async verify(link) {
    const result = await this.http.request(this.path, { method:"POST", body:{ link } });
    if (!result || result.ok !== true) return { ok:false, reason:result?.reason ?? "verifier_rejected" };
    if (result.subject_sha256 !== link.subject_sha256) return { ok:false, reason:"verifier_subject_digest_mismatch" };
    if (result.verdict !== link.verdict) return { ok:false, reason:"verifier_verdict_mismatch" };
    if (result.receipt_ref !== link.receipt_ref) return { ok:false, reason:"verifier_receipt_ref_mismatch" };
    if (result.correlation && digest(result.correlation) !== digest(link.correlation)) {
      return { ok:false, reason:"verifier_correlation_mismatch" };
    }
    return { ok:true, evidence:result };
  }
}
