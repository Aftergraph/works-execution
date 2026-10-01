package promotion

import "github.com/JonasAbde/works-execution/packages/secrets"

func mustSecretRef(s string) *secrets.Ref {
	return secrets.Must(s)
}
