package api

import "os"

// bridgeSecretFromEnv reads WORKS_PLATFORM_BRIDGE_SECRET at route-wiring
// time. The secret must be injected at process start; it is never logged
// and never exposed to clients. Empty value = /resume unavailable (503).
func bridgeSecretFromEnv() string {
	return os.Getenv("WORKS_PLATFORM_BRIDGE_SECRET")
}