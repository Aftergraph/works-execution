const $ = (id) => document.getElementById(id);
let state = null;
let authToken = sessionStorage.getItem("concord_token") ?? "";

function updateAuthLabel() {
  $("auth").textContent = authToken ? "AUTH SET" : "AUTH";
}

async function apiFetch(path, options = {}, retried = false) {
  const headers = new Headers(options.headers ?? {});
  if (authToken) headers.set("authorization", `Bearer ${authToken}`);
  const response = await fetch(path, { ...options, headers, cache:"no-store" });
  if (response.status === 401 && !retried) {
    const next = window.prompt("Concord bearer token");
    if (next !== null) {
      authToken = next.trim();
      if (authToken) sessionStorage.setItem("concord_token", authToken);
      else sessionStorage.removeItem("concord_token");
      updateAuthLabel();
      return apiFetch(path, options, true);
    }
  }
  return response;
}

$("auth").addEventListener("click", () => {
  const next = window.prompt("Concord bearer token", authToken);
  if (next === null) return;
  authToken = next.trim();
  if (authToken) sessionStorage.setItem("concord_token", authToken);
  else sessionStorage.removeItem("concord_token");
  updateAuthLabel();
  refresh();
});
updateAuthLabel();

function esc(value) {
  return String(value ?? "").replace(/[&<>"']/g, (c) => ({"&":"&amp;","<":"&lt;",">":"&gt;","\"":"&quot;","'":"&#39;"}[c]));
}

function render(snapshot) {
  state = snapshot;
  const holds = snapshot.conflicts.filter((c) => c.state === "HOLD");
  const links = snapshot.verification_links ?? [];
  const verificationBySubject = new Map(links.map((link) => [link.subject_id, link]));
  $("revision").textContent = snapshot.revision;
  $("replicaCount").textContent = snapshot.replicas.length;
  $("assertionCount").textContent = snapshot.assertions.length;
  $("holdCount").textContent = holds.length;
  $("health").className = `pill ${holds.length ? "warn" : "good"}`;
  $("health").textContent = holds.length ? "DEGRADED / HOLD" : "HEALTHY";

  $("replicas").innerHTML = snapshot.replicas.length ? snapshot.replicas.map((r) =>
    `<div class="replica"><b>${esc(r.name)}</b><small>${esc(r.kind)} · ${esc(r.replica_id)}</small><br><small>rev ${esc(r.last_seen_revision)}</small></div>`
  ).join("") : "No replicas registered.";

  $("claims").innerHTML = snapshot.assertions.length ? `<table><thead><tr><th>SUBJECT</th><th>PREDICATE</th><th>VALUE</th><th>STATE</th><th>ORIGIN</th></tr></thead><tbody>${snapshot.assertions.slice(-80).reverse().map((a) =>
    `<tr><td>${esc(a.subject)}</td><td class="mono">${esc(a.predicate)}</td><td>${esc(a.value_or_ref)}</td><td class="state">${esc(a.currentness).toUpperCase()}${verificationBySubject.get(a.assertion_id) ? " · " + esc(verificationBySubject.get(a.assertion_id).verdict).toUpperCase() : ""}</td><td class="mono">${esc(a._origin_replica_id)}</td></tr>`
  ).join("")}</tbody></table>` : '<div class="empty">No assertions yet.</div>';

  $("conflicts").innerHTML = holds.length ? holds.map((c) =>
    `<div class="conflict"><b>HOLD</b><div class="mono">${esc(c.conflict_id)}</div><small>${esc(c.reason)}</small></div>`
  ).join("") : '<div class="empty">No unresolved conflicts.</div>';

  $("integrity").innerHTML = `
    <dt>Snapshot</dt><dd>concord-snapshot/1</dd>
    <dt>Digest hint</dt><dd>${esc(snapshot.merkle_hint_sha256.slice(0,12))}…</dd>
    <dt>Receipts</dt><dd>${snapshot.receipts.length}</dd>
    <dt>Verifier links</dt><dd>${links.length}</dd>
    <dt>Governed receipts</dt><dd>${snapshot.receipts.filter((r) => r.governance_binding_sha256).length}</dd>
    <dt>Conflict policy</dt><dd>HOLD</dd>`;

  $("verification").innerHTML = links.length ? links.slice(-30).reverse().map((link) =>
    `<div class="verificationRow"><span class="pill ${link.verdict === "pass" ? "good" : link.verdict === "fail" ? "bad" : "warn"}">${esc(link.verdict).toUpperCase()}</span><div><b>${esc(link.subject_type)} · ${esc(link.subject_id)}</b><small>${esc(link.verifier_ref)} · ${esc(link.receipt_ref)}</small></div></div>`
  ).join("") : '<div class="empty">No independent verification links yet.</div>';
}

async function refresh() {
  try {
    const res = await apiFetch("/v1/state");
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    render(await res.json());
  } catch (error) {
    $("health").className = "pill bad";
    $("health").textContent = "OFFLINE";
  }
}

function appendEvent(event) {
  const node = document.createElement("div");
  node.className = "event";
  node.innerHTML = `<code>r${esc(event.revision)}</code><b>${esc(event.type)}</b><time>${esc(new Date(event.occurred_at).toLocaleTimeString())}</time>`;
  $("events").prepend(node);
  while ($("events").children.length > 60) $("events").lastElementChild.remove();
}

const knownEvents = new Set(["replica.registered","assertion.ingested","sync.receipted","conflict.resolved","verification.linked","checkpoint.acknowledged"]);

function handleSseFrame(frame) {
  let eventType = "message";
  const dataLines = [];
  for (const line of frame.split("\n")) {
    if (line.startsWith("event:")) eventType = line.slice(6).trim();
    else if (line.startsWith("data:")) dataLines.push(line.slice(5).trim());
  }
  if (!knownEvents.has(eventType) || dataLines.length === 0) return;
  try {
    appendEvent(JSON.parse(dataLines.join("\n")));
    refresh();
  } catch {}
}

async function connect() {
  for (;;) {
    try {
      const response = await apiFetch("/v1/events");
      if (!response.ok || !response.body) throw new Error(`HTTP ${response.status}`);
      $("streamState").textContent = "SSE LIVE";
      const reader = response.body.getReader();
      const decoder = new TextDecoder();
      let buffer = "";
      for (;;) {
        const {value, done} = await reader.read();
        if (done) break;
        buffer += decoder.decode(value, {stream:true}).replace(/\r\n/g, "\n");
        let boundary;
        while ((boundary = buffer.indexOf("\n\n")) !== -1) {
          const frame = buffer.slice(0, boundary);
          buffer = buffer.slice(boundary + 2);
          handleSseFrame(frame);
        }
      }
    } catch {
      $("streamState").textContent = "SSE RETRY";
      await new Promise((resolve) => setTimeout(resolve, 1500));
    }
  }
}

refresh();
connect();
