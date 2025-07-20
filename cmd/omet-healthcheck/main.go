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
	fmt.Printf("DEBUG: Starting with args: %v\n", os.Args)
	
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
				Value: -1,
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
	// DEBUG: Print raw CLI context info
	fmt.Printf("DEBUG: Raw args: %v\n", os.Args)
	fmt.Printf("DEBUG: ctx.NArg()=%d\n", ctx.NArg())
	fmt.Printf("DEBUG: ctx.Args().Slice()=%v\n", ctx.Args().Slice())
	
	if ctx.NArg() == 0 {
		return fmt.Errorf("missing required argument: metrics_file")
	}

	filename := ctx.Args().Get(0)
	verbose := ctx.Bool("verbose")

	// DEBUG: Print what we're getting from CLI parsing
	fmt.Printf("DEBUG: filename=%s\n", filename)
	fmt.Printf("DEBUG: max-age set=%v, value=%v\n", ctx.IsSet("max-age"), ctx.Duration("max-age"))
	fmt.Printf("DEBUG: max-consecutive-errors set=%v, value=%v\n", ctx.IsSet("max-consecutive-errors"), ctx.Int("max-consecutive-errors"))
	fmt.Printf("DEBUG: metric-exists set=%v, value=%s\n", ctx.IsSet("metric-exists"), ctx.String("metric-exists"))
	fmt.Printf("DEBUG: verbose set=%v, value=%v\n", ctx.IsSet("verbose"), verbose)

	// DEBUG: Check if any flags are set at all
	fmt.Printf("DEBUG: Any flags set? max-age=%v, max-consecutive-errors=%v, metric-exists=%v, verbose=%v\n",
		ctx.IsSet("max-age"), ctx.IsSet("max-consecutive-errors"), ctx.IsSet("metric-exists"), ctx.IsSet("verbose"))

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

	// DEBUG: Show which checks we're about to run
	fmt.Printf("DEBUG: About to check conditions...\n")

	// Check 1: Max age (if specified)
	if ctx.IsSet("max-age") {
		fmt.Printf("DEBUG: Running max-age check\n")
		maxAge := ctx.Duration("max-age")
		checkMaxAge(families, maxAge, &result, verbose)
	} else {
		fmt.Printf("DEBUG: Skipping max-age check (not set)\n")
	}

	// Check 2: Max consecutive errors (if specified)
	if ctx.IsSet("max-consecutive-errors") {
		fmt.Printf("DEBUG: Running max-consecutive-errors check\n")
		maxErrors := ctx.Int("max-consecutive-errors")
		if maxErrors >= 0 {
			checkConsecutiveErrors(families, maxErrors, &result, verbose)
		}
	} else {
		fmt.Printf("DEBUG: Skipping max-consecutive-errors check (not set)\n")
	}

	// Check 3: Metric exists (if specified)
	if ctx.IsSet("metric-exists") {
		fmt.Printf("DEBUG: Running metric-exists check\n")
		metricName := ctx.String("metric-exists")
		checkMetricExists(families, metricName, &result, verbose)
	} else {
		fmt.Printf("DEBUG: Skipping metric-exists check (not set)\n")
	}

	// If no specific checks were requested, do basic health check
	if !ctx.IsSet("max-age") && !ctx.IsSet("max-consecutive-errors") && !ctx.IsSet("metric-exists") {
		fmt.Printf("DEBUG: Running basic health check (no specific checks requested)\n")
		checkBasicHealth(families, &result, verbose)
	} else {
		fmt.Printf("DEBUG: Skipping basic health check (specific checks were requested)\n")
	}

	fmt.Printf("DEBUG: Final result.Healthy=%v\n", result.Healthy)

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
