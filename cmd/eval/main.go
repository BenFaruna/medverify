// Command eval scores label extraction against a versioned ground-truth
// manifest. Phase 04 ships the extraction scorer; phase 10 extends it to the
// assistant and nightly regression runs.
//
// The manifest is not committed because real pack photos need recorded consent.
// See testdata/eval/README.md for the format.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/vision"
	"github.com/benfaruna/medverify/internal/vision/visioncfg"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "eval: "+err.Error())
		os.Exit(1)
	}
}

func run() error {
	manifestPath := flag.String("manifest", "testdata/eval/extraction.json", "path to the extraction eval manifest")
	asJSON := flag.Bool("json", false, "print the report as JSON")
	minNRN := flag.Float64("min-nrn", 0.90, "minimum normalized NRN match rate")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.Vision.Provider == "fake" {
		fmt.Fprintln(os.Stderr, "eval: WARNING: VISION_PROVIDER=fake scores a deterministic placeholder, not a model")
	}
	provider, err := visioncfg.New(cfg.Vision)
	if err != nil {
		return err
	}

	manifest, err := vision.LoadEvalManifest(*manifestPath)
	if err != nil {
		return err
	}
	baseDir := filepath.Dir(*manifestPath)
	report := vision.EvalReport{}

	for _, evalCase := range manifest.Cases {
		label, err := extractCase(context.Background(), provider, cfg.Vision.MaxImageDimension, baseDir, evalCase)
		if err != nil {
			return fmt.Errorf("case %s: %w", evalCase.ID, err)
		}
		report.ScoreCase(evalCase, label)
	}

	if *asJSON {
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Println(string(encoded))
	} else {
		printReport(manifest.Name, report)
	}

	if report.FabricatedNRN > 0 {
		return fmt.Errorf("%d case(s) produced an NRN where the labeller read none", report.FabricatedNRN)
	}
	if nrn := report.Fields["nrn"]; nrn.Expected > 0 && nrn.NormRate < *minNRN {
		return fmt.Errorf("NRN normalized match %.2f is below the %.2f gate", nrn.NormRate, *minNRN)
	}
	return nil
}

func extractCase(ctx context.Context, provider vision.Provider, maxDimension int, baseDir string, evalCase vision.EvalCase) (vision.LabelData, error) {
	labels := make([]vision.RawLabel, 0, len(evalCase.Images))
	providerName, model, promptVersion := "", "", ""
	for position, name := range evalCase.Images {
		data, err := os.ReadFile(filepath.Join(baseDir, name))
		if err != nil {
			return vision.LabelData{}, fmt.Errorf("read image %q: %w", name, err)
		}
		prepared, err := vision.Preprocess(data, maxDimension)
		if err != nil {
			return vision.LabelData{}, err
		}
		result, err := provider.Extract(ctx, vision.Request{
			ScanID:   evalCase.ID,
			Language: evalCase.Language,
			Images:   []vision.Image{{Position: position + 1, Data: prepared}},
		})
		if errors.Is(err, vision.ErrUnreadable) {
			continue
		}
		if err != nil {
			return vision.LabelData{}, err
		}
		if result.Label == nil {
			continue
		}
		if providerName == "" {
			providerName, model, promptVersion = result.Provider, result.Model, result.PromptVersion
		}
		labels = append(labels, result.Label.WithSource(position+1))
	}
	label := vision.ValidateLabel(vision.MergeLabels(labels...))
	label.Provider = providerName
	label.Model = model
	label.PromptVersion = promptVersion
	return label, nil
}

func printReport(name string, report vision.EvalReport) {
	fmt.Printf("eval set: %s\ncases: %d\nfabricated NRNs: %d\n", name, report.Cases, report.FabricatedNRN)

	fields := make([]string, 0, len(report.Fields))
	for field := range report.Fields {
		fields = append(fields, field)
	}
	sort.Strings(fields)
	for _, field := range fields {
		score := report.Fields[field]
		fmt.Printf("  %-14s expected %3d  exact %5.1f%%  normalized %5.1f%%\n",
			field, score.Expected, score.ExactRate*100, score.NormRate*100)
	}
	if len(report.Failures) > 0 {
		fmt.Println("failures:")
		for _, failure := range report.Failures {
			fmt.Println("  - " + failure)
		}
	}
}
