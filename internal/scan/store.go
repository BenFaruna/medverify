package scan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
	"github.com/benfaruna/medverify/internal/safety"
	"github.com/benfaruna/medverify/internal/verdict"
)

// ErrScanNotFound is returned when a scan ID does not exist.
var ErrScanNotFound = errors.New("scan not found")

// Store persists scans, their images, results, and idempotency records.
type Store struct {
	pool    *pgxpool.Pool
	queries *dbgen.Queries
}

// NewStore builds a store around an existing pool.
func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{pool: pool, queries: dbgen.New(pool)}
}

// CreateInput is one scan creation, including uploaded image metadata.
type CreateInput struct {
	ID             uuid.UUID
	ClientKey      string
	IdempotencyKey string
	RequestHash    string
	Language       string
	LocationCell   string
	Images         []Image
}

// CreateScan inserts the scan, its images, and the idempotency record in one
// transaction. It returns created=false without an error when another request
// won the idempotency race; the caller reads the winner.
func (s *Store) CreateScan(ctx context.Context, input CreateInput) (Scan, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Scan{}, false, fmt.Errorf("begin scan transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := dbgen.New(tx)
	row, err := q.CreateScan(ctx, dbgen.CreateScanParams{
		ID:           input.ID,
		ClientKey:    input.ClientKey,
		Language:     optional(input.Language),
		LocationCell: optional(input.LocationCell),
	})
	if err != nil {
		return Scan{}, false, fmt.Errorf("create scan: %w", err)
	}

	images := make([]Image, 0, len(input.Images))
	for _, image := range input.Images {
		inserted, err := q.InsertScanImage(ctx, dbgen.InsertScanImageParams{
			ScanID:      row.ID,
			Position:    int16(image.Position),
			StorageKey:  image.StorageKey,
			ContentType: image.ContentType,
			ByteSize:    int32(image.ByteSize),
			Width:       int32(image.Width),
			Height:      int32(image.Height),
			Sha256:      image.SHA256,
		})
		if err != nil {
			return Scan{}, false, fmt.Errorf("insert scan image: %w", err)
		}
		images = append(images, imageFromRow(inserted))
	}

	if input.IdempotencyKey != "" {
		_, err := q.InsertIdempotencyKey(ctx, dbgen.InsertIdempotencyKeyParams{
			ClientKey:      input.ClientKey,
			IdempotencyKey: input.IdempotencyKey,
			RequestHash:    input.RequestHash,
			ScanID:         row.ID,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			// Another concurrent request inserted the key first. Roll back this
			// scan entirely and let the caller return the winner.
			return Scan{}, false, nil
		}
		if err != nil {
			return Scan{}, false, fmt.Errorf("record idempotency key: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return Scan{}, false, fmt.Errorf("commit scan: %w", err)
	}

	scan := scanFromRow(row)
	scan.Images = images
	return scan, true, nil
}

// GetScan loads one scan with its images and optional result.
func (s *Store) GetScan(ctx context.Context, id uuid.UUID) (Scan, error) {
	row, err := s.queries.GetScan(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return Scan{}, ErrScanNotFound
	}
	if err != nil {
		return Scan{}, fmt.Errorf("load scan %s: %w", id, err)
	}

	scan := scanFromRow(row)
	imageRows, err := s.queries.ListScanImages(ctx, id)
	if err != nil {
		return Scan{}, fmt.Errorf("load scan images %s: %w", id, err)
	}
	scan.Images = make([]Image, 0, len(imageRows))
	for _, imageRow := range imageRows {
		scan.Images = append(scan.Images, imageFromRow(imageRow))
	}

	resultRow, err := s.queries.GetScanResult(ctx, id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return Scan{}, fmt.Errorf("load scan result %s: %w", id, err)
	default:
		result, err := resultFromRow(resultRow)
		if err != nil {
			return Scan{}, fmt.Errorf("decode scan result %s: %w", id, err)
		}
		scan.Result = &result
	}

	flagRows, err := s.queries.ListSafetyFlagsForScan(ctx, id)
	if err != nil {
		return Scan{}, fmt.Errorf("load safety flags %s: %w", id, err)
	}
	scan.Flags = make([]safety.Flag, 0, len(flagRows))
	for _, flagRow := range flagRows {
		flag := safety.Flag{
			Type:       safety.FlagType(flagRow.FlagType),
			Severity:   safety.Severity(flagRow.Severity),
			ReasonCode: flagRow.ReasonCode,
		}
		if len(flagRow.Evidence) > 0 {
			if err := json.Unmarshal(flagRow.Evidence, &flag.Evidence); err != nil {
				return Scan{}, fmt.Errorf("decode safety flag evidence for scan %s: %w", id, err)
			}
		}
		if len(flagRow.Source) > 0 {
			if err := json.Unmarshal(flagRow.Source, &flag.Source); err != nil {
				return Scan{}, fmt.Errorf("decode safety flag source for scan %s: %w", id, err)
			}
		}
		scan.Flags = append(scan.Flags, flag)
	}
	return scan, nil
}

// ClaimScan moves a queued scan to processing. False means another worker
// already claimed it or it is already terminal.
func (s *Store) ClaimScan(ctx context.Context, id uuid.UUID) (bool, error) {
	rows, err := s.queries.ClaimScan(ctx, id)
	if err != nil {
		return false, fmt.Errorf("claim scan %s: %w", id, err)
	}
	return rows == 1, nil
}

// RequeueFailedScan returns a failed scan to the queue for a River retry.
// False means the row was not failed, so the caller leaves it alone.
func (s *Store) RequeueFailedScan(ctx context.Context, id uuid.UUID) (bool, error) {
	rows, err := s.queries.RequeueFailedScan(ctx, id)
	if err != nil {
		return false, fmt.Errorf("requeue scan %s: %w", id, err)
	}
	return rows == 1, nil
}

// FinishScan writes a terminal status and its result atomically. False means
// the scan was not processing: another worker finished it, so the caller must
// not overwrite the existing result.
func (s *Store) FinishScan(ctx context.Context, id uuid.UUID, status Status, result Result) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin finish transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	q := dbgen.New(tx)
	now := time.Now().UTC()
	rows, err := q.FinishScan(ctx, dbgen.FinishScanParams{
		ID:          id,
		Status:      string(status),
		CompletedAt: &now,
	})
	if err != nil {
		return false, fmt.Errorf("finish scan %s: %w", id, err)
	}
	if rows == 0 {
		return false, tx.Commit(ctx)
	}

	reasons := result.Reasons
	if len(reasons) == 0 {
		reasons = json.RawMessage("[]")
	}
	features := result.Features
	if len(features) == 0 {
		features = json.RawMessage("{}")
	}

	params := dbgen.UpsertScanResultParams{
		ScanID:    id,
		Reasons:   reasons,
		Extracted: result.Extracted,
		Features:  features,
		Provider:  optional(result.Provider),
		Error:     optional(result.Error),
	}
	if result.Verdict != nil {
		if len(result.Signals) == 0 {
			return false, fmt.Errorf("store result for scan %s: verdict has no signals", id)
		}
		encodedReasons, err := json.Marshal(result.Verdict.Reasons)
		if err != nil {
			return false, fmt.Errorf("encode verdict reasons for scan %s: %w", id, err)
		}
		code := string(result.Verdict.Verdict)
		confidence := string(result.Verdict.Confidence)
		version := result.Verdict.RulesVersion
		params.VerdictCode = &code
		params.VerdictConfidence = &confidence
		params.VerdictRulesVersion = &version
		params.VerdictScore = pgtype.Int2{Int16: int16(result.Verdict.Score), Valid: true}
		params.Signals = result.Signals
		params.Reasons = encodedReasons
	}
	if err := q.UpsertScanResult(ctx, params); err != nil {
		return false, fmt.Errorf("store result for scan %s: %w", id, err)
	}

	// Flags are replaced with the result so a retried scan cannot leave stale
	// findings from an earlier attempt.
	if err := q.DeleteSafetyFlagsForScan(ctx, id); err != nil {
		return false, fmt.Errorf("clear safety flags for scan %s: %w", id, err)
	}
	for _, flag := range result.Flags {
		evidence, err := json.Marshal(flag.Evidence)
		if err != nil {
			return false, fmt.Errorf("encode flag evidence for scan %s: %w", id, err)
		}
		source, err := json.Marshal(flag.Source)
		if err != nil {
			return false, fmt.Errorf("encode flag source for scan %s: %w", id, err)
		}
		entryID := pgtype.Int8{}
		if flag.Source.WatchlistEntryID > 0 {
			entryID = pgtype.Int8{Int64: flag.Source.WatchlistEntryID, Valid: true}
		}
		if err := q.InsertSafetyFlag(ctx, dbgen.InsertSafetyFlagParams{
			ScanID:           id,
			FlagType:         string(flag.Type),
			Severity:         string(flag.Severity),
			ReasonCode:       flag.ReasonCode,
			Evidence:         evidence,
			Source:           source,
			WatchlistEntryID: entryID,
		}); err != nil {
			return false, fmt.Errorf("store safety flag for scan %s: %w", id, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit result for scan %s: %w", id, err)
	}
	return true, nil
}

// IdempotencyRecord is the stored mapping from a request key to a scan.
type IdempotencyRecord struct {
	RequestHash string
	ScanID      uuid.UUID
}

// GetIdempotency returns a stored key or found=false.
func (s *Store) GetIdempotency(ctx context.Context, clientKey, key string) (IdempotencyRecord, bool, error) {
	row, err := s.queries.GetIdempotencyKey(ctx, dbgen.GetIdempotencyKeyParams{
		ClientKey:      clientKey,
		IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return IdempotencyRecord{}, false, fmt.Errorf("load idempotency key: %w", err)
	}
	return IdempotencyRecord{RequestHash: row.RequestHash, ScanID: row.ScanID}, true, nil
}

func scanFromRow(row dbgen.Scan) Scan {
	scan := Scan{
		ID:          row.ID,
		ClientKey:   row.ClientKey,
		Status:      Status(row.Status),
		CreatedAt:   row.CreatedAt,
		UpdatedAt:   row.UpdatedAt,
		CompletedAt: row.CompletedAt,
	}
	if row.Language != nil {
		scan.Language = *row.Language
	}
	if row.LocationCell != nil {
		scan.LocationCell = *row.LocationCell
	}
	return scan
}

func imageFromRow(row dbgen.ScanImage) Image {
	return Image{
		ID:          row.ID,
		Position:    int(row.Position),
		StorageKey:  row.StorageKey,
		ContentType: row.ContentType,
		ByteSize:    int(row.ByteSize),
		Width:       int(row.Width),
		Height:      int(row.Height),
		SHA256:      row.Sha256,
		CreatedAt:   row.CreatedAt,
	}
}

func resultFromRow(row dbgen.ScanResult) (Result, error) {
	result := Result{
		Reasons:   row.Reasons,
		Signals:   row.Signals,
		Extracted: row.Extracted,
		Features:  row.Features,
	}
	if row.Provider != nil {
		result.Provider = *row.Provider
	}
	if row.Error != nil {
		result.Error = *row.Error
	}
	if row.VerdictCode != nil {
		value := verdict.Result{
			Verdict:      verdict.Verdict(*row.VerdictCode),
			Score:        int(row.VerdictScore.Int16),
			RulesVersion: valueOrEmpty(row.VerdictRulesVersion),
		}
		if row.VerdictConfidence != nil {
			value.Confidence = verdict.Confidence(*row.VerdictConfidence)
		}
		if len(row.Reasons) > 0 {
			if err := json.Unmarshal(row.Reasons, &value.Reasons); err != nil {
				return Result{}, fmt.Errorf("decode verdict reasons: %w", err)
			}
		}
		result.Verdict = &value
	}
	return result, nil
}

func valueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optional(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
