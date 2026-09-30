export class JsonHttpClient {
  constructor({ baseUrl, token = "", timeoutMs = 5000 } = {}) {
    if (!baseUrl || typeof baseUrl !== "string") throw new Error("baseUrl required");
    this.baseUrl = baseUrl.replace(/\/+$/, "");
    this.token = token;
    this.timeoutMs = timeoutMs;
  }

  async request(path, { method = "GET", body } = {}) {
    const headers = { accept:"application/json" };
    if (this.token) headers.authorization = `Bearer ${this.token}`;
    if (body !== undefined) headers["content-type"] = "application/json";
    const response = await fetch(this.baseUrl + path, {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      signal: AbortSignal.timeout(this.timeoutMs)
    });
    const text = await response.text();
    let payload = {};
    if (text) {
      try { payload = JSON.parse(text); }
      catch { throw new Error(`invalid JSON from ${method} ${path}`); }
    }
    if (!response.ok) {
      const error = new Error(payload.error ?? `HTTP ${response.status} from ${path}`);
      error.status = response.status;
      error.payload = payload;
      throw error;
    }
    return payload;
  }
}
