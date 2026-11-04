// Command eval scores AI-dependent components against versioned, labelled
// datasets and enforces release gates.
//
// Subcommands:
//
//	assistant   score a recorded assistant run against an assistant dataset
//	extraction  score pack photos against an extraction manifest
//	retrieval   score leaflet retrieval against a labelled question set
//	verdict     replay normalised signals through the verdict rules
//	judge-calibration  measure judge/human agreement before a judge is used
//
// Reports always carry the dataset version and hash, model, prompt version, and
// rules version. A failing gate exits non-zero.
//
// Dataset files are not fabricated here. Fixtures under testdata only prove the
// machinery; they never stand in for human labels.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/benfaruna/medverify/internal/eval"
	"github.com/benfaruna/medverify/internal/platform/config"
	"github.com/benfaruna/medverify/internal/platform/db"
	"github.com/benfaruna/medverify/internal/rag"
	"github.com/benfaruna/medverify/internal/verdict"
	"github.com/benfaruna/medverify/internal/vision"
	"github.com/benfaruna/medverify/internal/vision/visioncfg"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

const usage = `usage: eval <command> [options]

commands:
  assistant   score recorded assistant outputs against a dataset (offline)
  extraction  score pack photos against a labelled extraction manifest
  retrieval   score leaflet retrieval against a labelled question set
  verdict     replay normalised signals through the verdict rules
  judge-calibration  measure judge/human agreement before a judge is used

Run "eval <command> -h" for command options.
`

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	switch args[0] {
	case "assistant":
		return runAssistant(args[1:], stdout, stderr)
	case "extraction":
		return runExtraction(args[1:], stdout, stderr)
	case "retrieval":
		return runRetrieval(args[1:], stdout, stderr)
	case "verdict":
		return runVerdict(args[1:], stdout, stderr)
	case "judge-calibration":
		return runJudgeCalibration(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	default:
		_, _ = fmt.Fprintf(stderr, "eval: unknown command %q\n\n", args[0])
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
}

func runAssistant(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("assistant", flag.ContinueOnError)
	flags.SetOutput(stderr)
	datasetDir := flags.String("dataset", "", "dataset directory containing dataset.json and cases.jsonl")
	recordedPath := flags.String("recorded", "", "JSONL recorded assistant outputs")
	split := flags.String("split", "dev", "dataset split: dev or test")
	maxCost := flags.Float64("max-cost", 0, "fail if the recorded run cost exceeds this many USD (0 disables the cap)")
	gatesPath := flags.String("gates", "", "gate file (optional)")
	outDir := flags.String("out-dir", "", "directory for JSON and Markdown reports (optional)")
	asJSON := flags.Bool("json", false, "print the JSON report instead of Markdown")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *datasetDir == "" || *recordedPath == "" {
		_, _ = fmt.Fprintln(stderr, "eval: assistant requires -dataset and -recorded")
		return 2
	}
	selectedSplit := eval.Split(*split)
	if !selectedSplit.Valid() {
		_, _ = fmt.Fprintf(stderr, "eval: -split must be dev or test, got %q\n", *split)
		return 2
	}

	dataset, err := eval.LoadDataset(*datasetDir)
	if err != nil {
		return fail(stderr, err)
	}
	outputs, err := eval.LoadRecordedOutputs(*recordedPath)
	if err != nil {
		return fail(stderr, err)
	}
	report, err := eval.ScoreAssistant(dataset, selectedSplit, outputs, now())
	if err != nil {
		return fail(stderr, err)
	}
	if *maxCost > 0 && report.CostUSD > *maxCost {
		return fail(stderr, fmt.Errorf("recorded run cost $%.4f exceeds the cap $%.4f", report.CostUSD, *maxCost))
	}
	return finishReport(report, *gatesPath, *outDir, *asJSON, stdout, stderr)
}

func runExtraction(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("extraction", flag.ContinueOnError)
	flags.SetOutput(stderr)
	manifestPath := flags.String("manifest", "testdata/eval/extraction.json", "path to the extraction eval manifest")
	version := flags.String("version", "", "dataset version recorded in the report (defaults to the manifest name)")
	minNRN := flags.Float64("min-nrn", 0.90, "minimum normalized NRN match rate")
	gatesPath := flags.String("gates", "", "gate file (optional)")
	outDir := flags.String("out-dir", "", "directory for JSON and Markdown reports (optional)")
	asJSON := flags.Bool("json", false, "print the JSON report instead of Markdown")
	if err := flags.Parse(args); err != nil {
		return 2
	}

	cfg, err := config.Load()
	if err != nil {
		return fail(stderr, err)
	}
	if cfg.Vision.Provider == "fake" {
		_, _ = fmt.Fprintln(stderr, "eval: WARNING: VISION_PROVIDER=fake scores a deterministic placeholder, not a model")
	}
	provider, err := visioncfg.New(cfg.Vision)
	if err != nil {
		return fail(stderr, err)
	}
	manifestBytes, err := os.ReadFile(*manifestPath)
	if err != nil {
		return fail(stderr, fmt.Errorf("eval: read manifest: %w", err))
	}
	manifest, err := vision.LoadEvalManifest(*manifestPath)
	if err != nil {
		return fail(stderr, err)
	}
	baseDir := filepath.Dir(*manifestPath)
	source := vision.EvalReport{}
	model, promptVersion := "", ""
	for _, evalCase := range manifest.Cases {
		label, err := extractCase(context.Background(), provider, cfg.Vision.MaxImageDimension, baseDir, evalCase)
		if err != nil {
			return fail(stderr, fmt.Errorf("case %s: %w", evalCase.ID, err))
		}
		if model == "" {
			model, promptVersion = label.Model, label.PromptVersion
		}
		source.ScoreCase(evalCase, label)
	}
	if model == "" {
		model = "unknown"
	}
	if promptVersion == "" {
		promptVersion = "unknown"
	}
	datasetVersion := *version
	if datasetVersion == "" {
		datasetVersion = manifest.Name
	}
	report := eval.ExtractionReport(manifest.Name, datasetVersion, eval.HashBytes(manifestBytes),
		model, promptVersion, now(), source)

	failures, err := loadGates(*gatesPath, report)
	if err != nil {
		return fail(stderr, err)
	}
	builtIn := eval.GateFile{
		Version: "extraction-defaults-v1", Component: eval.ComponentExtraction,
		Thresholds: map[string]eval.Threshold{
			"fabricated_nrn": {Max: floatPointer(0)},
		},
	}
	if metric, ok := report.Metrics["field.nrn.normalized"]; ok && metric.Total > 0 {
		builtIn.Thresholds["field.nrn.normalized"] = eval.Threshold{Min: minNRN}
	}
	failures = append(failures, builtIn.Evaluate(report)...)
	if code := emit(report, *outDir, *asJSON, stdout, stderr); code != 0 {
		return code
	}
	return gateExit(failures, stderr)
}

func runRetrieval(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("retrieval", flag.ContinueOnError)
	flags.SetOutput(stderr)
	setPath := flags.String("file", "", "labelled retrieval eval YAML")
	k := flags.Int("k", rag.DefaultSearchLimit, "passages retrieved per question")
	gatesPath := flags.String("gates", "", "gate file (optional)")
	outDir := flags.String("out-dir", "", "directory for JSON and Markdown reports (optional)")
	asJSON := flags.Bool("json", false, "print the JSON report instead of Markdown")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *setPath == "" {
		_, _ = fmt.Fprintln(stderr, "eval: retrieval requires -file")
		return 2
	}
	setBytes, err := os.ReadFile(*setPath)
	if err != nil {
		return fail(stderr, fmt.Errorf("eval: read retrieval set: %w", err))
	}
	set, err := rag.LoadEvalSet(*setPath)
	if err != nil {
		return fail(stderr, err)
	}
	cfg, err := config.Load()
	if err != nil {
		return fail(stderr, err)
	}
	embedder, err := rag.NewEmbedder(cfg.Embeddings)
	if err != nil {
		return fail(stderr, err)
	}
	pool, err := db.Open(cfg.Database)
	if err != nil {
		return fail(stderr, err)
	}
	defer pool.Close()

	store := rag.NewStore(pool)
	source, err := rag.Evaluate(context.Background(), store, embedder, store, set, rag.EvalOptions{K: *k})
	if err != nil {
		return fail(stderr, err)
	}
	report := eval.RetrievalReport(filepath.Base(*setPath), set.Version,
		eval.HashBytes(setBytes), embedder.Model(), now(), source)

	return finishReport(report, *gatesPath, *outDir, *asJSON, stdout, stderr)
}

func runVerdict(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("verdict", flag.ContinueOnError)
	flags.SetOutput(stderr)
	datasetDir := flags.String("dataset", "", "dataset directory containing dataset.json and cases.jsonl")
	split := flags.String("split", "test", "dataset split: dev or test")
	rulesPath := flags.String("rules", "", "verdict rules YAML (defaults to the embedded version)")
	gatesPath := flags.String("gates", "", "gate file (optional)")
	outDir := flags.String("out-dir", "", "directory for JSON and Markdown reports (optional)")
	asJSON := flags.Bool("json", false, "print the JSON report instead of Markdown")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *datasetDir == "" {
		_, _ = fmt.Fprintln(stderr, "eval: verdict requires -dataset")
		return 2
	}
	selectedSplit := eval.Split(*split)
	if !selectedSplit.Valid() {
		_, _ = fmt.Fprintf(stderr, "eval: -split must be dev or test, got %q\n", *split)
		return 2
	}
	rules, err := verdict.LoadRules(*rulesPath)
	if err != nil {
		return fail(stderr, err)
	}
	dataset, err := eval.LoadDataset(*datasetDir)
	if err != nil {
		return fail(stderr, err)
	}
	report, err := eval.ScoreVerdict(dataset, selectedSplit, rules, now())
	if err != nil {
		return fail(stderr, err)
	}
	return finishReport(report, *gatesPath, *outDir, *asJSON, stdout, stderr)
}

func runJudgeCalibration(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("judge-calibration", flag.ContinueOnError)
	flags.SetOutput(stderr)
	labelsPath := flags.String("labels", "", "JSONL human/judge label pairs")
	values := flags.String("values", "", "comma-separated label values, e.g. supported,unsupported")
	minKappa := flags.Float64("min-kappa", 0, "agreed kappa floor (required)")
	minCases := flags.Int("min-cases", 0, "agreed minimum labelled pairs (required)")
	asJSON := flags.Bool("json", false, "print the calibration as JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *labelsPath == "" || *values == "" {
		_, _ = fmt.Fprintln(stderr, "eval: judge-calibration requires -labels and -values")
		return 2
	}
	if *minKappa <= 0 || *minCases <= 0 {
		_, _ = fmt.Fprintln(stderr, "eval: judge-calibration requires an agreed -min-kappa and -min-cases; do not invent thresholds")
		return 2
	}
	declared := make([]string, 0, 4)
	for _, value := range strings.Split(*values, ",") {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			declared = append(declared, trimmed)
		}
	}
	records, err := eval.LoadJudgeLabels(*labelsPath)
	if err != nil {
		return fail(stderr, err)
	}
	calibration, err := eval.CalibrateJudge(records, declared)
	if err != nil {
		return fail(stderr, err)
	}

	if *asJSON {
		encoded, err := json.MarshalIndent(calibration, "", "  ")
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = fmt.Fprintln(stdout, string(encoded))
	} else {
		_, _ = fmt.Fprintf(stdout, "judge calibration: cases=%d kappa=%.4f agreement=%.4f\n",
			calibration.Cases, calibration.Kappa, calibration.Agreement)
	}
	failures := calibration.Meets(*minKappa, *minCases)
	if len(failures) == 0 {
		return 0
	}
	_, _ = fmt.Fprintln(stderr, "eval: judge is not calibrated for release use:")
	for _, failure := range failures {
		_, _ = fmt.Fprintln(stderr, "  - "+failure)
	}
	return 1
}

func loadGates(path string, report eval.Report) ([]eval.GateFailure, error) {
	if path == "" {
		return nil, nil
	}
	gates, err := eval.LoadGates(path)
	if err != nil {
		return nil, err
	}
	return gates.Evaluate(report), nil
}

func emit(report eval.Report, outDir string, asJSON bool, stdout, stderr io.Writer) int {
	if err := report.Validate(); err != nil {
		return fail(stderr, err)
	}
	if outDir != "" {
		base := filepath.Join(outDir, reportFilename(report))
		if err := report.WriteJSON(base + ".json"); err != nil {
			return fail(stderr, err)
		}
		if err := report.WriteMarkdown(base + ".md"); err != nil {
			return fail(stderr, err)
		}
	}
	if asJSON {
		encoded, err := report.JSON()
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = stdout.Write(encoded)
		return 0
	}
	rendered, err := report.Markdown()
	if err != nil {
		return fail(stderr, err)
	}
	_, _ = fmt.Fprint(stdout, rendered)
	return 0
}

func gateExit(failures []eval.GateFailure, stderr io.Writer) int {
	if len(failures) == 0 {
		return 0
	}
	_, _ = fmt.Fprintf(stderr, "eval: %d release gate(s) failed:\n", len(failures))
	for _, failure := range failures {
		_, _ = fmt.Fprintf(stderr, "  - %s\n", failure.String())
	}
	return 1
}

func fail(stderr io.Writer, err error) int {
	message := err.Error()
	if !strings.HasPrefix(message, "eval: ") {
		message = "eval: " + message
	}
	_, _ = fmt.Fprintln(stderr, message)
	return 1
}

func reportFilename(report eval.Report) string {
	parts := []string{string(report.Component), report.Dataset, report.DatasetVersion}
	if report.Split != "" {
		parts = append(parts, string(report.Split))
	}
	return sanitize(strings.Join(parts, "-"))
}

func sanitize(value string) string {
	var builder strings.Builder
	lastDash := false
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			builder.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				builder.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(builder.String(), "-")
}

func floatPointer(value float64) *float64 { return &value }

// now is a seam for deterministic tests.
var now = func() time.Time { return time.Now() }

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

// finishReport writes the report, then reports gate failures. A gate failure
// exits non-zero even though the report was written, so a CI step cannot miss
// it.
func finishReport(report eval.Report, gatesPath, outDir string, asJSON bool, stdout, stderr io.Writer) int {
	failures, err := loadGates(gatesPath, report)
	if err != nil {
		return fail(stderr, err)
	}
	if code := emit(report, outDir, asJSON, stdout, stderr); code != 0 {
		return code
	}
	return gateExit(failures, stderr)
}
