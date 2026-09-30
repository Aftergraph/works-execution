# Aftergraph domain cutover

Updated 2026-09-30 from Cloudflare's live tunnel and Access configuration and
public hostname probes. The Aftergraph service routes and matching Control
Access destination have been published; the Rendetalje service routes remain
available during the rollback window.

## Service hostname map

| Current service hostname | Canonical Aftergraph hostname | Origin observed in Cloudflare Tunnel | Aftergraph route state |
|---|---|---|---|
| `control.rendetalje.dk` | `control.aftergraph.org` | `http://control:3040` | Published; Access protected |
| `works.rendetalje.dk` | `works.aftergraph.org` | `http://172.17.0.1:18191` | Published; `/healthz` returns 200 |
| `intel.rendetalje.dk` | `intel.aftergraph.org` | `http://172.17.0.1:8090` | Published; `/` returns 401 |
| `work-intelligence.rendetalje.dk` | `work-intelligence.aftergraph.org` | `http://172.17.0.1:3001` | Present |
| — | `studio-api.aftergraph.org` | `http://172.21.0.1:8000` | Present |
| — | `works-mtls.aftergraph.org` | TLS peer name for the private VDS listener at `100.71.253.52:3041` | Private Tailscale path planned; not deployed |

The `renos-control-vds` tunnel has nine published applications after the
cutover additions. Cloudflare created DNS records for all three new hostnames;
each resolves publicly. The three corresponding Rendetalje Tunnel routes are
still present and resolve as before.

The WORKS mTLS listener will bind only to the VDS Tailscale address
`100.71.253.52:3041`. Lenovo initiates the worker connection to that private
address; the worker verifies the server certificate for
`works-mtls.aftergraph.org`, and WORKS verifies the worker's certificate URI
identity and binds it to the bearer worker ID. This preserves end-to-end TLS
and exposes no inbound worker port or new public TCP route. The public
`works.aftergraph.org` route remains for the GitHub webhook and operator/API
surfaces. The listener, certificates, and physical worker connection are not
deployed yet. This transport does not by itself provide hardware attestation,
durable relay failover, or reconnect/resume proof.

## Access and mail boundaries observed

The Cloudflare Access application `RenOS Control — VDS` protects
`control.rendetalje.dk` and `control.aftergraph.org` with the same reusable
`Allow Rendetalje staff` policy, which permits the existing `@rendetalje.dk`
identity. Do not change the allowed identity or remove the existing policy
until an Aftergraph-domain login identity is confirmed and tested.

The observed `rendetalje.dk` zone has 20 DNS records: the Rendetalje Pages
apex and `www`, an `app` A/AAAA pair, Google Workspace MX records, an Amazon
SES MX record, and mail verification, SPF, DKIM, and DMARC records. The
`aftergraph.org` zone currently has no MX records. Preserve those mail and
tenant records; only the four product-service Tunnel hostnames listed above
are in the legacy cutover scope.

## Safe cutover order

1. Bind the mTLS listener to VDS Tailscale address `100.71.253.52:3041` only.
   Provision the VDS server certificate and per-worker client identities in
   protected machine-local stores, restart the API, and prove that the listener
   rejects clients without a trusted certificate. Rotation and revocation
   must be defined before this becomes a long-lived production credential.
2. **Complete.** Add `control.aftergraph.org` to the existing control Access
   application with its current policy and publish the matching Tunnel route
   to `http://control:3040`.
3. **Complete.** Publish `works.aftergraph.org` and `intel.aftergraph.org` to
   their observed origins. `work-intelligence.aftergraph.org` remains in
   place. These hostnames preserve current origin behavior; separately verify
   each application's own authentication before use.
4. Configure both the VDS worker and Lenovo worker to use the private mTLS
   listener. The Lenovo client URL uses the VDS Tailscale IP, while
   `WORKS_MTLS_SERVER_NAME=works-mtls.aftergraph.org` verifies the Aftergraph
   certificate identity. Do not create a public TCP route or Cloudflare Access
   service token for this worker channel.
 5. **Partially complete (2026-09-30).** Public probes returned Control 302,
    WORKS `/healthz` 200, Intel `/` 401, and Work Intelligence `/` 200 for the
    Aftergraph hostnames. The Rendetalje service hostnames returned the same
    status codes and remain available for rollback. GitHub hook `672708250` now
    targets `https://works.aftergraph.org/v1/webhook/github` with its existing
    active state, `pull_request,push` events, JSON content type, and TLS
    verification enabled. A new signed GitHub `ping` delivery returned HTTP
    200 (delivery `3845697366459941000`, 2026-09-30T18:19:46Z). The previous
    HTTP 401 was resolved by restoring the active WORKS service secret into the
    complete GitHub hook configuration without displaying or duplicating the
    secret. Authenticated Control login, mTLS enrollment, and a WORKS worker
    lease under the Aftergraph hostnames are still unverified.
6. Keep old product-service routes and DNS records for the rollback window.
   After all consumers use Aftergraph names and the owner accepts the
   cutover, remove only the four old product-service Tunnel routes and
   matching DNS records.

## Preserve unrelated Rendetalje zone records

Only the product-service subdomains in the map are in scope. Do not replace
the zone or remove its apex, `www`, `app`, Google Workspace, SES, or other
unrelated records. The observed `rendetalje.dk` zone contains a Pages site,
another application host, and mail records.

The existing control Access policy allows addresses ending in
`@rendetalje.dk`. Keep it while adding the new hostname; do not guess an
`@aftergraph.org` identity or remove the existing allow rule until a working
Aftergraph-domain login identity is confirmed.

## Current external status

The Cloudflare dashboard is available in the authenticated browser. SentinelX
currently has no connected hosts and no saved Cloudflare API integration, so
Cloudflare changes are managed through the dashboard. The `renos-control-vds`
tunnel is Healthy and serves the published Aftergraph routes. The
`rendetalje.dk` zone still has 20 DNS records, including the Pages site, app
host, Google Workspace, SES, SPF, DKIM, DMARC, and verification records.
Preserve those unrelated site, app, mail, and tenant records; only the four
product-service Tunnel hostnames listed above are in scope. GitHub webhook
`672708250` now uses `works.aftergraph.org`; its signed ping returned HTTP 200.

The WORKS API currently runs at VDS commit
`7ea30cfe8550b280d96b25191774de15d9904e64` on port 18191. The candidate mTLS
listener/client code is not deployed, and no worker certificate has been
provisioned. `works-mtls.aftergraph.org` is a TLS peer name only; it does not
need a public DNS/Tunnel route for the planned Tailscale transport. The active
WORKS webhook secret remains in the VDS service environment; do not copy it
into another `.env`, repository file, or artifact. Authenticated Control login
and a WORKS worker lease remain unverified.

The VDS `works-worker` systemd unit is active, but its process defaults to
`http://127.0.0.1:8080`, which returns no HTTP response; the active API answers
on port 18191. `WORKS_WORKER_ID` is unset, so the worker generates a temporary
`wrkr_local_*` identity at each start. The worker is in pool `avc-core`, and
all four `WORKS_MTLS_*` client settings are absent. Port 3041 has no listener.
An active systemd process therefore does not prove a healthy worker, stable
identity, enrollment, heartbeat, or lease claim. The next deployment must use
the correct API endpoint, a stable worker identity, and the dedicated mTLS
listener.
