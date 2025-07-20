package main

import (
	"bytes"
	"fmt"
	"testing"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseLabels(t *testing.T) {
	tests := []struct {
		name        string
		input       []string
		expected    map[string]string
		expectError bool
	}{
		{
			name:     "empty labels",
			input:    []string{},
			expected: map[string]string{},
		},
		{
			name:     "single label",
			input:    []string{"key=value"},
			expected: map[string]string{"key": "value"},
		},
		{
			name:     "multiple labels",
			input:    []string{"env=prod", "region=us-east", "service=api"},
			expected: map[string]string{"env": "prod", "region": "us-east", "service": "api"},
		},
		{
			name:        "invalid format - no equals",
			input:       []string{"invalid"},
			expectError: true,
		},
		{
			name:     "value with equals sign",
			input:    []string{"key=value=extra"},
			expected: map[string]string{"key": "value=extra"},
		},
		{
			name:     "empty value",
			input:    []string{"key="},
			expected: map[string]string{"key": ""},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := parseLabels(tt.input)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestReadValueFromStdin(t *testing.T) {
	tests := []struct {
		name        string
		input       string
		expected    float64
		expectError bool
	}{
		{
			name:     "valid integer",
			input:    "42\n",
			expected: 42.0,
		},
		{
			name:     "valid float",
			input:    "3.14159\n",
			expected: 3.14159,
		},
		{
			name:     "valid negative",
			input:    "-123.45\n",
			expected: -123.45,
		},
		{
			name:     "whitespace trimmed",
			input:    "  100  \n",
			expected: 100.0,
		},
		{
			name:        "invalid text",
			input:       "not_a_number\n",
			expectError: true,
		},
		{
			name:        "empty input",
			input:       "",
			expectError: true,
		},
		{
			name:        "only whitespace",
			input:       "   \n",
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanup := mockStdin(t, tt.input)
			defer cleanup()

			result, err := readValueFromStdin()

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, result)
			}
		})
	}
}

func TestApplyOperation(t *testing.T) {
	tests := []struct {
		name        string
		operation   string
		expectError bool
		errorMsg    string
	}{
		{
			name:      "valid inc operation",
			operation: "inc",
		},
		{
			name:      "valid set operation",
			operation: "set",
		},
		{
			name:      "valid observe operation",
			operation: "observe",
		},
		{
			name:        "invalid operation",
			operation:   "invalid",
			expectError: true,
			errorMsg:    "unknown operation",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			families := make(map[string]*dto.MetricFamily)
			labels := map[string]string{}

			err := applyOperation(families, "test_metric", tt.operation, labels, 1.0)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorMsg != "" {
					assert.Contains(t, err.Error(), tt.errorMsg)
				}
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestIncrementCounter(t *testing.T) {
	tests := []struct {
		name        string
		families    map[string]*dto.MetricFamily
		metricName  string
		labels      map[string]string
		increment   float64
		expectError bool
		validate    func(t *testing.T, families map[string]*dto.MetricFamily)
	}{
		{
			name:       "create new counter",
			families:   make(map[string]*dto.MetricFamily),
			metricName: "test_counter",
			labels:     map[string]string{},
			increment:  1.0,
			validate: func(t *testing.T, families map[string]*dto.MetricFamily) {
				family, exists := families["test_counter"]
				require.True(t, exists)
				assert.Equal(t, dto.MetricType_COUNTER, family.GetType())
				assert.Len(t, family.Metric, 1)
				assert.Equal(t, 1.0, family.Metric[0].GetCounter().GetValue())
			},
		},
		{
			name:       "increment existing counter",
			families:   createTestCounterFamily("test_counter", 5.0),
			metricName: "test_counter",
			labels:     map[string]string{},
			increment:  3.0,
			validate: func(t *testing.T, families map[string]*dto.MetricFamily) {
				family := families["test_counter"]
				assert.Equal(t, 8.0, family.Metric[0].GetCounter().GetValue())
			},
		},
		{
			name:       "increment with labels",
			families:   make(map[string]*dto.MetricFamily),
			metricName: "test_counter",
			labels:     map[string]string{"service": "api", "env": "prod"},
			increment:  2.0,
			validate: func(t *testing.T, families map[string]*dto.MetricFamily) {
				family := families["test_counter"]
				metric := family.Metric[0]
				assert.Equal(t, 2.0, metric.GetCounter().GetValue())
				assert.Len(t, metric.Label, 2)
			},
		},
		{
			name:        "error on gauge type",
			families:    createTestGaugeFamily("test_gauge", 10.0),
			metricName:  "test_gauge",
			labels:      map[string]string{},
			increment:   1.0,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := incrementCounter(tt.families, tt.metricName, tt.labels, tt.increment)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, tt.families)
				}
			}
		})
	}
}

func TestSetGauge(t *testing.T) {
	tests := []struct {
		name        string
		families    map[string]*dto.MetricFamily
		metricName  string
		labels      map[string]string
		value       float64
		expectError bool
		validate    func(t *testing.T, families map[string]*dto.MetricFamily)
	}{
		{
			name:       "create new gauge",
			families:   make(map[string]*dto.MetricFamily),
			metricName: "test_gauge",
			labels:     map[string]string{},
			value:      42.5,
			validate: func(t *testing.T, families map[string]*dto.MetricFamily) {
				family, exists := families["test_gauge"]
				require.True(t, exists)
				assert.Equal(t, dto.MetricType_GAUGE, family.GetType())
				assert.Len(t, family.Metric, 1)
				assert.Equal(t, 42.5, family.Metric[0].GetGauge().GetValue())
			},
		},
		{
			name:       "update existing gauge",
			families:   createTestGaugeFamily("test_gauge", 10.0),
			metricName: "test_gauge",
			labels:     map[string]string{},
			value:      99.9,
			validate: func(t *testing.T, families map[string]*dto.MetricFamily) {
				family := families["test_gauge"]
				assert.Equal(t, 99.9, family.Metric[0].GetGauge().GetValue())
			},
		},
		{
			name:        "error on counter type",
			families:    createTestCounterFamily("test_counter", 5.0),
			metricName:  "test_counter",
			labels:      map[string]string{},
			value:       10.0,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := setGauge(tt.families, tt.metricName, tt.labels, tt.value)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, tt.families)
				}
			}
		})
	}
}

func TestObserveHistogram(t *testing.T) {
	tests := []struct {
		name        string
		families    map[string]*dto.MetricFamily
		metricName  string
		labels      map[string]string
		value       float64
		expectError bool
		validate    func(t *testing.T, families map[string]*dto.MetricFamily)
	}{
		{
			name:       "create new histogram with single observation",
			families:   make(map[string]*dto.MetricFamily),
			metricName: "response_time_seconds",
			labels:     map[string]string{},
			value:      0.123,
			validate: func(t *testing.T, families map[string]*dto.MetricFamily) {
				family, exists := families["response_time_seconds"]
				require.True(t, exists, "histogram family should be created")
				assert.Equal(t, dto.MetricType_HISTOGRAM, family.GetType())

				// Should have one metric
				require.Len(t, family.Metric, 1)
				metric := family.Metric[0]

				// Should have histogram data
				require.NotNil(t, metric.Histogram, "metric should have histogram data")

				// Should have count = 1 (one observation)
				assert.Equal(t, uint64(1), metric.Histogram.GetSampleCount())

				// Should have sum = 0.123 (the observed value)
				assert.Equal(t, 0.123, metric.Histogram.GetSampleSum())

				// Should have buckets (at least the +Inf bucket)
				buckets := metric.Histogram.GetBucket()
				require.NotEmpty(t, buckets, "should have at least one bucket")

				// The +Inf bucket should have count = 1
				infBucket := buckets[len(buckets)-1]
				assert.True(t, infBucket.GetUpperBound() > 1e10, "last bucket should be +Inf")
				assert.Equal(t, uint64(1), infBucket.GetCumulativeCount())
			},
		},
		{
			name:       "add observation to existing histogram",
			families:   createTestHistogramFamily("response_time_seconds", []float64{0.1}, []uint64{1}, 1, 0.1),
			metricName: "response_time_seconds",
			labels:     map[string]string{},
			value:      0.2,
			validate: func(t *testing.T, families map[string]*dto.MetricFamily) {
				family := families["response_time_seconds"]
				metric := family.Metric[0]

				// Should have count = 2 (original 1 + new 1)
				assert.Equal(t, uint64(2), metric.Histogram.GetSampleCount())

				// Should have sum = 0.3 (original 0.1 + new 0.2)
				assert.InDelta(t, 0.3, metric.Histogram.GetSampleSum(), 1e-10)
			},
		},
		{
			name:       "observe with labels",
			families:   make(map[string]*dto.MetricFamily),
			metricName: "request_duration",
			labels:     map[string]string{"method": "GET", "status": "200"},
			value:      0.05,
			validate: func(t *testing.T, families map[string]*dto.MetricFamily) {
				family := families["request_duration"]
				metric := family.Metric[0]

				// Should have the correct labels
				assert.Len(t, metric.Label, 2)

				// Should have histogram data
				assert.Equal(t, uint64(1), metric.Histogram.GetSampleCount())
				assert.Equal(t, 0.05, metric.Histogram.GetSampleSum())
			},
		},
		{
			name:        "error on counter type",
			families:    createTestCounterFamily("test_counter", 5.0),
			metricName:  "test_counter",
			labels:      map[string]string{},
			value:       0.123,
			expectError: true,
		},
		{
			name:        "error on gauge type",
			families:    createTestGaugeFamily("test_gauge", 10.0),
			metricName:  "test_gauge",
			labels:      map[string]string{},
			value:       0.123,
			expectError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := observeHistogram(tt.families, tt.metricName, tt.labels, tt.value)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				if tt.validate != nil {
					tt.validate(t, tt.families)
				}
			}
		})
	}
}

func TestLabelsMatch(t *testing.T) {
	tests := []struct {
		name           string
		existingLabels []*dto.LabelPair
		newLabels      map[string]string
		expected       bool
	}{
		{
			name:           "empty labels match",
			existingLabels: []*dto.LabelPair{},
			newLabels:      map[string]string{},
			expected:       true,
		},
		{
			name: "single label match",
			existingLabels: []*dto.LabelPair{
				{Name: stringPtr("key"), Value: stringPtr("value")},
			},
			newLabels: map[string]string{"key": "value"},
			expected:  true,
		},
		{
			name: "single label mismatch",
			existingLabels: []*dto.LabelPair{
				{Name: stringPtr("key"), Value: stringPtr("value1")},
			},
			newLabels: map[string]string{"key": "value2"},
			expected:  false,
		},
		{
			name: "different number of labels",
			existingLabels: []*dto.LabelPair{
				{Name: stringPtr("key1"), Value: stringPtr("value1")},
			},
			newLabels: map[string]string{"key1": "value1", "key2": "value2"},
			expected:  false,
		},
		{
			name: "multiple labels match",
			existingLabels: []*dto.LabelPair{
				{Name: stringPtr("env"), Value: stringPtr("prod")},
				{Name: stringPtr("service"), Value: stringPtr("api")},
			},
			newLabels: map[string]string{"env": "prod", "service": "api"},
			expected:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := labelsMatch(tt.existingLabels, tt.newLabels)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFindOrCreateMetric(t *testing.T) {
	// Test finding existing metric
	existingFamily := createTestCounterFamily("test_counter", 10.0)["test_counter"]

	// Add labels to the existing metric
	existingFamily.Metric[0].Label = []*dto.LabelPair{
		{Name: stringPtr("env"), Value: stringPtr("prod")},
	}

	// Should find the existing metric
	foundMetric := findOrCreateMetric(existingFamily, map[string]string{"env": "prod"})
	assert.Equal(t, existingFamily.Metric[0], foundMetric)
	assert.Len(t, existingFamily.Metric, 1) // Should not create a new one

	// Should create a new metric with different labels
	newMetric := findOrCreateMetric(existingFamily, map[string]string{"env": "dev"})
	assert.NotEqual(t, existingFamily.Metric[0], newMetric)
	assert.Len(t, existingFamily.Metric, 2) // Should have created a new one
}

func TestCreateLabelPairs(t *testing.T) {
	tests := []struct {
		name     string
		input    map[string]string
		expected int // number of label pairs
	}{
		{
			name:     "empty labels",
			input:    map[string]string{},
			expected: 0,
		},
		{
			name:     "single label",
			input:    map[string]string{"key": "value"},
			expected: 1,
		},
		{
			name:     "multiple labels",
			input:    map[string]string{"env": "prod", "service": "api", "region": "us-east"},
			expected: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := createLabelPairs(tt.input)
			assert.Len(t, result, tt.expected)

			// Verify all labels are present
			labelMap := make(map[string]string)
			for _, pair := range result {
				labelMap[pair.GetName()] = pair.GetValue()
			}
			assert.Equal(t, tt.input, labelMap)
		})
	}
}

// Helper functions for creating test metric families
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

func createTestHistogramFamily(name string, bucketBounds []float64, bucketCounts []uint64, sampleCount uint64, sampleSum float64) map[string]*dto.MetricFamily {
	families := make(map[string]*dto.MetricFamily)
	metricType := dto.MetricType_HISTOGRAM

	// Create buckets
	var buckets []*dto.Bucket
	for i, bound := range bucketBounds {
		buckets = append(buckets, &dto.Bucket{
			UpperBound:      &bound,
			CumulativeCount: &bucketCounts[i],
		})
	}

	// Add +Inf bucket
	infBound := float64(1e10) // Represents +Inf
	buckets = append(buckets, &dto.Bucket{
		UpperBound:      &infBound,
		CumulativeCount: &sampleCount,
	})

	families[name] = &dto.MetricFamily{
		Name: &name,
		Type: &metricType,
		Help: stringPtr("Test histogram"),
		Metric: []*dto.Metric{
			{
				Histogram: &dto.Histogram{
					SampleCount: &sampleCount,
					SampleSum:   &sampleSum,
					Bucket:      buckets,
				},
			},
		},
	}
	return families
}

func TestMetricRoundTrip(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		value     float64
	}{
		{"counter", "inc", 5.0},
		{"gauge", "set", 42.5},
		{"histogram", "observe", 0.123},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			families := make(map[string]*dto.MetricFamily)

			// Apply operation
			err := applyOperation(families, "test_metric", tt.operation, map[string]string{}, tt.value)
			require.NoError(t, err)

			// Verify we can write it (this would have caught the bug!)
			var buf bytes.Buffer
			err = writeMetrics(families, &buf)
			require.NoError(t, err)

			// Basic sanity check
			output := buf.String()
			assert.Contains(t, output, "test_metric")
			assert.NotEmpty(t, output)
		})
	}
}

func TestHistogramDebug(t *testing.T) {
	t.Run("single observation debug", func(t *testing.T) {
		families := make(map[string]*dto.MetricFamily)

		// Add one observation
		err := observeHistogram(families, "response_time", map[string]string{"service": "web-api"}, 0.25)
		require.NoError(t, err)

		// Debug: Print the internal structure
		family := families["response_time"]
		metric := family.Metric[0]
		histogram := metric.Histogram

		t.Logf("Sample Count: %d", histogram.GetSampleCount())
		t.Logf("Sample Sum: %g", histogram.GetSampleSum())
		t.Logf("Number of buckets: %d", len(histogram.Bucket))

		for i, bucket := range histogram.Bucket {
			t.Logf("Bucket %d: le=%g, count=%d", i, bucket.GetUpperBound(), bucket.GetCumulativeCount())
		}

		// Test serialization
		var buf bytes.Buffer
		err = writeMetrics(families, &buf)
		require.NoError(t, err)

		output := buf.String()
		t.Logf("Serialized output:\n%s", output)

		// Verify we get the expected output
		assert.Contains(t, output, "response_time_count")
		assert.Contains(t, output, "response_time_sum")
		assert.Contains(t, output, "response_time_bucket")
	})

	t.Run("multiple observations debug", func(t *testing.T) {
		families := make(map[string]*dto.MetricFamily)

		// Add multiple observations
		values := []float64{0.25, 100, 1000}
		for _, val := range values {
			err := observeHistogram(families, "response_time", map[string]string{"service": "web-api"}, val)
			require.NoError(t, err)
		}

		// Debug internal state
		family := families["response_time"]
		metric := family.Metric[0]
		histogram := metric.Histogram

		t.Logf("After %d observations:", len(values))
		t.Logf("Sample Count: %d", histogram.GetSampleCount())
		t.Logf("Sample Sum: %g", histogram.GetSampleSum())

		expectedSum := 0.25 + 100 + 1000 // = 1100.25
		assert.Equal(t, uint64(3), histogram.GetSampleCount())
		assert.InDelta(t, expectedSum, histogram.GetSampleSum(), 1e-10)

		// Check bucket distribution
		for i, bucket := range histogram.Bucket {
			t.Logf("Bucket %d: le=%g, count=%d", i, bucket.GetUpperBound(), bucket.GetCumulativeCount())
		}

		// Test serialization
		var buf bytes.Buffer
		err := writeMetrics(families, &buf)
		require.NoError(t, err)

		output := buf.String()
		t.Logf("Serialized output:\n%s", output)
	})
}

func TestAddOperationalMetrics(t *testing.T) {
	t.Run("adds operation type counter", func(t *testing.T) {
		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		
		addOperationalMetrics(families, "inc", 1024, time.Second, collector)
		
		// Verify operations counter was created
		require.Contains(t, families, "omet_operations_by_type_total")
		opsFamily := families["omet_operations_by_type_total"]
		assert.Equal(t, dto.MetricType_COUNTER, opsFamily.GetType())
		assert.Equal(t, "Total number of OMET operations by type", opsFamily.GetHelp())
		
		// Should have one metric with operation=inc label
		assert.Len(t, opsFamily.Metric, 1)
		metric := opsFamily.Metric[0]
		assert.Len(t, metric.Label, 1)
		assert.Equal(t, "operation", metric.Label[0].GetName())
		assert.Equal(t, "inc", metric.Label[0].GetValue())
		assert.Equal(t, 1.0, metric.GetCounter().GetValue())
	})
	
	t.Run("increments existing operation counter", func(t *testing.T) {
		// Start with existing operations counter
		families := createTestCounterFamily("omet_operations_by_type_total", 5.0)
		opsFamily := families["omet_operations_by_type_total"]
		
		// Add operation label to existing metric
		opsFamily.Metric[0].Label = []*dto.LabelPair{
			{Name: stringPtr("operation"), Value: stringPtr("set")},
		}
		
		collector := &ErrorCollector{}
		addOperationalMetrics(families, "set", 2048, time.Minute, collector)
		
		// Should increment existing counter
		assert.Equal(t, 6.0, opsFamily.Metric[0].GetCounter().GetValue())
	})
	
	t.Run("adds input bytes counter when size > 0", func(t *testing.T) {
		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		
		addOperationalMetrics(families, "observe", 4096, time.Millisecond*500, collector)
		
		// Verify input bytes counter was created
		require.Contains(t, families, "omet_input_bytes_total")
		inputFamily := families["omet_input_bytes_total"]
		assert.Equal(t, dto.MetricType_COUNTER, inputFamily.GetType())
		assert.Equal(t, "Total bytes read from input files", inputFamily.GetHelp())
		assert.Equal(t, 4096.0, inputFamily.Metric[0].GetCounter().GetValue())
	})
	
	t.Run("skips input bytes counter when size is 0", func(t *testing.T) {
		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		
		addOperationalMetrics(families, "inc", 0, time.Second, collector)
		
		// Should not create input bytes counter
		assert.NotContains(t, families, "omet_input_bytes_total")
	})
	
	t.Run("adds process duration gauge", func(t *testing.T) {
		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		duration := time.Millisecond * 1500 // 1.5 seconds
		
		addOperationalMetrics(families, "set", 1024, duration, collector)
		
		// Verify duration gauge was created
		require.Contains(t, families, "omet_process_duration_seconds")
		durationFamily := families["omet_process_duration_seconds"]
		assert.Equal(t, dto.MetricType_GAUGE, durationFamily.GetType())
		assert.Equal(t, "Duration of the last OMET operation in seconds", durationFamily.GetHelp())
		assert.Equal(t, 1.5, durationFamily.Metric[0].GetGauge().GetValue())
	})
	
	t.Run("adds consecutive errors gauge for failed run", func(t *testing.T) {
		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		
		// Add some errors (this run failed)
		collector.AddError(fmt.Errorf("error 1"), "type1")
		collector.AddError(fmt.Errorf("error 2"), "type2")
		
		addOperationalMetrics(families, "inc", 512, time.Second, collector)
		
		// Verify consecutive errors gauge was created
		require.Contains(t, families, "omet_consecutive_errors_total")
		errorsFamily := families["omet_consecutive_errors_total"]
		assert.Equal(t, dto.MetricType_GAUGE, errorsFamily.GetType())
		assert.Equal(t, "Number of consecutive failed OMET runs (resets on success)", errorsFamily.GetHelp())
		assert.Equal(t, 1.0, errorsFamily.Metric[0].GetGauge().GetValue())
	})
	
	t.Run("increments consecutive errors from existing count", func(t *testing.T) {
		// Start with existing consecutive errors (from previous runs)
		families := createTestGaugeFamily("omet_consecutive_errors_total", 2.0)
		collector := &ErrorCollector{}
		
		// This run also failed
		collector.AddError(fmt.Errorf("error 1"), "type1")
		
		addOperationalMetrics(families, "inc", 256, time.Second, collector)
		
		// Should increment to 3 (2 + 1)
		errorsFamily := families["omet_consecutive_errors_total"]
		assert.Equal(t, 3.0, errorsFamily.Metric[0].GetGauge().GetValue())
	})
	
	t.Run("resets consecutive errors on successful run", func(t *testing.T) {
		// Start with existing consecutive errors (from previous runs)
		families := createTestGaugeFamily("omet_consecutive_errors_total", 5.0)
		collector := &ErrorCollector{}
		
		// This run was successful (no errors)
		
		addOperationalMetrics(families, "inc", 256, time.Second, collector)
		
		// Should reset to 0
		errorsFamily := families["omet_consecutive_errors_total"]
		assert.Equal(t, 0.0, errorsFamily.Metric[0].GetGauge().GetValue())
	})
}

func TestOperationalMetricsIntegration(t *testing.T) {
	t.Run("operational metrics appear in output", func(t *testing.T) {
		// Use mock time for deterministic testing
		mockTime := time.Date(2024, 5, 1, 10, 30, 0, 0, time.UTC)
		setupMockTime(t, mockTime)

		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		
		// Add a regular metric operation
		err := incrementCounter(families, "test_counter", map[string]string{"env": "test"}, 5.0)
		require.NoError(t, err)
		
		// Add operational metrics
		duration := time.Millisecond * 750 // 0.75 seconds
		addOperationalMetrics(families, "inc", 2048, duration, collector)

		var buf bytes.Buffer
		err = writeMetricsWithSelfMonitoring(families, &buf)
		require.NoError(t, err)

		output := buf.String()

		// Verify operational metrics appear in output
		assert.Contains(t, output, "# HELP omet_operations_by_type_total", "should include operations counter help")
		assert.Contains(t, output, "# TYPE omet_operations_by_type_total counter", "should include operations counter type")
		assert.Contains(t, output, `omet_operations_by_type_total{operation="inc"} 1`, "should include operation count")
		
		assert.Contains(t, output, "# HELP omet_input_bytes_total", "should include input bytes help")
		assert.Contains(t, output, "omet_input_bytes_total 2048", "should include input bytes count")
		
		assert.Contains(t, output, "# HELP omet_process_duration_seconds", "should include duration help")
		assert.Contains(t, output, "omet_process_duration_seconds 0.75", "should include duration value")
		
		assert.Contains(t, output, "# HELP omet_consecutive_errors_total", "should include consecutive errors help")
		assert.Contains(t, output, "omet_consecutive_errors_total 0", "should show zero consecutive errors")
		
		// Verify self-monitoring metrics are still there
		assert.Contains(t, output, "omet_modifications_total", "should include modifications counter")
		assert.Contains(t, output, "omet_last_write", "should include last write timestamp")
	})
	
	t.Run("consecutive errors tracked across runs", func(t *testing.T) {
		mockTime := time.Date(2024, 6, 1, 15, 0, 0, 0, time.UTC)
		setupMockTime(t, mockTime)

		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		
		// Simulate multiple errors in this run
		collector.AddError(fmt.Errorf("parse error"), "parse_error")
		collector.AddError(fmt.Errorf("io error"), "io_error")
		collector.AddError(fmt.Errorf("operation error"), "operation_error")
		
		addOperationalMetrics(families, "set", 1024, time.Second, collector)
		addErrorMetrics(families, collector)

		var buf bytes.Buffer
		err := writeMetricsWithSelfMonitoring(families, &buf)
		require.NoError(t, err)

		output := buf.String()

		// Should show 1 consecutive error (this run failed, regardless of how many individual errors)
		assert.Contains(t, output, "omet_consecutive_errors_total 1", "should track consecutive failed runs")
		
		// Should also have error breakdown by type
		assert.Contains(t, output, `omet_errors_total{type="parse_error"} 1`, "should count parse errors")
		assert.Contains(t, output, `omet_errors_total{type="io_error"} 1`, "should count io errors")
		assert.Contains(t, output, `omet_errors_total{type="operation_error"} 1`, "should count operation errors")
	})
}

func TestErrorCollector(t *testing.T) {
	t.Run("collects and categorizes errors", func(t *testing.T) {
		collector := &ErrorCollector{}
		
		// Add different types of errors
		collector.AddError(fmt.Errorf("invalid argument"), "invalid_args")
		collector.AddError(fmt.Errorf("file not found"), "io_error")
		collector.AddError(fmt.Errorf("parse failed"), "parse_error")
		collector.AddError(fmt.Errorf("another invalid arg"), "invalid_args")
		
		assert.True(t, collector.HasErrors())
		assert.Len(t, collector.errors, 4)
		assert.Equal(t, "invalid argument", collector.FirstError().Error())
		assert.Len(t, collector.errors, 4)
	})
	
	t.Run("handles no errors", func(t *testing.T) {
		collector := &ErrorCollector{}
		
		assert.False(t, collector.HasErrors())
		assert.Nil(t, collector.FirstError())
	})
	
}

func TestAddErrorMetrics(t *testing.T) {
	t.Run("adds error metrics with type labels", func(t *testing.T) {
		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		
		// Add various error types
		collector.AddError(fmt.Errorf("bad arg"), "invalid_args")
		collector.AddError(fmt.Errorf("bad arg 2"), "invalid_args") 
		collector.AddError(fmt.Errorf("io failed"), "io_error")
		collector.AddError(fmt.Errorf("parse failed"), "parse_error")
		
		addErrorMetrics(families, collector)
		
		// Verify error family was created
		require.Contains(t, families, "omet_errors_total")
		errorFamily := families["omet_errors_total"]
		assert.Equal(t, dto.MetricType_COUNTER, errorFamily.GetType())
		assert.Equal(t, "Total number of OMET errors by type", errorFamily.GetHelp())
		
		// Should have 3 metrics (one per error type)
		assert.Len(t, errorFamily.Metric, 3)
		
		// Check error counts by type
		errorCounts := make(map[string]float64)
		for _, metric := range errorFamily.Metric {
			var errorType string
			for _, label := range metric.Label {
				if label.GetName() == "type" {
					errorType = label.GetValue()
					break
				}
			}
			errorCounts[errorType] = metric.GetCounter().GetValue()
		}
		
		assert.Equal(t, 2.0, errorCounts["invalid_args"], "should have 2 invalid_args errors")
		assert.Equal(t, 1.0, errorCounts["io_error"], "should have 1 io_error")
		assert.Equal(t, 1.0, errorCounts["parse_error"], "should have 1 parse_error")
	})
	
	t.Run("increments existing error metrics", func(t *testing.T) {
		// Start with existing error metrics
		families := createTestCounterFamily("omet_errors_total", 5.0)
		errorFamily := families["omet_errors_total"]
		
		// Add type label to existing metric
		errorFamily.Metric[0].Label = []*dto.LabelPair{
			{Name: stringPtr("type"), Value: stringPtr("invalid_args")},
		}
		
		collector := &ErrorCollector{}
		collector.AddError(fmt.Errorf("another bad arg"), "invalid_args")
		collector.AddError(fmt.Errorf("new error type"), "operation_error")
		
		addErrorMetrics(families, collector)
		
		// Should now have 2 metrics
		assert.Len(t, errorFamily.Metric, 2)
		
		// Find the invalid_args metric and verify it was incremented
		var invalidArgsCount, operationErrorCount float64
		for _, metric := range errorFamily.Metric {
			for _, label := range metric.Label {
				if label.GetName() == "type" {
					if label.GetValue() == "invalid_args" {
						invalidArgsCount = metric.GetCounter().GetValue()
					} else if label.GetValue() == "operation_error" {
						operationErrorCount = metric.GetCounter().GetValue()
					}
				}
			}
		}
		
		assert.Equal(t, 6.0, invalidArgsCount, "should increment existing invalid_args counter")
		assert.Equal(t, 1.0, operationErrorCount, "should create new operation_error counter")
	})
	
	t.Run("does nothing when no errors", func(t *testing.T) {
		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		
		addErrorMetrics(families, collector)
		
		// Should not create error metrics family
		assert.NotContains(t, families, "omet_errors_total")
	})
}

func TestErrorHandlingIntegration(t *testing.T) {
	t.Run("invalid operation adds error metric but continues", func(t *testing.T) {
		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		
		// This should fail
		err := applyOperation(families, "test_metric", "invalid_operation", map[string]string{}, 1.0)
		assert.Error(t, err)
		collector.AddError(err, "operation_error")
		
		// Add error metrics
		addErrorMetrics(families, collector)
		
		// Verify error metric was added
		require.Contains(t, families, "omet_errors_total")
		errorFamily := families["omet_errors_total"]
		assert.Len(t, errorFamily.Metric, 1)
		
		// Check the error type label
		metric := errorFamily.Metric[0]
		assert.Len(t, metric.Label, 1)
		assert.Equal(t, "type", metric.Label[0].GetName())
		assert.Equal(t, "operation_error", metric.Label[0].GetValue())
		assert.Equal(t, 1.0, metric.GetCounter().GetValue())
	})
	
	t.Run("type mismatch adds error metric", func(t *testing.T) {
		// Start with a counter
		families := createTestCounterFamily("test_counter", 5.0)
		collector := &ErrorCollector{}
		
		// Try to set it as a gauge (should fail)
		err := setGauge(families, "test_counter", map[string]string{}, 10.0)
		assert.Error(t, err)
		collector.AddError(err, "operation_error")
		
		// Add error metrics
		addErrorMetrics(families, collector)
		
		// Should have both the original counter and the error metric
		assert.Contains(t, families, "test_counter")
		assert.Contains(t, families, "omet_errors_total")
		
		// Verify error metric
		errorFamily := families["omet_errors_total"]
		assert.Equal(t, 1.0, errorFamily.Metric[0].GetCounter().GetValue())
	})
}

func TestErrorResilienceIntegration(t *testing.T) {
	t.Run("invalid label format still produces error metrics", func(t *testing.T) {
		// Create a valid metrics file
		testContent := `# HELP test_counter A test counter
# TYPE test_counter counter
test_counter 10
`
		testFile := createTempFile(t, testContent)
		
		// Create app and run with invalid label format
		app := createTestApp()
		
		output := captureOutput(t, func() {
			// This should fail due to invalid label format but still produce output
			err := app.Run([]string{"omet", "-f", testFile, "-l", "foobar", "test_counter", "inc", "1"})
			// We expect this to fail, but we want output anyway
			assert.Error(t, err, "should return error for invalid label format")
		})
		
		// Verify we got output despite the error
		assert.NotEmpty(t, output, "should produce output even with invalid labels")
		
		// Verify error metrics appear in output
		assert.Contains(t, output, "omet_errors_total", "should include error metrics")
		assert.Contains(t, output, `omet_errors_total{type="invalid_args"}`, "should categorize label parsing error")
		
		// Verify original metrics are preserved
		assert.Contains(t, output, "test_counter 10", "should preserve original metrics")
		
		// Verify self-monitoring metrics
		assert.Contains(t, output, "omet_modifications_total", "should include modification counter")
		assert.Contains(t, output, "omet_last_write", "should include last write timestamp")
	})
	
	t.Run("multiple error types are all captured", func(t *testing.T) {
		// Create a valid metrics file
		testContent := `# HELP existing_gauge A test gauge
# TYPE existing_gauge gauge
existing_gauge 42.5
`
		testFile := createTempFile(t, testContent)
		
		app := createTestApp()
		
		output := captureOutput(t, func() {
			// Multiple errors: invalid label + type mismatch
			err := app.Run([]string{"omet", "-f", testFile, "-l", "invalid_label", "existing_gauge", "inc", "1"})
			assert.Error(t, err, "should return error")
		})
		
		// Should have both error types
		assert.Contains(t, output, "omet_errors_total", "should include error metrics")
		// Note: We might see both invalid_args and operation_error
		
		// Should still preserve original metrics
		assert.Contains(t, output, "existing_gauge 42.5", "should preserve original gauge")
	})
	
	t.Run("file not found still produces error output", func(t *testing.T) {
		app := createTestApp()
		
		output := captureOutput(t, func() {
			// File doesn't exist, but we should still get error metrics
			err := app.Run([]string{"omet", "-f", "/nonexistent/file.txt", "test_metric", "set", "100"})
			assert.Error(t, err, "should return error for missing file")
		})
		
		// Should produce output with error metrics
		assert.NotEmpty(t, output, "should produce output even when file missing")
		assert.Contains(t, output, "omet_errors_total", "should include error metrics")
		assert.Contains(t, output, `omet_errors_total{type="io_error"}`, "should categorize file error")
		
		// Should still create the requested metric
		assert.Contains(t, output, "test_metric 100", "should create requested metric despite file error")
	})
}

func TestSelfMonitoringMetrics(t *testing.T) {
	t.Run("adds self-monitoring metrics on write", func(t *testing.T) {
		// Use mock time for deterministic testing
		mockTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
		setupMockTime(t, mockTime)

		families := make(map[string]*dto.MetricFamily)

		// Add a regular metric
		err := incrementCounter(families, "test_counter", map[string]string{"env": "test"}, 1.0)
		require.NoError(t, err)

		// Write metrics (this should add self-monitoring metrics)
		var buf bytes.Buffer
		err = writeMetricsWithSelfMonitoring(families, &buf)
		require.NoError(t, err)

		// Verify self-monitoring metrics were added
		assert.Contains(t, families, "omet_last_write", "should add omet_last_write metric")
		assert.Contains(t, families, "omet_modifications_total", "should add omet_modifications_total metric")

		// Verify omet_last_write is a gauge with expected timestamp
		lastWriteFamily := families["omet_last_write"]
		assert.Equal(t, dto.MetricType_GAUGE, lastWriteFamily.GetType())
		assert.Len(t, lastWriteFamily.Metric, 1)

		timestamp := int64(lastWriteFamily.Metric[0].GetGauge().GetValue())
		expectedTimestamp := mockTime.Unix()
		assert.Equal(t, expectedTimestamp, timestamp, "timestamp should match mock time")

		// Verify omet_modifications_total is a counter starting at 1
		modificationsFamily := families["omet_modifications_total"]
		assert.Equal(t, dto.MetricType_COUNTER, modificationsFamily.GetType())
		assert.Len(t, modificationsFamily.Metric, 1)

		count := modificationsFamily.Metric[0].GetCounter().GetValue()
		assert.Equal(t, 1.0, count, "should start at 1 for first modification")
	})

	t.Run("increments modification counter on subsequent writes", func(t *testing.T) {
		// Use mock time for deterministic testing
		mockTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
		mockProvider := setupMockTime(t, mockTime)

		families := make(map[string]*dto.MetricFamily)

		// First write
		err := incrementCounter(families, "test_counter", map[string]string{}, 1.0)
		require.NoError(t, err)

		var buf1 bytes.Buffer
		err = writeMetricsWithSelfMonitoring(families, &buf1)
		require.NoError(t, err)

		// Advance time and do second write
		mockProvider.SetTime(mockTime.Add(5 * time.Minute))

		err = incrementCounter(families, "test_counter", map[string]string{}, 1.0)
		require.NoError(t, err)

		var buf2 bytes.Buffer
		err = writeMetricsWithSelfMonitoring(families, &buf2)
		require.NoError(t, err)

		// Verify counter incremented
		modificationsFamily := families["omet_modifications_total"]
		count := modificationsFamily.Metric[0].GetCounter().GetValue()
		assert.Equal(t, 2.0, count, "should increment to 2 after second write")

		// Verify timestamp updated to new time
		lastWriteFamily := families["omet_last_write"]
		timestamp := int64(lastWriteFamily.Metric[0].GetGauge().GetValue())
		expectedTimestamp := mockTime.Add(5 * time.Minute).Unix()
		assert.Equal(t, expectedTimestamp, timestamp, "timestamp should be updated to new mock time")
	})

	t.Run("preserves existing self-monitoring metrics", func(t *testing.T) {
		// Use mock time for deterministic testing
		mockTime := time.Date(2024, 2, 1, 15, 30, 0, 0, time.UTC)
		setupMockTime(t, mockTime)

		// Start with existing self-monitoring metrics (simulating file read)
		families := createTestCounterFamily("omet_modifications_total", 42.0)
		gaugeFamily := createTestGaugeFamily("omet_last_write", 1234567890.0)
		families["omet_last_write"] = gaugeFamily["omet_last_write"]

		// Add a regular metric
		err := setGauge(families, "test_gauge", map[string]string{}, 100.0)
		require.NoError(t, err)

		// Write metrics
		var buf bytes.Buffer
		err = writeMetricsWithSelfMonitoring(families, &buf)
		require.NoError(t, err)

		// Verify existing counter was incremented (not reset)
		modificationsFamily := families["omet_modifications_total"]
		count := modificationsFamily.Metric[0].GetCounter().GetValue()
		assert.Equal(t, 43.0, count, "should increment existing counter")

		// Verify timestamp was updated to mock time
		lastWriteFamily := families["omet_last_write"]
		timestamp := int64(lastWriteFamily.Metric[0].GetGauge().GetValue())
		expectedTimestamp := mockTime.Unix()
		assert.Equal(t, expectedTimestamp, timestamp, "should update to mock time")
	})

	t.Run("self-monitoring metrics appear in output", func(t *testing.T) {
		// Use mock time for deterministic testing
		mockTime := time.Date(2024, 3, 1, 9, 15, 30, 0, time.UTC)
		setupMockTime(t, mockTime)

		families := make(map[string]*dto.MetricFamily)

		// Add a metric and write
		err := observeHistogram(families, "response_time", map[string]string{"service": "api"}, 0.123)
		require.NoError(t, err)

		var buf bytes.Buffer
		err = writeMetricsWithSelfMonitoring(families, &buf)
		require.NoError(t, err)

		output := buf.String()

		// Verify self-monitoring metrics appear in output
		assert.Contains(t, output, "# HELP omet_last_write", "should include help for omet_last_write")
		assert.Contains(t, output, "# TYPE omet_last_write gauge", "should include type for omet_last_write")
		assert.Contains(t, output, "omet_last_write ", "should include omet_last_write value")

		assert.Contains(t, output, "# HELP omet_modifications_total", "should include help for omet_modifications_total")
		assert.Contains(t, output, "# TYPE omet_modifications_total counter", "should include type for omet_modifications_total")
		assert.Contains(t, output, "omet_modifications_total ", "should include omet_modifications_total value")

		// Verify timestamp appears in output (allow scientific notation)
		expectedTimestamp := mockTime.Unix()
		expectedTimestampFloat := float64(expectedTimestamp)
		assert.Contains(t, output, fmt.Sprintf("omet_last_write %g", expectedTimestampFloat), "should include mock timestamp in correct format")
	})
	
	t.Run("error metrics appear in output with self-monitoring", func(t *testing.T) {
		// Use mock time for deterministic testing
		mockTime := time.Date(2024, 4, 1, 14, 30, 0, 0, time.UTC)
		setupMockTime(t, mockTime)

		families := make(map[string]*dto.MetricFamily)
		collector := &ErrorCollector{}
		
		// Add some errors
		collector.AddError(fmt.Errorf("invalid operation"), "operation_error")
		collector.AddError(fmt.Errorf("parse failed"), "parse_error")
		
		// Add error metrics
		addErrorMetrics(families, collector)

		var buf bytes.Buffer
		err := writeMetricsWithSelfMonitoring(families, &buf)
		require.NoError(t, err)

		output := buf.String()

		// Verify error metrics appear in output
		assert.Contains(t, output, "# HELP omet_errors_total", "should include help for omet_errors_total")
		assert.Contains(t, output, "# TYPE omet_errors_total counter", "should include type for omet_errors_total")
		assert.Contains(t, output, "omet_errors_total{type=\"operation_error\"} 1", "should include operation_error count")
		assert.Contains(t, output, "omet_errors_total{type=\"parse_error\"} 1", "should include parse_error count")
		
		// Verify self-monitoring metrics are still there
		assert.Contains(t, output, "omet_modifications_total 1", "should include modifications counter")
		assert.Contains(t, output, "omet_last_write", "should include last write timestamp")
	})
}
