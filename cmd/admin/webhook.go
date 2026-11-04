package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"text/tabwriter"

	"github.com/google/uuid"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/webhook"
)

func webhookCommand(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("webhook requires one of: list, replay")
	}
	switch args[0] {
	case "list":
		return webhookList(ctx, cfg, args[1:])
	case "replay":
		return webhookReplay(ctx, cfg, args[1:])
	default:
		return fmt.Errorf("unknown webhook action %q", args[0])
	}
}

func webhookList(ctx context.Context, cfg config.Config, args []string) error {
	limit := int32(50)
	if len(args) > 0 {
		parsed, err := strconv.ParseInt(args[0], 10, 32)
		if err != nil || parsed <= 0 {
			return fmt.Errorf("webhook list limit must be a positive integer")
		}
		limit = int32(parsed)
	}

	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	deliveries, err := webhook.NewDBStore(pool).ListDead(ctx, limit)
	if err != nil {
		return err
	}

	out := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(out, "ID\tCLIENT\tEVENT\tATTEMPTS\tLAST STATUS\tURL")
	for _, delivery := range deliveries {
		status := "-"
		if delivery.LastStatusCode != nil {
			status = strconv.Itoa(int(*delivery.LastStatusCode))
		}
		_, _ = fmt.Fprintf(out, "%s\t%s\t%s\t%d\t%s\t%s\n",
			delivery.ID, delivery.ClientName, delivery.EventType, delivery.Attempts, status, delivery.URL)
	}
	if err := out.Flush(); err != nil {
		return err
	}
	if len(deliveries) == 0 {
		_, _ = fmt.Fprintln(os.Stdout, "no dead deliveries")
	}
	return nil
}

func webhookReplay(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("webhook replay requires one delivery id")
	}
	id, err := uuid.Parse(args[0])
	if err != nil {
		return fmt.Errorf("webhook replay: invalid delivery id: %w", err)
	}

	pool, err := db.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer pool.Close()

	replayed, err := webhook.NewDBStore(pool).Replay(ctx, id)
	if err != nil {
		return err
	}
	if !replayed {
		return fmt.Errorf("delivery %s is not dead; only a dead delivery can be replayed", id)
	}
	_, _ = fmt.Fprintf(os.Stdout, "delivery %s is queued for another attempt\n", id)
	return nil
}
