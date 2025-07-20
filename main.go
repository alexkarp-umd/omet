package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/urfave/cli/v2"
)

// TimeProvider allows injecting time for testing
type TimeProvider interface {
	Now() time.Time
}

type RealTimeProvider struct{}

func (r RealTimeProvider) Now() time.Time {
	return time.Now()
}

// Global time provider (can be overridden in tests)
var timeProvider TimeProvider = RealTimeProvider{}

// FileLock represents a file lock with timeout
type FileLock struct {
	file    *os.File
	locked  bool
	timeout time.Duration
}

func NewFileLock(filename string, timeout time.Duration) (*FileLock, error) {
	file, err := os.OpenFile(filename, os.O_RDWR|os.O_CREATE, 0644)
	if err != nil {
		return nil, fmt.Errorf("failed to open file for locking: %w", err)
	}
	
	return &FileLock{
		file:    file,
		timeout: timeout,
	}, nil
}

func (fl *FileLock) Lock(ctx context.Context) error {
	if fl.locked {
		return fmt.Errorf("already locked")
	}
	
	// Create a context with timeout
	lockCtx, cancel := context.WithTimeout(ctx, fl.timeout)
	defer cancel()
	
	// Try to acquire lock with timeout
	done := make(chan error, 1)
	go func() {
		err := syscall.Flock(int(fl.file.Fd()), syscall.LOCK_EX)
		done <- err
	}()
	
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("failed to acquire lock: %w", err)
		}
		fl.locked = true
		return nil
	case <-lockCtx.Done():
		return fmt.Errorf("lock timeout after %v", fl.timeout)
	}
}

func (fl *FileLock) Unlock() error {
	if !fl.locked {
		return nil
	}
	
	err := syscall.Flock(int(fl.file.Fd()), syscall.LOCK_UN)
	if err != nil {
		return fmt.Errorf("failed to release lock: %w", err)
	}
	
	fl.locked = false
	return nil
}

func (fl *FileLock) Close() error {
	if fl.locked {
		fl.Unlock()
	}
	return fl.file.Close()
}

// ErrorCollector collects errors during operation for metrics
type ErrorCollector struct {
	errors []ErrorInfo
}

type ErrorInfo struct {
	err       error
	errorType string
}

func (ec *ErrorCollector) AddError(err error, errorType string) {
	ec.errors = append(ec.errors, ErrorInfo{err: err, errorType: errorType})
}

func (ec *ErrorCollector) HasErrors() bool {
	return len(ec.errors) > 0
}

func (ec *ErrorCollector) FirstError() error {
	if len(ec.errors) == 0 {
		return nil
	}
	return ec.errors[0].err
}

// Standard histogram buckets for response times (in seconds)
var defaultHistogramBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Lock wait histogram buckets (in seconds) - focused on sub-second to few-second waits
var lockWaitHistogramBuckets = []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}


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
			&cli.DurationFlag{
				Name:  "lock-timeout",
				Value: 30 * time.Second,
				Usage: "How long to wait for file lock",
			},
			&cli.BoolFlag{
				Name:  "no-lock",
				Usage: "Skip file locking (dangerous!)",
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
	errorCollector := &ErrorCollector{}
	var lockWaitTime time.Duration
	
	// Validate arguments
	if ctx.NArg() < 2 {
		return cli.ShowAppHelp(ctx)
	}

	metricName := ctx.Args().Get(0)
	operation := ctx.Args().Get(1)

	// Parse labels
	labels, err := parseLabels(ctx.StringSlice("label"))
	if err != nil {
		errorCollector.AddError(err, "invalid_args")
		if ctx.Bool("verbose") {
			log.Printf("Label parsing error: %v", err)
		}
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
			errorCollector.AddError(fmt.Errorf("invalid value '%s': %w", ctx.Args().Get(2), err), "invalid_args")
			value = 0 // Use default value
		} else {
			value = val
		}
	} else {
		// Read value from stdin or use default
		if operation == "inc" {
			value = 1.0 // Default increment
		} else {
			val, err := readValueFromStdin()
			if err != nil {
				errorCollector.AddError(fmt.Errorf("failed to read value from stdin: %w", err), "io_error")
				value = 0 // Use default value
			} else {
				value = val
			}
		}
	}

	if ctx.Bool("verbose") {
		log.Printf("Using value: %g", value)
	}

	// Determine if we should use file locking
	filename := ctx.String("file")
	useLocking := filename != "-" && !ctx.Bool("no-lock")
	
	var families map[string]*dto.MetricFamily
	var inputSize int64
	var lock *FileLock
	
	if useLocking {
		// Use file locking approach
		lockTimeout := ctx.Duration("lock-timeout")
		
		if ctx.Bool("verbose") {
			log.Printf("Acquiring lock on %s (timeout: %v)", filename, lockTimeout)
		}
		
		lock, err = NewFileLock(filename, lockTimeout)
		if err != nil {
			errorCollector.AddError(fmt.Errorf("failed to create file lock: %w", err), "io_error")
			families = make(map[string]*dto.MetricFamily)
		} else {
			defer lock.Close()
			
			// Measure lock wait time
			lockStart := time.Now()
			err = lock.Lock(context.Background())
			lockWaitTime = time.Since(lockStart)
			
			if err != nil {
				errorCollector.AddError(fmt.Errorf("failed to acquire lock: %w", err), "lock_error")
				families = make(map[string]*dto.MetricFamily)
			} else {
				defer lock.Unlock()
				
				if ctx.Bool("verbose") {
					log.Printf("Lock acquired in %v", lockWaitTime)
				}
				
				// Read and parse the locked file
				lock.file.Seek(0, 0) // Reset to beginning
				if stat, err := lock.file.Stat(); err == nil {
					inputSize = stat.Size()
				}
				
				parsedFamilies, err := parseMetrics(lock.file)
				if err != nil {
					errorCollector.AddError(fmt.Errorf("failed to parse metrics: %w", err), "parse_error")
					families = make(map[string]*dto.MetricFamily)
				} else {
					families = parsedFamilies
				}
			}
		}
	} else {
		// Use stdin/no-lock logic
		var input io.Reader
		if filename == "-" {
			input = os.Stdin
		} else {
			file, err := os.Open(filename)
			if err != nil {
				errorCollector.AddError(fmt.Errorf("failed to open file %s: %w", filename, err), "io_error")
				families = make(map[string]*dto.MetricFamily) // Start with empty metrics
			} else {
				defer file.Close()
				// Get file size for metrics
				if stat, err := file.Stat(); err == nil {
					inputSize = stat.Size()
				}
				input = file
			}
		}

		// Parse existing metrics (best effort)
		if input != nil {
			parsedFamilies, err := parseMetrics(input)
			if err != nil {
				errorCollector.AddError(fmt.Errorf("failed to parse metrics: %w", err), "parse_error")
				families = make(map[string]*dto.MetricFamily) // Start with empty metrics
			} else {
				families = parsedFamilies
			}
		}

		if families == nil {
			families = make(map[string]*dto.MetricFamily)
		}
	}

	if ctx.Bool("verbose") {
		log.Printf("Parsed %d metric families", len(families))
	}

	// Apply the operation (best effort)
	if !errorCollector.HasErrors() || (labels != nil && value != 0) {
		err = applyOperation(families, metricName, operation, labels, value)
		if err != nil {
			errorCollector.AddError(fmt.Errorf("failed to apply operation: %w", err), "operation_error")
		}
	}


	// Always try to write metrics (including error metrics)
	addErrorMetrics(families, errorCollector)
	addOperationalMetrics(families, operation, inputSize, lockWaitTime, errorCollector)
	
	// Write back to the locked file if using locking, otherwise to stdout
	if useLocking && lock != nil && lock.locked {
		// Truncate and write to the locked file
		lock.file.Seek(0, 0)
		lock.file.Truncate(0)
		err = writeMetricsWithSelfMonitoring(families, lock.file)
	} else {
		err = writeMetricsWithSelfMonitoring(families, os.Stdout)
	}
	
	if err != nil {
		// This is a critical error - we can't write output
		return fmt.Errorf("failed to write metrics: %w", err)
	}

	// Return first error for exit code, but after writing metrics
	if errorCollector.HasErrors() {
		return errorCollector.FirstError()
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
	return observeHistogramWithBuckets(families, name, labels, value, defaultHistogramBuckets)
}

func observeHistogramWithBuckets(families map[string]*dto.MetricFamily, name string, labels map[string]string, value float64, buckets []float64) error {
	family, err := getOrCreateFamily(families, name, dto.MetricType_HISTOGRAM)
	if err != nil {
		return err
	}

	metric := findOrCreateMetric(family, labels)

	// Initialize histogram if it doesn't exist
	if metric.Histogram == nil {
		metric.Histogram = createHistogram(buckets)
	}

	// Update sample count and sum
	currentCount := metric.Histogram.GetSampleCount()
	currentSum := metric.Histogram.GetSampleSum()

	metric.Histogram.SampleCount = uint64Ptr(currentCount + 1)
	metric.Histogram.SampleSum = float64Ptr(currentSum + value)

	// Update bucket counts
	for _, bucket := range metric.Histogram.Bucket {
		if value <= bucket.GetUpperBound() {
			currentBucketCount := bucket.GetCumulativeCount()
			bucket.CumulativeCount = uint64Ptr(currentBucketCount + 1)
		}
	}

	return nil
}

func createHistogram(buckets []float64) *dto.Histogram {
	var histogramBuckets []*dto.Bucket

	// Create buckets with the specified upper bounds
	for _, bound := range buckets {
		histogramBuckets = append(histogramBuckets, &dto.Bucket{
			UpperBound:      float64Ptr(bound),
			CumulativeCount: uint64Ptr(0),
		})
	}

	// Add +Inf bucket
	histogramBuckets = append(histogramBuckets, &dto.Bucket{
		UpperBound:      float64Ptr(math.Inf(1)),
		CumulativeCount: uint64Ptr(0),
	})

	return &dto.Histogram{
		SampleCount: uint64Ptr(0),
		SampleSum:   float64Ptr(0),
		Bucket:      histogramBuckets,
	}
}

func uint64Ptr(u uint64) *uint64 {
	return &u
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

// writeMetrics serializes metric families to text format (pure function)
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
			switch family.GetType() {
			case dto.MetricType_COUNTER:
				value := metric.GetCounter().GetValue()
				fmt.Fprintf(output, "%s%s %g\n", name, labelStr, value)
			case dto.MetricType_GAUGE:
				value := metric.GetGauge().GetValue()
				fmt.Fprintf(output, "%s%s %g\n", name, labelStr, value)
			case dto.MetricType_HISTOGRAM:
				histogram := metric.GetHistogram()

				// Write histogram buckets
				for _, bucket := range histogram.GetBucket() {
					bucketLabelStr := labelStr
					if len(labelParts) > 0 {
						bucketLabelStr = fmt.Sprintf("{%s,le=\"%g\"}", strings.Join(labelParts, ","), bucket.GetUpperBound())
					} else {
						bucketLabelStr = fmt.Sprintf("{le=\"%g\"}", bucket.GetUpperBound())
					}
					fmt.Fprintf(output, "%s_bucket%s %d\n", name, bucketLabelStr, bucket.GetCumulativeCount())
				}

				// Write count and sum
				fmt.Fprintf(output, "%s_count%s %d\n", name, labelStr, histogram.GetSampleCount())
				fmt.Fprintf(output, "%s_sum%s %g\n", name, labelStr, histogram.GetSampleSum())
			default:
				if metric.Untyped != nil {
					value := metric.GetUntyped().GetValue()
					fmt.Fprintf(output, "%s%s %g\n", name, labelStr, value)
				}
			}
		}
	}

	return nil
}

// writeMetricsWithSelfMonitoring adds self-monitoring metrics and writes output
func writeMetricsWithSelfMonitoring(families map[string]*dto.MetricFamily, output io.Writer) error {
	addSelfMonitoringMetrics(families)
	return writeMetrics(families, output)
}

func stringPtr(s string) *string {
	return &s
}

func float64Ptr(f float64) *float64 {
	return &f
}

func addSelfMonitoringMetrics(families map[string]*dto.MetricFamily) {
	// Add omet_last_write gauge with current timestamp
	lastWriteFamily, err := getOrCreateFamily(families, "omet_last_write", dto.MetricType_GAUGE)
	if err == nil {
		metric := findOrCreateMetric(lastWriteFamily, map[string]string{})
		currentTime := float64(timeProvider.Now().Unix())
		metric.Gauge = &dto.Gauge{Value: &currentTime}

		// Set help text if not already set
		if lastWriteFamily.Help == nil {
			lastWriteFamily.Help = stringPtr("Unix timestamp of last OMET write operation")
		}
	}

	// Add omet_modifications_total counter
	modificationsFamily, err := getOrCreateFamily(families, "omet_modifications_total", dto.MetricType_COUNTER)
	if err == nil {
		metric := findOrCreateMetric(modificationsFamily, map[string]string{})

		// Initialize or increment counter
		if metric.Counter == nil {
			metric.Counter = &dto.Counter{Value: float64Ptr(1.0)}
		} else {
			currentValue := metric.Counter.GetValue()
			metric.Counter.Value = float64Ptr(currentValue + 1.0)
		}

		// Set help text if not already set
		if modificationsFamily.Help == nil {
			modificationsFamily.Help = stringPtr("Total number of OMET modification operations")
		}
	}
}

func addErrorMetrics(families map[string]*dto.MetricFamily, errorCollector *ErrorCollector) {
	if !errorCollector.HasErrors() {
		return
	}

	// Add omet_errors_total counter with error type labels
	errorsFamily, err := getOrCreateFamily(families, "omet_errors_total", dto.MetricType_COUNTER)
	if err != nil {
		return // Can't add error metrics if we can't create the family
	}

	// Set custom help text (override the generic one)
	errorsFamily.Help = stringPtr("Total number of OMET errors by type")

	// Count errors by type
	errorCounts := make(map[string]int)
	for _, errorInfo := range errorCollector.errors {
		errorCounts[errorInfo.errorType]++
	}

	// Add/increment counter for each error type
	for errorType, count := range errorCounts {
		labels := map[string]string{"type": errorType}
		metric := findOrCreateMetric(errorsFamily, labels)

		if metric.Counter == nil {
			metric.Counter = &dto.Counter{Value: float64Ptr(float64(count))}
		} else {
			currentValue := metric.Counter.GetValue()
			metric.Counter.Value = float64Ptr(currentValue + float64(count))
		}
	}
}

func addOperationalMetrics(families map[string]*dto.MetricFamily, operation string, inputSize int64, lockWaitTime time.Duration, errorCollector *ErrorCollector) {
	// Add omet_operations_by_type_total counter
	opsFamily, err := getOrCreateFamily(families, "omet_operations_by_type_total", dto.MetricType_COUNTER)
	if err == nil {
		opsFamily.Help = stringPtr("Total number of OMET operations by type")
		labels := map[string]string{"operation": operation}
		metric := findOrCreateMetric(opsFamily, labels)

		if metric.Counter == nil {
			metric.Counter = &dto.Counter{Value: float64Ptr(1.0)}
		} else {
			currentValue := metric.Counter.GetValue()
			metric.Counter.Value = float64Ptr(currentValue + 1.0)
		}
	}

	// Add omet_input_bytes_total counter (only if we have input size)
	if inputSize > 0 {
		inputFamily, err := getOrCreateFamily(families, "omet_input_bytes_total", dto.MetricType_COUNTER)
		if err == nil {
			inputFamily.Help = stringPtr("Total bytes read from input files")
			metric := findOrCreateMetric(inputFamily, map[string]string{})

			if metric.Counter == nil {
				metric.Counter = &dto.Counter{Value: float64Ptr(float64(inputSize))}
			} else {
				currentValue := metric.Counter.GetValue()
				metric.Counter.Value = float64Ptr(currentValue + float64(inputSize))
			}
		}
	}


	// Add omet_consecutive_errors_total gauge
	consecutiveErrorsFamily, err := getOrCreateFamily(families, "omet_consecutive_errors_total", dto.MetricType_GAUGE)
	if err == nil {
		consecutiveErrorsFamily.Help = stringPtr("Number of consecutive failed OMET runs (resets on success)")
		metric := findOrCreateMetric(consecutiveErrorsFamily, map[string]string{})
		
		// Get existing consecutive error count (from previous runs)
		existingCount := 0.0
		if metric.Gauge != nil {
			existingCount = metric.Gauge.GetValue()
		}
		
		// If this run had errors, increment consecutive count
		// If this run was successful, reset to 0
		var newCount float64
		if errorCollector.HasErrors() {
			newCount = existingCount + 1.0
		} else {
			newCount = 0.0
		}
		
		metric.Gauge = &dto.Gauge{Value: &newCount}
	}

	// Add omet_lock_wait_seconds histogram (only if we actually waited for a lock)
	if lockWaitTime > 0 {
		lockWaitFamily, err := getOrCreateFamily(families, "omet_lock_wait_seconds", dto.MetricType_HISTOGRAM)
		if err == nil {
			lockWaitFamily.Help = stringPtr("Time spent waiting for file locks in seconds")
			lockWaitSeconds := lockWaitTime.Seconds()
			err := observeHistogramWithBuckets(families, "omet_lock_wait_seconds", map[string]string{}, lockWaitSeconds, lockWaitHistogramBuckets)
			if err != nil {
				// Log error but continue - don't let lock metrics break the operation
			}
		}
	}
}
