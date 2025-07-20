package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/urfave/cli/v2"
)

func main() {
	app := &cli.App{
		Name:  "omet-healthcheck",
		Usage: "Health check tool for OMET metrics",
		Description: `Fast health checking for OMET-generated metrics files.
		
Examples:
  # Check if metrics were written recently
  omet-healthcheck /shared/metrics.prom --max-age=300s
  
  # Check consecutive error count
  omet-healthcheck /shared/metrics.prom --max-consecutive-errors=10
  
  # Check if specific metric exists
  omet-healthcheck /shared/metrics.prom --metric-exists=omet_last_write
  
  # Multiple checks (all must pass)
  omet-healthcheck /shared/metrics.prom --max-age=300s --max-consecutive-errors=5

Exit codes:
  0 = healthy (all checks passed)
  1 = unhealthy (one or more checks failed)
  2 = error (file not found, parse error, etc.)`,

		Flags: []cli.Flag{
			&cli.DurationFlag{
				Name:  "max-age",
				Usage: "Maximum age since last write (e.g. 300s, 5m)",
			},
			&cli.IntFlag{
				Name:  "max-consecutive-errors",
				Usage: "Maximum allowed consecutive errors",
				Value: -1, // -1 means don't check
			},
			&cli.StringFlag{
				Name:  "metric-exists",
				Usage: "Check that specified metric exists",
			},
			&cli.BoolFlag{
				Name:  "verbose",
				Usage: "Enable verbose output",
			},
		},

		ArgsUsage: "<metrics_file>",
		Action:    checkHealth,
	}

	if err := app.Run(os.Args); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(2)
	}
}

type HealthCheckResult struct {
	Healthy              bool
	Checks               map[string]CheckResult
	Error                string
	LastWriteTimestamp   *int64
	ConsecutiveErrors    *float64
	MetricsFound         []string
}

type CheckResult struct {
	Passed  bool
	Message string
	Value   string
}

func checkHealth(ctx *cli.Context) error {
	if ctx.NArg() == 0 {
		return fmt.Errorf("missing required argument: metrics_file")
	}

	filename := ctx.Args().Get(0)
	verbose := ctx.Bool("verbose")

	if verbose {
		log.Printf("Checking health of metrics file: %s", filename)
	}

	// Parse metrics file
	families, err := parseMetricsFile(filename)
	if err != nil {
		return fmt.Errorf("failed to parse metrics file: %w", err)
	}

	if verbose {
		log.Printf("Parsed %d metric families", len(families))
	}

	// Perform health checks
	result := HealthCheckResult{
		Healthy: true,
		Checks:  make(map[string]CheckResult),
	}

	// Check 1: Max age (if specified)
	if ctx.IsSet("max-age") {
		maxAge := ctx.Duration("max-age")
		checkMaxAge(families, maxAge, &result, verbose)
	}

	// Check 2: Max consecutive errors (if specified)
	if ctx.IsSet("max-consecutive-errors") {
		maxErrors := ctx.Int("max-consecutive-errors")
		if maxErrors >= 0 {
			checkConsecutiveErrors(families, maxErrors, &result, verbose)
		}
	}

	// Check 3: Metric exists (if specified)
	if ctx.IsSet("metric-exists") {
		metricName := ctx.String("metric-exists")
		checkMetricExists(families, metricName, &result, verbose)
	}

	// If no specific checks were requested, do basic health check
	if !ctx.IsSet("max-age") && !ctx.IsSet("max-consecutive-errors") && !ctx.IsSet("metric-exists") {
		checkBasicHealth(families, &result, verbose)
	}

	// Output results
	outputText(&result, verbose)

	// Exit with appropriate code
	if !result.Healthy {
		os.Exit(1) // Unhealthy
	}

	return nil // Healthy
}

func parseMetricsFile(filename string) (map[string]*dto.MetricFamily, error) {
	file, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	return parseMetrics(file)
}

func parseMetrics(input io.Reader) (map[string]*dto.MetricFamily, error) {
	parser := expfmt.TextParser{}
	families, err := parser.TextToMetricFamilies(input)
	if err != nil {
		return nil, err
	}
	return families, nil
}

func checkMaxAge(families map[string]*dto.MetricFamily, maxAge time.Duration, result *HealthCheckResult, verbose bool) {
	family, exists := families["omet_last_write"]
	if !exists {
		result.Healthy = false
		result.Checks["max_age"] = CheckResult{
			Passed:  false,
			Message: "omet_last_write metric not found",
		}
		if verbose {
			log.Printf("FAIL: omet_last_write metric not found")
		}
		return
	}

	if len(family.Metric) == 0 {
		result.Healthy = false
		result.Checks["max_age"] = CheckResult{
			Passed:  false,
			Message: "omet_last_write metric has no data",
		}
		if verbose {
			log.Printf("FAIL: omet_last_write metric has no data")
		}
		return
	}

	// Get timestamp from gauge
	timestamp := int64(family.Metric[0].GetGauge().GetValue())
	result.LastWriteTimestamp = &timestamp
	
	lastWrite := time.Unix(timestamp, 0)
	age := time.Since(lastWrite)

	if age > maxAge {
		result.Healthy = false
		result.Checks["max_age"] = CheckResult{
			Passed:  false,
			Message: fmt.Sprintf("Last write too old: %v (max: %v)", age.Round(time.Second), maxAge),
			Value:   age.String(),
		}
		if verbose {
			log.Printf("FAIL: Last write too old: %v (max: %v)", age.Round(time.Second), maxAge)
		}
	} else {
		result.Checks["max_age"] = CheckResult{
			Passed:  true,
			Message: fmt.Sprintf("Last write age OK: %v (max: %v)", age.Round(time.Second), maxAge),
			Value:   age.String(),
		}
		if verbose {
			log.Printf("PASS: Last write age OK: %v", age.Round(time.Second))
		}
	}
}

func checkConsecutiveErrors(families map[string]*dto.MetricFamily, maxErrors int, result *HealthCheckResult, verbose bool) {
	family, exists := families["omet_consecutive_errors_total"]
	if !exists {
		// No consecutive errors metric means no errors (healthy)
		result.Checks["consecutive_errors"] = CheckResult{
			Passed:  true,
			Message: "No consecutive errors metric found (assuming healthy)",
			Value:   "0",
		}
		if verbose {
			log.Printf("PASS: No consecutive errors metric found (assuming healthy)")
		}
		return
	}

	if len(family.Metric) == 0 {
		result.Checks["consecutive_errors"] = CheckResult{
			Passed:  true,
			Message: "Consecutive errors metric has no data (assuming healthy)",
			Value:   "0",
		}
		if verbose {
			log.Printf("PASS: Consecutive errors metric has no data (assuming healthy)")
		}
		return
	}

	// Get consecutive error count from gauge
	consecutiveErrors := family.Metric[0].GetGauge().GetValue()
	result.ConsecutiveErrors = &consecutiveErrors

	if int(consecutiveErrors) > maxErrors {
		result.Healthy = false
		result.Checks["consecutive_errors"] = CheckResult{
			Passed:  false,
			Message: fmt.Sprintf("Too many consecutive errors: %.0f (max: %d)", consecutiveErrors, maxErrors),
			Value:   fmt.Sprintf("%.0f", consecutiveErrors),
		}
		if verbose {
			log.Printf("FAIL: Too many consecutive errors: %.0f (max: %d)", consecutiveErrors, maxErrors)
		}
	} else {
		result.Checks["consecutive_errors"] = CheckResult{
			Passed:  true,
			Message: fmt.Sprintf("Consecutive errors OK: %.0f (max: %d)", consecutiveErrors, maxErrors),
			Value:   fmt.Sprintf("%.0f", consecutiveErrors),
		}
		if verbose {
			log.Printf("PASS: Consecutive errors OK: %.0f", consecutiveErrors)
		}
	}
}

func checkMetricExists(families map[string]*dto.MetricFamily, metricName string, result *HealthCheckResult, verbose bool) {
	_, exists := families[metricName]
	
	if !exists {
		result.Healthy = false
		result.Checks["metric_exists"] = CheckResult{
			Passed:  false,
			Message: fmt.Sprintf("Metric '%s' not found", metricName),
		}
		if verbose {
			log.Printf("FAIL: Metric '%s' not found", metricName)
		}
	} else {
		result.Checks["metric_exists"] = CheckResult{
			Passed:  true,
			Message: fmt.Sprintf("Metric '%s' found", metricName),
		}
		if verbose {
			log.Printf("PASS: Metric '%s' found", metricName)
		}
	}

	// Add list of found metrics for debugging
	var metricNames []string
	for name := range families {
		metricNames = append(metricNames, name)
	}
	result.MetricsFound = metricNames
}

func checkBasicHealth(families map[string]*dto.MetricFamily, result *HealthCheckResult, verbose bool) {
	// Basic health check: ensure we have some metrics and omet_last_write exists
	if len(families) == 0 {
		result.Healthy = false
		result.Checks["basic_health"] = CheckResult{
			Passed:  false,
			Message: "No metrics found in file",
		}
		if verbose {
			log.Printf("FAIL: No metrics found in file")
		}
		return
	}

	// Check for omet_last_write (indicates omet is working)
	if _, exists := families["omet_last_write"]; !exists {
		result.Healthy = false
		result.Checks["basic_health"] = CheckResult{
			Passed:  false,
			Message: "omet_last_write metric not found (omet may not be running)",
		}
		if verbose {
			log.Printf("FAIL: omet_last_write metric not found")
		}
		return
	}

	result.Checks["basic_health"] = CheckResult{
		Passed:  true,
		Message: fmt.Sprintf("Basic health OK: %d metric families found", len(families)),
	}
	if verbose {
		log.Printf("PASS: Basic health OK: %d metric families found", len(families))
	}
}


func outputText(result *HealthCheckResult, verbose bool) {
	if result.Healthy {
		fmt.Printf("HEALTHY")
		if verbose {
			fmt.Printf(" - All checks passed")
		}
		fmt.Printf("\n")
	} else {
		fmt.Printf("UNHEALTHY")
		if verbose {
			fmt.Printf(" - One or more checks failed")
		}
		fmt.Printf("\n")
		
		// Show failed checks
		for name, check := range result.Checks {
			if !check.Passed {
				fmt.Printf("  %s: %s\n", name, check.Message)
			}
		}
	}
}
