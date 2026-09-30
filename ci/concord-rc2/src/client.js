export class ConcordClient {
  constructor(baseUrl, token = "") {
    this.baseUrl = baseUrl.replace(/\/$/, "");
    this.token = token;
  }

  async request(path, { method = "GET", body } = {}) {
    const headers = { accept: "application/json" };
    if (body !== undefined) headers["content-type"] = "application/json";
    if (this.token) headers.authorization = `Bearer ${this.token}`;
    const response = await fetch(`${this.baseUrl}${path}`, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body)
    });
    const payload = await response.json();
    if (!response.ok && response.status !== 207) {
      const error = new Error(payload.error ?? payload.errors?.join(", ") ?? `HTTP ${response.status}`);
      error.status = response.status;
      error.payload = payload;
      throw error;
    }
    return payload;
  }

  health() { return this.request("/health"); }
  ready() { return this.request("/ready"); }
  metrics() { return this.request("/metrics"); }
  state() { return this.request("/v1/state"); }
  exportSnapshot() { return this.request("/v1/export"); }
  registerReplica(replica) { return this.request("/v1/replicas", { method:"POST", body:replica }); }
  push(payload) { return this.request("/v1/sync/push", { method:"POST", body:payload }); }
  pull(replicaId, since = 0) {
    return this.request(`/v1/sync/pull?replica_id=${encodeURIComponent(replicaId)}&since=${Number(since)}`);
  }
  acknowledge(checkpoint) {
    return this.request("/v1/sync/ack", { method:"POST", body:checkpoint });
  }
  addVerificationLink(link, signature = null) {
    return this.request("/v1/verification-links", {
      method:"POST",
      body: signature ? { link, signature } : link
    });
  }
  rotateReplicaKey(replicaId, payload) {
    return this.request(`/v1/replicas/${encodeURIComponent(replicaId)}/key-rotation`, {
      method:"POST",
      body:payload
    });
  }
  resolve(conflictId, payload) {
    return this.request(`/v1/conflicts/${encodeURIComponent(conflictId)}/resolve`, { method:"POST", body:payload });
  }
}
