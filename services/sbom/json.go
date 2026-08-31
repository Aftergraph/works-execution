package sbom

import "encoding/json"

// jsonDecode wraps encoding/json's Unmarshal so tests can stub it in
// the future (e.g. to inject malformed JSON). It also surfaces a
// consistent error message across callers.
func jsonDecode(b []byte, v any) error {
	return json.Unmarshal(b, v)
}