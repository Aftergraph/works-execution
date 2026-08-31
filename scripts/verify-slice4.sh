#!/usr/bin/env bash
# Slice-4 verification gate. Run after subagents return.
set -euo pipefail
cd /root/works-venture

export PATH=$PATH:/usr/local/go/bin

echo "=== 1. vet ==="
go vet ./... && echo "vet: clean"

echo
echo "=== 2. unit tests ==="
go test ./... -count=1 2>&1 | tail -20

echo
echo "=== 3. e2e regression ==="
make build 2>&1 | tail -3
go test -tags=e2e ./e2e/ -count=1 2>&1 | tail -5

echo
echo "=== 4. standards-validate ==="
make standards-validate 2>&1 | tail -10

echo
echo "=== 5. kanban-validate ==="
make kanban-validate 2>&1 | tail -5

echo
echo "=== 6. sbom ==="
make sbom 2>&1 | tail -5
ls -la artifacts/sbom/ 2>&1 | head -5

echo
echo "=== 7. smoke: hit new endpoints ==="
# Start API in background; hit each new endpoint; assert no 5xx.
rm -f /tmp/slice4.db /tmp/slice4.db-*
mkdir -p /tmp/slice4-artifacts
./bin/works-api -addr 127.0.0.1:18099 -db /tmp/slice4.db > /tmp/slice4-api.log 2>&1 &
API_PID=$!
sleep 1

echo "  POST /v1/runners/register:"
curl -sS -w '\n    HTTP %{http_code}\n' -X POST -H 'Content-Type: application/json' \
  -d '{"trust_class":"standard","capabilities":{"os":["linux"],"arch":["amd64"],"cpu_milli":2000,"memory_mib":4096}}' \
  http://127.0.0.1:18099/v1/runners/register 2>&1 | head -5

echo "  GET /metrics:"
curl -sS -w '\n    HTTP %{http_code}\n' http://127.0.0.1:18099/metrics 2>&1 | head -10

echo "  POST /v1/workers/enroll:"
curl -sS -w '\n    HTTP %{http_code}\n' -X POST -H 'Content-Type: application/json' \
  -d '{"runner_id":"wrkr_smoke","trust_class":"standard"}' \
  http://127.0.0.1:18099/v1/workers/enroll 2>&1 | head -5

kill $API_PID 2>/dev/null
wait 2>/dev/null

echo
echo "=== 8. summary ==="
./bin/works-standards summary | head -10
echo
./bin/works-kanban summary | head -40