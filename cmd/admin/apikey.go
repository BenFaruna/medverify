package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/benfaruna/medverify/internal/platform/auth"
	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/platform/db/dbgen"
)

// Plan defaults for a new client. They mirror the migration defaults so a
// client created by hand behaves like one created by a future admin API.
const (
	defaultRatePerMin        int32 = 60
	defaultDailyRequestQuota int32 = 10000
	defaultDailyScanQuota    int32 = 200
)

// keyOptions is the parsed form of `apikey create`.
type keyOptions struct {
	client            string
	sandbox           bool
	scopes            []string
	expires           time.Duration
	ratePerMin        int32
	dailyRequestQuota int32
	dailyScanQuota    int32
	planSet           bool
}

func apikeyCommand(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("apikey requires one of: create, list, revoke")
	}
	switch args[0] {
	case "create":
		return apikeyCreate(ctx, cfg, args[1:])
	case "list":
		return apikeyList(ctx, cfg)
	case "revoke":
		return apikeyRevoke(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("unknown apikey action %q", args[0])
	}
}

func apikeyCreate(ctx context.Context, cfg config.Config, args []string) error {
	options, err := parseKeyOptions(args)
	if err != nil {
		return err
	}

	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()
	queries := dbgen.New(pool)

	client, err := queries.GetAPIClientByName(ctx, options.client)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		client, err = queries.CreateAPIClient(ctx, dbgen.CreateAPIClientParams{
			Name:              options.client,
			Sandbox:           options.sandbox,
			RatePerMin:        options.ratePerMin,
			DailyRequestQuota: options.dailyRequestQuota,
			DailyScanQuota:    options.dailyScanQuota,
		})
		if err != nil {
			return fmt.Errorf("create api client: %w", err)
		}
	case err != nil:
		return fmt.Errorf("load api client: %w", err)
	case options.planSet:
		// Quotas and sandbox are per client, not per key. Silently ignoring the
		// flags would leave an operator believing a limit changed.
		return fmt.Errorf("client %q already exists; plan and sandbox flags only apply at client creation", options.client)
	}

	fullKey, hash, prefix, _, err := auth.GenerateAPIKey()
	if err != nil {
		return fmt.Errorf("generate api key: %w", err)
	}
	params := dbgen.CreateAPIKeyParams{
		ClientID:  client.ID,
		KeyPrefix: prefix,
		KeyHash:   hash,
		Scopes:    options.scopes,
	}
	if options.expires > 0 {
		expires := time.Now().Add(options.expires)
		params.ExpiresAt = &expires
	}
	if _, err := queries.CreateAPIKey(ctx, params); err != nil {
		return fmt.Errorf("create api key: %w", err)
	}

	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintf(out, "client:\t%s (id %d)\n", client.Name, client.ID)
	_, _ = fmt.Fprintf(out, "sandbox:\t%t\n", client.Sandbox)
	_, _ = fmt.Fprintf(out, "scopes:\t%s\n", strings.Join(options.scopes, " "))
	_, _ = fmt.Fprintf(out, "expires:\t%s\n", expiryLabel(options.expires))
	_, _ = fmt.Fprintf(out, "key prefix:\tmv_%s\n", prefix)
	_ = out.Flush()

	// The full key is written once and never stored. There is no command that
	// can show it again; a lost key is revoked and replaced.
	_, _ = fmt.Fprintf(os.Stdout, "\nAPI key (shown once, store it now): %s\n", fullKey)
	_, _ = fmt.Fprintln(os.Stdout, "Do not paste it into logs, tickets, or chat.")
	return nil
}

func apikeyList(ctx context.Context, cfg config.Config) error {
	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	keys, err := dbgen.New(pool).ListAPIKeys(ctx)
	if err != nil {
		return fmt.Errorf("list api keys: %w", err)
	}

	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(out, "PREFIX\tCLIENT\tSTATE\tSCOPES\tCREATED\tLAST USED\tEXPIRES")
	for _, key := range keys {
		_, _ = fmt.Fprintf(out, "mv_%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			key.KeyPrefix,
			key.ClientName,
			keyState(key.Active, key.RevokedAt, key.ExpiresAt, time.Now()),
			strings.Join(key.Scopes, " "),
			key.CreatedAt.Format(time.DateOnly),
			optionalDate(key.LastUsedAt),
			optionalDate(key.ExpiresAt),
		)
	}
	return out.Flush()
}

func apikeyRevoke(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("apikey revoke requires one key or key prefix")
	}
	prefix, err := keyPrefixArg(args[0])
	if err != nil {
		return err
	}

	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	revoked, err := dbgen.New(pool).RevokeAPIKey(ctx, prefix)
	if err != nil {
		return fmt.Errorf("revoke api key: %w", err)
	}
	if revoked == 0 {
		return fmt.Errorf("no active key with prefix mv_%s", prefix)
	}
	_, _ = fmt.Fprintf(os.Stdout, "revoked %d key with prefix mv_%s\n", revoked, prefix)
	return nil
}

// parseKeyOptions accepts only the documented flags and rejects an unknown one
// rather than ignoring a typo.
func parseKeyOptions(args []string) (keyOptions, error) {
	options := keyOptions{
		scopes:            slices.Clone(auth.AllScopes),
		ratePerMin:        defaultRatePerMin,
		dailyRequestQuota: defaultDailyRequestQuota,
		dailyScanQuota:    defaultDailyScanQuota,
	}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		value := func() (string, error) {
			if i+1 >= len(args) {
				return "", fmt.Errorf("%s requires a value", arg)
			}
			i++
			return args[i], nil
		}

		var err error
		switch arg {
		case "--client":
			options.client, err = value()
		case "--scopes":
			var raw string
			raw, err = value()
			if err == nil {
				options.scopes, err = parseScopes(raw)
			}
		case "--expires":
			var raw string
			raw, err = value()
			if err == nil {
				options.expires, err = parseExpiry(raw)
			}
		case "--rate":
			var raw string
			raw, err = value()
			if err == nil {
				options.ratePerMin, err = positiveInt32(raw, "--rate")
				options.planSet = true
			}
		case "--daily-requests":
			var raw string
			raw, err = value()
			if err == nil {
				options.dailyRequestQuota, err = positiveInt32(raw, "--daily-requests")
				options.planSet = true
			}
		case "--daily-scans":
			var raw string
			raw, err = value()
			if err == nil {
				options.dailyScanQuota, err = positiveInt32(raw, "--daily-scans")
				options.planSet = true
			}
		case "--sandbox":
			options.sandbox = true
			options.planSet = true
		default:
			return keyOptions{}, fmt.Errorf("unknown apikey create option %q", arg)
		}
		if err != nil {
			return keyOptions{}, err
		}
	}

	if options.client == "" {
		return keyOptions{}, fmt.Errorf("apikey create requires --client <name>")
	}
	return options, nil
}

// parseScopes keeps the caller's order, removes duplicates, and rejects any
// scope this server does not enforce.
func parseScopes(raw string) ([]string, error) {
	scopes := make([]string, 0, len(auth.AllScopes))
	for _, scope := range strings.Split(raw, ",") {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if !slices.Contains(auth.AllScopes, scope) {
			return nil, fmt.Errorf("unknown scope %q (allowed: %s)", scope, strings.Join(auth.AllScopes, ", "))
		}
		if !slices.Contains(scopes, scope) {
			scopes = append(scopes, scope)
		}
	}
	if len(scopes) == 0 {
		return nil, fmt.Errorf("--scopes must name at least one scope")
	}
	return scopes, nil
}

func parseExpiry(raw string) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("--expires must be a duration such as 720h: %w", err)
	}
	if duration <= 0 {
		return 0, fmt.Errorf("--expires must be positive")
	}
	return duration, nil
}

func positiveInt32(raw, flag string) (int32, error) {
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", flag)
	}
	return int32(value), nil
}

// keyPrefixArg accepts either a full key or the bare lookup prefix, so an
// operator can revoke from a key they still hold or from a `list` row.
func keyPrefixArg(arg string) (string, error) {
	prefix := strings.TrimPrefix(arg, "mv_")
	if prefix == "" || strings.Contains(prefix, "_") {
		// A full key is parsed rather than trimmed, so a typo cannot revoke an
		// unrelated prefix. A bare prefix may not contain a separator.
		if strings.HasPrefix(arg, "mv_") && strings.Contains(prefix, "_") {
			parsed, err := auth.KeyPrefixFromToken(arg)
			if err != nil {
				return "", fmt.Errorf("not an issued key: %w", err)
			}
			return parsed, nil
		}
		return "", fmt.Errorf("expected a full key or a key prefix")
	}
	return prefix, nil
}

func keyState(active bool, revokedAt, expiresAt *time.Time, now time.Time) string {
	switch {
	case revokedAt != nil:
		return "revoked"
	case !active:
		return "inactive"
	case expiresAt != nil && !expiresAt.After(now):
		return "expired"
	default:
		return "active"
	}
}

func expiryLabel(duration time.Duration) string {
	if duration <= 0 {
		return "never"
	}
	return time.Now().Add(duration).Format(time.RFC3339)
}

func optionalDate(value *time.Time) string {
	if value == nil {
		return "-"
	}
	return value.Format(time.DateOnly)
}
