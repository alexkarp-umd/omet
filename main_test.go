package main

import (
	"testing"

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
				assert.Equal(t, 0.3, metric.Histogram.GetSampleSum())
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
