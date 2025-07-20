package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckMaxAge(t *testing.T) {
	tests := []struct {
		name           string
		timestamp      int64
		maxAge         time.Duration
		expectHealthy  bool
		expectMessage  string
	}{
		{
			name:          "recent timestamp passes",
			timestamp:     time.Now().Unix() - 60, // 1 minute ago
			maxAge:        5 * time.Minute,
			expectHealthy: true,
		},
		{
			name:          "old timestamp fails",
			timestamp:     time.Now().Unix() - 600, // 10 minutes ago
			maxAge:        5 * time.Minute,
			expectHealthy: false,
			expectMessage: "Last write too old",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			families := createTestGaugeFamily("omet_last_write", float64(tt.timestamp))
			
			result := HealthCheckResult{
				Healthy: true,
				Checks:  make(map[string]CheckResult),
			}

			checkMaxAge(families, tt.maxAge, &result, false)

			assert.Equal(t, tt.expectHealthy, result.Healthy)
			check, exists := result.Checks["max_age"]
			require.True(t, exists)
			assert.Equal(t, tt.expectHealthy, check.Passed)
			
			if tt.expectMessage != "" {
				assert.Contains(t, check.Message, tt.expectMessage)
			}
		})
	}
}

func TestCheckConsecutiveErrors(t *testing.T) {
	tests := []struct {
		name           string
		errorCount     *float64 // nil means no metric
		maxErrors      int
		expectHealthy  bool
		expectMessage  string
	}{
		{
			name:          "no metric is healthy",
			errorCount:    nil,
			maxErrors:     5,
			expectHealthy: true,
			expectMessage: "No consecutive errors metric found",
		},
		{
			name:          "low error count passes",
			errorCount:    float64Ptr(2),
			maxErrors:     5,
			expectHealthy: true,
			expectMessage: "Consecutive errors OK",
		},
		{
			name:          "high error count fails",
			errorCount:    float64Ptr(10),
			maxErrors:     5,
			expectHealthy: false,
			expectMessage: "Too many consecutive errors",
		},
		{
			name:          "zero errors passes",
			errorCount:    float64Ptr(0),
			maxErrors:     5,
			expectHealthy: true,
			expectMessage: "Consecutive errors OK",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var families map[string]*dto.MetricFamily
			if tt.errorCount != nil {
				families = createTestGaugeFamily("omet_consecutive_errors_total", *tt.errorCount)
			} else {
				families = make(map[string]*dto.MetricFamily)
			}
			
			result := HealthCheckResult{
				Healthy: true,
				Checks:  make(map[string]CheckResult),
			}

			checkConsecutiveErrors(families, tt.maxErrors, &result, false)

			assert.Equal(t, tt.expectHealthy, result.Healthy)
			check, exists := result.Checks["consecutive_errors"]
			require.True(t, exists)
			assert.Equal(t, tt.expectHealthy, check.Passed)
			assert.Contains(t, check.Message, tt.expectMessage)
		})
	}
}

func TestCheckMetricExists(t *testing.T) {
	tests := []struct {
		name           string
		metricName     string
		metricsExist   []string
		expectHealthy  bool
		expectMessage  string
	}{
		{
			name:          "existing metric passes",
			metricName:    "test_counter",
			metricsExist:  []string{"test_counter", "other_metric"},
			expectHealthy: true,
			expectMessage: "Metric 'test_counter' found",
		},
		{
			name:          "missing metric fails",
			metricName:    "missing_metric",
			metricsExist:  []string{"test_counter", "other_metric"},
			expectHealthy: false,
			expectMessage: "Metric 'missing_metric' not found",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			families := make(map[string]*dto.MetricFamily)
			for _, name := range tt.metricsExist {
				testFamilies := createTestCounterFamily(name, 1.0)
				families[name] = testFamilies[name]
			}
			
			result := HealthCheckResult{
				Healthy: true,
				Checks:  make(map[string]CheckResult),
			}

			checkMetricExists(families, tt.metricName, &result, false)

			assert.Equal(t, tt.expectHealthy, result.Healthy)
			check, exists := result.Checks["metric_exists"]
			require.True(t, exists)
			assert.Equal(t, tt.expectHealthy, check.Passed)
			assert.Contains(t, check.Message, tt.expectMessage)
			
			// Should include list of found metrics
			assert.Len(t, result.MetricsFound, len(tt.metricsExist))
		})
	}
}

func TestCheckBasicHealth(t *testing.T) {
	tests := []struct {
		name           string
		families       map[string]*dto.MetricFamily
		expectHealthy  bool
		expectMessage  string
	}{
		{
			name:          "empty metrics fails",
			families:      make(map[string]*dto.MetricFamily),
			expectHealthy: false,
			expectMessage: "No metrics found in file",
		},
		{
			name: "metrics without omet_last_write fails",
			families: func() map[string]*dto.MetricFamily {
				return createTestCounterFamily("some_metric", 1.0)
			}(),
			expectHealthy: false,
			expectMessage: "omet_last_write metric not found",
		},
		{
			name: "metrics with omet_last_write passes",
			families: func() map[string]*dto.MetricFamily {
				families := createTestCounterFamily("some_metric", 1.0)
				gaugeFamilies := createTestGaugeFamily("omet_last_write", float64(time.Now().Unix()))
				families["omet_last_write"] = gaugeFamilies["omet_last_write"]
				return families
			}(),
			expectHealthy: true,
			expectMessage: "Basic health OK",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := HealthCheckResult{
				Healthy: true,
				Checks:  make(map[string]CheckResult),
			}

			checkBasicHealth(tt.families, &result, false)

			assert.Equal(t, tt.expectHealthy, result.Healthy)
			check, exists := result.Checks["basic_health"]
			require.True(t, exists)
			assert.Equal(t, tt.expectHealthy, check.Passed)
			assert.Contains(t, check.Message, tt.expectMessage)
		})
	}
}

func TestOutputJSON(t *testing.T) {
	result := HealthCheckResult{
		Healthy: false,
		Checks: map[string]CheckResult{
			"max_age": {
				Passed:  false,
				Message: "Last write too old: 10m0s (max: 5m0s)",
				Value:   "10m0s",
			},
			"consecutive_errors": {
				Passed:  true,
				Message: "Consecutive errors OK: 0 (max: 5)",
				Value:   "0",
			},
		},
		LastWriteTimestamp: int64Ptr(1234567890),
		ConsecutiveErrors:  float64Ptr(0),
		MetricsFound:       []string{"omet_last_write", "test_counter"},
	}

	// Capture output
	var buf bytes.Buffer
	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	go func() {
		defer w.Close()
		outputJSON(&result)
	}()

	var output bytes.Buffer
	output.ReadFrom(r)
	os.Stdout = oldStdout

	jsonStr := output.String()

	// Verify JSON structure (basic checks)
	assert.Contains(t, jsonStr, `"healthy":false`)
	assert.Contains(t, jsonStr, `"max_age"`)
	assert.Contains(t, jsonStr, `"consecutive_errors"`)
	assert.Contains(t, jsonStr, `"last_write_timestamp":1234567890`)
	assert.Contains(t, jsonStr, `"consecutive_errors":0`)
	assert.Contains(t, jsonStr, `"metrics_found"`)
	assert.Contains(t, jsonStr, `"omet_last_write"`)
	assert.Contains(t, jsonStr, `"test_counter"`)
}

func TestOutputText(t *testing.T) {
	tests := []struct {
		name           string
		result         HealthCheckResult
		verbose        bool
		expectContains []string
	}{
		{
			name: "healthy output",
			result: HealthCheckResult{
				Healthy: true,
				Checks: map[string]CheckResult{
					"basic_health": {Passed: true, Message: "All good"},
				},
			},
			verbose:        false,
			expectContains: []string{"HEALTHY"},
		},
		{
			name: "unhealthy output",
			result: HealthCheckResult{
				Healthy: false,
				Checks: map[string]CheckResult{
					"max_age": {Passed: false, Message: "Too old"},
					"consecutive_errors": {Passed: true, Message: "OK"},
				},
			},
			verbose:        true,
			expectContains: []string{"UNHEALTHY", "max_age: Too old"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Capture output
			var buf bytes.Buffer
			oldStdout := os.Stdout
			r, w, _ := os.Pipe()
			os.Stdout = w

			go func() {
				defer w.Close()
				outputText(&tt.result, tt.verbose)
			}()

			var output bytes.Buffer
			output.ReadFrom(r)
			os.Stdout = oldStdout

			outputStr := output.String()

			for _, expected := range tt.expectContains {
				assert.Contains(t, outputStr, expected)
			}
		})
	}
}

func TestParseMetrics(t *testing.T) {
	metricsContent := `# HELP test_counter A test counter
# TYPE test_counter counter
test_counter 42
test_counter{label="value"} 10

# HELP omet_last_write Last write timestamp
# TYPE omet_last_write gauge
omet_last_write 1234567890
`

	families, err := parseMetrics(strings.NewReader(metricsContent))
	require.NoError(t, err)

	assert.Len(t, families, 2)
	assert.Contains(t, families, "test_counter")
	assert.Contains(t, families, "omet_last_write")

	// Verify counter
	counterFamily := families["test_counter"]
	assert.Equal(t, dto.MetricType_COUNTER, counterFamily.GetType())
	assert.Len(t, counterFamily.Metric, 2)

	// Verify gauge
	gaugeFamily := families["omet_last_write"]
	assert.Equal(t, dto.MetricType_GAUGE, gaugeFamily.GetType())
	assert.Len(t, gaugeFamily.Metric, 1)
	assert.Equal(t, 1234567890.0, gaugeFamily.Metric[0].GetGauge().GetValue())
}

// Helper functions (reuse from main_test.go)
func createTestCounterFamily(name string, value float64) map[string]*dto.MetricFamily {
	families := make(map[string]*dto.MetricFamily)
	metricType := dto.MetricType_COUNTER
	families[name] = &dto.MetricFamily{
		Name: &name,
		Type: &metricType,
		Help: stringPtr("Test counter"),
		Metric: []*dto.Metric{
			{
				Counter: &dto.Counter{Value: &value},
			},
		},
	}
	return families
}

func createTestGaugeFamily(name string, value float64) map[string]*dto.MetricFamily {
	families := make(map[string]*dto.MetricFamily)
	metricType := dto.MetricType_GAUGE
	families[name] = &dto.MetricFamily{
		Name: &name,
		Type: &metricType,
		Help: stringPtr("Test gauge"),
		Metric: []*dto.Metric{
			{
				Gauge: &dto.Gauge{Value: &value},
			},
		},
	}
	return families
}

func stringPtr(s string) *string {
	return &s
}

func float64Ptr(f float64) *float64 {
	return &f
}

func int64Ptr(i int64) *int64 {
	return &i
}
