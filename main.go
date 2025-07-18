package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/urfave/cli/v2"
)

// Standard histogram buckets for response times (in seconds)
var defaultHistogramBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

func main() {
	app := &cli.App{
		Name:  "omet",
		Usage: "OpenMetrics manipulation tool",
		Description: `A tool for reading, modifying, and writing Prometheus/OpenMetrics format data.
        
Examples:
  # Value from stdin
  echo "42" | omet -f metrics.txt -l queue=processing queue_depth set
  grep ERROR app.log | wc -l | omet -f metrics.txt -l level=error error_count set

  # Explicit value  
  omet -f metrics.txt -l region=us-east request_count inc 1
  omet -f metrics.txt -l queue=processing queue_depth set 42`,

		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "file",
				Aliases: []string{"f"},
				Usage:   "Input metrics file (default: stdin)",
				Value:   "-",
			},
			&cli.StringSliceFlag{
				Name:    "label",
				Aliases: []string{"l"},
				Usage:   "Add label in KEY=VALUE format (can be repeated)",
			},
			&cli.BoolFlag{
				Name:    "verbose",
				Aliases: []string{"v"},
				Usage:   "Enable verbose logging",
			},
		},

		ArgsUsage: "<metric_name> <operation> [value]",

		Action: runOmet,
	}

	if err := app.Run(os.Args); err != nil {
		log.Fatal(err)
	}
}

func runOmet(ctx *cli.Context) error {
	// Validate arguments
	if ctx.NArg() < 2 {
		return cli.ShowAppHelp(ctx)
	}

	metricName := ctx.Args().Get(0)
	operation := ctx.Args().Get(1)

	// Parse labels
	labels, err := parseLabels(ctx.StringSlice("label"))
	if err != nil {
		return fmt.Errorf("invalid label format: %w", err)
	}

	if ctx.Bool("verbose") {
		log.Printf("Metric: %s, Operation: %s, Labels: %v", metricName, operation, labels)
	}

	// Determine value
	var value float64
	if ctx.NArg() >= 3 {
		// Value provided as argument
		val, err := strconv.ParseFloat(ctx.Args().Get(2), 64)
		if err != nil {
			return fmt.Errorf("invalid value '%s': %w", ctx.Args().Get(2), err)
		}
		value = val
	} else {
		// Read value from stdin or use default
		if operation == "inc" {
			value = 1.0 // Default increment
		} else {
			val, err := readValueFromStdin()
			if err != nil {
				return fmt.Errorf("failed to read value from stdin: %w", err)
			}
			value = val
		}
	}

	if ctx.Bool("verbose") {
		log.Printf("Using value: %g", value)
	}

	// Read input metrics
	var input io.Reader
	filename := ctx.String("file")
	if filename == "-" {
		input = os.Stdin
	} else {
		file, err := os.Open(filename)
		if err != nil {
			return fmt.Errorf("failed to open file %s: %w", filename, err)
		}
		defer file.Close()
		input = file
	}

	// Parse existing metrics
	families, err := parseMetrics(input)
	if err != nil {
		return fmt.Errorf("failed to parse metrics: %w", err)
	}

	if ctx.Bool("verbose") {
		log.Printf("Parsed %d metric families", len(families))
	}

	// Apply the operation
	err = applyOperation(families, metricName, operation, labels, value)
	if err != nil {
		return fmt.Errorf("failed to apply operation: %w", err)
	}

	// Write output
	err = writeMetrics(families, os.Stdout)
	if err != nil {
		return fmt.Errorf("failed to write metrics: %w", err)
	}

	return nil
}

func parseLabels(labelStrings []string) (map[string]string, error) {
	labels := make(map[string]string)

	for _, labelStr := range labelStrings {
		parts := strings.SplitN(labelStr, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid label format: %s (expected KEY=VALUE)", labelStr)
		}
		labels[parts[0]] = parts[1]
	}

	return labels, nil
}

func readValueFromStdin() (float64, error) {
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return 0, fmt.Errorf("no input available")
	}

	line := strings.TrimSpace(scanner.Text())
	if line == "" {
		return 0, fmt.Errorf("empty input")
	}

	val, err := strconv.ParseFloat(line, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid number: %s", line)
	}

	return val, nil
}

func parseMetrics(input io.Reader) (map[string]*dto.MetricFamily, error) {
	parser := expfmt.TextParser{}
	families, err := parser.TextToMetricFamilies(input)
	if err != nil {
		return nil, err
	}
	return families, nil
}

func applyOperation(families map[string]*dto.MetricFamily, metricName, operation string, labels map[string]string, value float64) error {
	switch operation {
	case "inc":
		return incrementCounter(families, metricName, labels, value)
	case "set":
		return setGauge(families, metricName, labels, value)
	case "observe":
		return observeHistogram(families, metricName, labels, value)
	default:
		return fmt.Errorf("unknown operation: %s (supported: inc, set, observe)", operation)
	}
}

func incrementCounter(families map[string]*dto.MetricFamily, name string, labels map[string]string, increment float64) error {
	family, err := getOrCreateFamily(families, name, dto.MetricType_COUNTER)
	if err != nil {
		return err
	}

	metric := findOrCreateMetric(family, labels)

	if metric.Counter == nil {
		metric.Counter = &dto.Counter{Value: float64Ptr(0)}
	}

	currentValue := metric.Counter.GetValue()
	metric.Counter.Value = float64Ptr(currentValue + increment)

	return nil
}

func setGauge(families map[string]*dto.MetricFamily, name string, labels map[string]string, value float64) error {
	family, err := getOrCreateFamily(families, name, dto.MetricType_GAUGE)
	if err != nil {
		return err
	}

	metric := findOrCreateMetric(family, labels)
	metric.Gauge = &dto.Gauge{Value: float64Ptr(value)}

	return nil
}

func createMetricFamily(name string, metricType dto.MetricType) *dto.MetricFamily {
	typeStr := strings.ToLower(metricType.String())
	// Capitalize first letter manually for consistency
	if len(typeStr) > 0 {
		typeStr = strings.ToUpper(typeStr[:1]) + typeStr[1:]
	}
	
	return &dto.MetricFamily{
		Name: &name,
		Type: &metricType,
		Help: stringPtr(fmt.Sprintf("%s metric %s", typeStr, name)),
	}
}

func validateMetricType(family *dto.MetricFamily, expectedType dto.MetricType, metricName string) error {
	if family.GetType() != expectedType {
		return fmt.Errorf("metric %s is not a %s (type: %s)", 
			metricName, 
			strings.ToLower(expectedType.String()), 
			family.GetType())
	}
	return nil
}

func getOrCreateFamily(families map[string]*dto.MetricFamily, name string, metricType dto.MetricType) (*dto.MetricFamily, error) {
	family, exists := families[name]
	if !exists {
		family = createMetricFamily(name, metricType)
		families[name] = family
	}

	if err := validateMetricType(family, metricType, name); err != nil {
		return nil, err
	}

	return family, nil
}

func observeHistogram(families map[string]*dto.MetricFamily, name string, labels map[string]string, value float64) error {
	// TODO: Implement histogram observation
	// This would require:
	// 1. Managing histogram buckets
	// 2. Updating count and sum
	// 3. Incrementing appropriate bucket counters
	return fmt.Errorf("histogram observe operation not yet implemented")
}

func findOrCreateMetric(family *dto.MetricFamily, labels map[string]string) *dto.Metric {
	// Look for existing metric with matching labels
	for _, metric := range family.Metric {
		if labelsMatch(metric.Label, labels) {
			return metric
		}
	}

	// Create new metric
	metric := &dto.Metric{
		Label: createLabelPairs(labels),
	}

	family.Metric = append(family.Metric, metric)
	return metric
}

func labelsMatch(existingLabels []*dto.LabelPair, newLabels map[string]string) bool {
	if len(existingLabels) != len(newLabels) {
		return false
	}

	for _, labelPair := range existingLabels {
		value, exists := newLabels[labelPair.GetName()]
		if !exists || value != labelPair.GetValue() {
			return false
		}
	}

	return true
}

func createLabelPairs(labels map[string]string) []*dto.LabelPair {
	var labelPairs []*dto.LabelPair
	for key, value := range labels {
		labelPairs = append(labelPairs, &dto.LabelPair{
			Name:  stringPtr(key),
			Value: stringPtr(value),
		})
	}
	return labelPairs
}

func writeMetrics(families map[string]*dto.MetricFamily, output io.Writer) error {
	// Convert back to text format
	for _, family := range families {
		// Write HELP line
		if family.Help != nil {
			fmt.Fprintf(output, "# HELP %s %s\n", family.GetName(), family.GetHelp())
		}

		// Write TYPE line
		if family.Type != nil {
			fmt.Fprintf(output, "# TYPE %s %s\n", family.GetName(), strings.ToLower(family.GetType().String()))
		}

		// Write metrics
		for _, metric := range family.Metric {
			name := family.GetName()

			// Build label string
			var labelParts []string
			for _, label := range metric.Label {
				labelParts = append(labelParts, fmt.Sprintf("%s=\"%s\"", label.GetName(), label.GetValue()))
			}

			var labelStr string
			if len(labelParts) > 0 {
				labelStr = "{" + strings.Join(labelParts, ",") + "}"
			}

			// Write value based on type
			var value float64
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				value = metric.GetCounter().GetValue()
			case dto.MetricType_GAUGE:
				value = metric.GetGauge().GetValue()
			default:
				if metric.Untyped != nil {
					value = metric.GetUntyped().GetValue()
				}
			}

			fmt.Fprintf(output, "%s%s %g\n", name, labelStr, value)
		}
	}

	return nil
}

func stringPtr(s string) *string {
	return &s
}

func float64Ptr(f float64) *float64 {
	return &f
}
