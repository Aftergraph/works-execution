package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"sort"
)

var (
	ErrEconomicTrustRootGenerationRegression = errors.New("economic trust root generation regression")
	ErrEconomicTrustRootEquivocation         = errors.New("economic trust root equivocation")
	ErrEconomicTrustRootGenerationGap        = errors.New("economic trust root generation gap")
	ErrEconomicTrustRootFloorRegression      = errors.New("economic trust root revocation floor regression")
	ErrEconomicTrustRootRevivalForbidden     = errors.New("economic trust root revival forbidden")
	ErrEconomicTrustRootConcurrentAdvance    = errors.New("economic trust root concurrent advance")
	ErrEconomicTrustRootAuthorizationRequired = errors.New("economic trust root rotation authorization required")
	ErrEconomicTrustRootAuthorizationInvalid  = errors.New("economic trust root rotation authorization invalid")
)

const economicSourceTrustRootSchema =
	"CREATE TABLE IF NOT EXISTS economic_source_trust_roots (" +
	"source_class TEXT NOT NULL," +
	"source_id TEXT NOT NULL," +
	"trust_root_id TEXT NOT NULL," +
	"public_key_fingerprint TEXT NOT NULL," +
	"generation INTEGER NOT NULL CHECK(generation > 0)," +
	"min_attestation_generation INTEGER NOT NULL CHECK(min_attestation_generation > 0)," +
	"status TEXT NOT NULL CHECK(status IN ('ACTIVE','REVOKED'))," +
	"valid_from_unix INTEGER NOT NULL CHECK(valid_from_unix > 0)," +
	"valid_until_unix INTEGER NOT NULL CHECK(valid_until_unix > valid_from_unix)," +
	"revision INTEGER NOT NULL CHECK(revision > 0)," +
	"state_digest TEXT NOT NULL," +
	"updated_at TEXT NOT NULL," +
	"PRIMARY KEY(source_class, source_id)," +
	"CHECK(source_class IN ('registry','custody','representation'))" +
	");"

type EconomicSourceTrustRootState struct {
	SourceClass              string
	SourceID                 string
	TrustRootID              string
	PublicKeyFingerprint     string
	Generation               int64
	MinAttestationGeneration int64
	Status                   string
	ValidFromUnix            int64
	ValidUntilUnix           int64
	Revision                 int64
	StateDigest              string
}


type EconomicTrustRootRotationAuthorizationPayload struct {
	SourceClass                   string
	SourceID                      string
	PriorTrustRootID              string
	NewTrustRootID                string
	PriorPublicKeyFingerprint     string
	NewPublicKeyFingerprint       string
	PriorGeneration               int64
	NewGeneration                 int64
	PriorMinAttestationGeneration int64
	NewMinAttestationGeneration   int64
	AuthorityLeaseID              string
	ApprovalProofIDs              []string
	RotationNonce                 string
	Reason                        string
	ExpiresAtMillis               int64
}

type EconomicTrustRootRotationAuthorization struct {
	Schema                      string
	Decision                    string
	Authorized                  bool
	TrustRootMutationAuthorized bool
	AuthorizationDigest         string
	Payload                     EconomicTrustRootRotationAuthorizationPayload
	ApprovalsVerified           bool
	AuthorityVerified           bool
	ExecutionAuthority          bool
	LiveValueEnabled            bool
	Final                       bool
	PromotionAuthority          bool
	ExternalEffects             int
}

func economicTrustRootAuthorizationDigest(p EconomicTrustRootRotationAuthorizationPayload) string {
	approvals := append([]string(nil), p.ApprovalProofIDs...)
	sort.Strings(approvals)
	parts := []string{
		"aftergraph/economic-trust-root-rotation/v1",
		"GOVERNED_TRUST_ROOT_ROTATION",
		p.SourceClass, p.SourceID,
		p.PriorTrustRootID, p.NewTrustRootID,
		p.PriorPublicKeyFingerprint, p.NewPublicKeyFingerprint,
		strconv.FormatInt(p.PriorGeneration, 10), strconv.FormatInt(p.NewGeneration, 10),
		strconv.FormatInt(p.PriorMinAttestationGeneration, 10), strconv.FormatInt(p.NewMinAttestationGeneration, 10),
		p.AuthorityLeaseID, strings.Join(approvals, ","),
		p.RotationNonce, p.Reason, strconv.FormatInt(p.ExpiresAtMillis, 10),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func validateEconomicTrustRootRotationAuthorization(
	current EconomicSourceTrustRootState,
	next EconomicSourceTrustRootState,
	auth *EconomicTrustRootRotationAuthorization,
) error {
	if auth == nil {
		return ErrEconomicTrustRootAuthorizationRequired
	}
	if auth.Schema != "aftergraph.economic-trust-root-rotation-authorization/v1" ||
		auth.Decision != "AUTHORIZED_PREPARE_ONLY" ||
		!auth.Authorized || !auth.TrustRootMutationAuthorized ||
		!auth.ApprovalsVerified || !auth.AuthorityVerified ||
		auth.ExecutionAuthority || auth.LiveValueEnabled || auth.Final ||
		auth.PromotionAuthority || auth.ExternalEffects != 0 {
		return ErrEconomicTrustRootAuthorizationInvalid
	}
	p := auth.Payload
	if p.SourceClass != current.SourceClass || p.SourceID != current.SourceID ||
		p.PriorTrustRootID != current.TrustRootID || p.NewTrustRootID != next.TrustRootID ||
		p.PriorPublicKeyFingerprint != current.PublicKeyFingerprint ||
		p.NewPublicKeyFingerprint != next.PublicKeyFingerprint ||
		p.PriorGeneration != current.Generation || p.NewGeneration != next.Generation ||
		p.PriorMinAttestationGeneration != current.MinAttestationGeneration ||
		p.NewMinAttestationGeneration != next.MinAttestationGeneration {
		return ErrEconomicTrustRootAuthorizationInvalid
	}
	if len(p.ApprovalProofIDs) < 2 || strings.TrimSpace(p.AuthorityLeaseID) == "" ||
		strings.TrimSpace(p.RotationNonce) == "" || p.ExpiresAtMillis <= time.Now().UnixMilli() {
		return ErrEconomicTrustRootAuthorizationInvalid
	}
	seen := map[string]struct{}{}
	for _, id := range p.ApprovalProofIDs {
		if strings.TrimSpace(id) == "" {
			return ErrEconomicTrustRootAuthorizationInvalid
		}
		if _, ok := seen[id]; ok {
			return ErrEconomicTrustRootAuthorizationInvalid
		}
		seen[id] = struct{}{}
	}
	if auth.AuthorizationDigest != economicTrustRootAuthorizationDigest(p) {
		return ErrEconomicTrustRootAuthorizationInvalid
	}
	return nil
}

type EconomicSourceTrustRootTransition struct {
	Schema          string
	Decision        string
	PriorGeneration int64
	Current         EconomicSourceTrustRootState
	ExternalEffects int
}

func (s *SQLiteStore) migrateEconomicSourceTrustRoots() error {
	_, err := s.db.Exec(economicSourceTrustRootSchema)
	return err
}

func validateEconomicSourceTrustRoot(v EconomicSourceTrustRootState) error {
	switch v.SourceClass {
	case "registry", "custody", "representation":
	default:
		return errors.New("economic trust root source class invalid")
	}
	if strings.TrimSpace(v.SourceID) == "" || strings.TrimSpace(v.TrustRootID) == "" {
		return errors.New("economic trust root identity required")
	}
	if !isTrustHash(v.PublicKeyFingerprint) {
		return errors.New("economic trust root fingerprint invalid")
	}
	if v.Generation <= 0 || v.MinAttestationGeneration <= 0 {
		return errors.New("economic trust root generation invalid")
	}
	switch v.Status {
	case "ACTIVE", "REVOKED":
	default:
		return errors.New("economic trust root status invalid")
	}
	if v.ValidFromUnix <= 0 || v.ValidUntilUnix <= v.ValidFromUnix {
		return errors.New("economic trust root validity invalid")
	}
	return nil
}

func isTrustHash(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(value[len("sha256:"):])
	return err == nil
}

func economicTrustRootDigest(v EconomicSourceTrustRootState) string {
	parts := []string{
		v.SourceClass,
		v.SourceID,
		v.TrustRootID,
		v.PublicKeyFingerprint,
		strconv.FormatInt(v.Generation, 10),
		strconv.FormatInt(v.MinAttestationGeneration, 10),
		v.Status,
		strconv.FormatInt(v.ValidFromUnix, 10),
		strconv.FormatInt(v.ValidUntilUnix, 10),
		strconv.FormatInt(v.Revision, 10),
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func exactTrustRootReplay(a, b EconomicSourceTrustRootState) bool {
	return a.SourceClass == b.SourceClass &&
		a.SourceID == b.SourceID &&
		a.TrustRootID == b.TrustRootID &&
		a.PublicKeyFingerprint == b.PublicKeyFingerprint &&
		a.Generation == b.Generation &&
		a.MinAttestationGeneration == b.MinAttestationGeneration &&
		a.Status == b.Status &&
		a.ValidFromUnix == b.ValidFromUnix &&
		a.ValidUntilUnix == b.ValidUntilUnix
}

func (s *SQLiteStore) AdvanceEconomicSourceTrustRoot(
	ctx context.Context,
	next EconomicSourceTrustRootState,
) (*EconomicSourceTrustRootTransition, error) {
	return s.advanceEconomicSourceTrustRoot(ctx, next, nil)
}

func (s *SQLiteStore) AdvanceEconomicSourceTrustRootAuthorized(
	ctx context.Context,
	next EconomicSourceTrustRootState,
	auth EconomicTrustRootRotationAuthorization,
) (*EconomicSourceTrustRootTransition, error) {
	return s.advanceEconomicSourceTrustRoot(ctx, next, &auth)
}

func (s *SQLiteStore) advanceEconomicSourceTrustRoot(
	ctx context.Context,
	next EconomicSourceTrustRootState,
	auth *EconomicTrustRootRotationAuthorization,
) (*EconomicSourceTrustRootTransition, error) {
	if err := validateEconomicSourceTrustRoot(next); err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("economic trust root begin: %w", err)
	}
	defer tx.Rollback()

	current, err := loadEconomicSourceTrustRootTx(ctx, tx, next.SourceClass, next.SourceID)
	if err != nil {
		return nil, err
	}

	if current == nil {
		next.Revision = 1
		next.StateDigest = economicTrustRootDigest(next)
		_, err := tx.ExecContext(ctx,
			"INSERT INTO economic_source_trust_roots "+
				"(source_class, source_id, trust_root_id, public_key_fingerprint, generation, "+
				"min_attestation_generation, status, valid_from_unix, valid_until_unix, revision, state_digest, updated_at) "+
				"VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)",
			next.SourceClass, next.SourceID, next.TrustRootID, next.PublicKeyFingerprint,
			next.Generation, next.MinAttestationGeneration, next.Status,
			next.ValidFromUnix, next.ValidUntilUnix, next.Revision, next.StateDigest,
			time.Now().UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return nil, fmt.Errorf("economic trust root insert: %w", err)
		}
		if _, err := appendEconomicTrustRootEventTx(ctx, tx, next, "ADVANCED", ""); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("economic trust root commit: %w", err)
		}
		return &EconomicSourceTrustRootTransition{
			Schema: "aftergraph.economic-source-trust-root-transition/v1",
			Decision: "ADVANCED",
			PriorGeneration: 0,
			Current: next,
			ExternalEffects: 0,
		}, nil
	}

	if next.Generation < current.Generation {
		return nil, ErrEconomicTrustRootGenerationRegression
	}
	if next.Generation == current.Generation {
		if !exactTrustRootReplay(*current, next) {
			return nil, ErrEconomicTrustRootEquivocation
		}
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("economic trust root replay commit: %w", err)
		}
		return &EconomicSourceTrustRootTransition{
			Schema: "aftergraph.economic-source-trust-root-transition/v1",
			Decision: "IDEMPOTENT_REPLAY",
			PriorGeneration: current.Generation,
			Current: *current,
			ExternalEffects: 0,
		}, nil
	}
	if next.Generation != current.Generation+1 {
		return nil, ErrEconomicTrustRootGenerationGap
	}
	if next.MinAttestationGeneration < current.MinAttestationGeneration {
		return nil, ErrEconomicTrustRootFloorRegression
	}
	if next.ValidFromUnix < current.ValidFromUnix {
		return nil, errors.New("economic trust root validity regression")
	}
	sameKey := next.PublicKeyFingerprint == current.PublicKeyFingerprint &&
		next.TrustRootID == current.TrustRootID
	if current.Status == "REVOKED" && sameKey && next.Status == "ACTIVE" {
		return nil, ErrEconomicTrustRootRevivalForbidden
	}

	decision := "ADVANCED_POLICY"
	if !sameKey {
		if err := validateEconomicTrustRootRotationAuthorization(*current, next, auth); err != nil {
			return nil, err
		}
		decision = "ROTATED"
	}

	next.Revision = current.Revision + 1
	next.StateDigest = economicTrustRootDigest(next)
	res, err := tx.ExecContext(ctx,
		"UPDATE economic_source_trust_roots SET trust_root_id=?, public_key_fingerprint=?, generation=?, "+
			"min_attestation_generation=?, status=?, valid_from_unix=?, valid_until_unix=?, revision=?, state_digest=?, updated_at=? "+
			"WHERE source_class=? AND source_id=? AND generation=? AND revision=?",
		next.TrustRootID, next.PublicKeyFingerprint, next.Generation,
		next.MinAttestationGeneration, next.Status, next.ValidFromUnix, next.ValidUntilUnix,
		next.Revision, next.StateDigest, time.Now().UTC().Format(time.RFC3339Nano),
		next.SourceClass, next.SourceID, current.Generation, current.Revision,
	)
	if err != nil {
		return nil, fmt.Errorf("economic trust root update: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if rows != 1 {
		return nil, ErrEconomicTrustRootConcurrentAdvance
	}
	authorizationDigest := ""
	if decision == "ROTATED" && auth != nil {
		authorizationDigest = auth.AuthorizationDigest
	}
	if _, err := appendEconomicTrustRootEventTx(ctx, tx, next, decision, authorizationDigest); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("economic trust root commit advance: %w", err)
	}
	return &EconomicSourceTrustRootTransition{
		Schema: "aftergraph.economic-source-trust-root-transition/v1",
		Decision: decision,
		PriorGeneration: current.Generation,
		Current: next,
		ExternalEffects: 0,
	}, nil
}

func (s *SQLiteStore) GetEconomicSourceTrustRoot(
	ctx context.Context,
	sourceClass, sourceID string,
) (*EconomicSourceTrustRootState, error) {
	row := s.readQueryRow(ctx,
		"SELECT source_class, source_id, trust_root_id, public_key_fingerprint, generation, "+
			"min_attestation_generation, status, valid_from_unix, valid_until_unix, revision, state_digest "+
			"FROM economic_source_trust_roots WHERE source_class=? AND source_id=?",
		sourceClass, sourceID,
	)
	return scanEconomicSourceTrustRoot(row)
}

func loadEconomicSourceTrustRootTx(
	ctx context.Context,
	tx *sql.Tx,
	sourceClass, sourceID string,
) (*EconomicSourceTrustRootState, error) {
	row := tx.QueryRowContext(ctx,
		"SELECT source_class, source_id, trust_root_id, public_key_fingerprint, generation, "+
			"min_attestation_generation, status, valid_from_unix, valid_until_unix, revision, state_digest "+
			"FROM economic_source_trust_roots WHERE source_class=? AND source_id=?",
		sourceClass, sourceID,
	)
	return scanEconomicSourceTrustRoot(row)
}

func scanEconomicSourceTrustRoot(row economicSourceGenerationScanner) (*EconomicSourceTrustRootState, error) {
	var out EconomicSourceTrustRootState
	err := row.Scan(
		&out.SourceClass, &out.SourceID, &out.TrustRootID, &out.PublicKeyFingerprint,
		&out.Generation, &out.MinAttestationGeneration, &out.Status,
		&out.ValidFromUnix, &out.ValidUntilUnix, &out.Revision, &out.StateDigest,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if out.StateDigest != economicTrustRootDigest(out) {
		return nil, errors.New("economic trust root state digest mismatch")
	}
	return &out, nil
}
