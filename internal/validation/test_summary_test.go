package validation

import (
	"math"
	"testing"
)

func TestComputeTestSummary_EmptyData(t *testing.T) {
	t.Parallel()

	summary := ComputeTestSummary(nil, 40, 4096)

	if summary == nil {
		t.Fatal("expected non-nil TestSummary for empty data")
	}

	// Empty data should return zero/default values
	if summary.FreqPvalue != 0 {
		t.Errorf("expected FreqPvalue=0 for empty data, got %f", summary.FreqPvalue)
	}
	if summary.RunsPvalue != 0 {
		t.Errorf("expected RunsPvalue=0 for empty data, got %f", summary.RunsPvalue)
	}
}

func TestComputeTestSummary_ValidBalancedData(t *testing.T) {
	t.Parallel()

	// Create perfectly balanced data: alternating bits pattern
	// 0x55 = 01010101, 0xAA = 10101010
	data := make([]byte, 256)
	for i := 0; i < 256; i++ {
		if i%2 == 0 {
			data[i] = 0x55
		} else {
			data[i] = 0xAA
		}
	}

	rctWindow := uint32(40)
	aptWindow := uint32(256)
	summary := ComputeTestSummary(data, rctWindow, aptWindow)

	if summary == nil {
		t.Fatal("expected non-nil TestSummary")
	}

	// Balanced data should have reasonable p-values
	if summary.FreqPvalue < 0.05 {
		t.Errorf("expected FreqPvalue >= 0.05 for balanced data, got %f", summary.FreqPvalue)
	}

	// Note: Alternating pattern (0x55/0xAA) has very low runs p-value
	// This is expected behavior for this specific pattern
	if summary.RunsPvalue < 0 || summary.RunsPvalue > 1 {
		t.Errorf("expected RunsPvalue in [0,1], got %f", summary.RunsPvalue)
	}

	// Verify RCT and APT stats are present
	if summary.RctStat == nil {
		t.Error("expected RctStat to be set")
	} else if *summary.RctStat < 0 {
		t.Errorf("expected RctStat >= 0, got %f", *summary.RctStat)
	}

	if summary.AptStat == nil {
		t.Error("expected AptStat to be set")
	} else if *summary.AptStat < 0 || *summary.AptStat > 1 {
		t.Errorf("expected AptStat in [0,1], got %f", *summary.AptStat)
	}

	// Verify window parameters are preserved
	if summary.RctWindow == nil || *summary.RctWindow != rctWindow {
		t.Errorf("expected RctWindow=%d, got %v", rctWindow, summary.RctWindow)
	}
	if summary.AptWindow == nil || *summary.AptWindow != aptWindow {
		t.Errorf("expected AptWindow=%d, got %v", aptWindow, summary.AptWindow)
	}

	t.Logf("Balanced data: FreqPvalue=%.6f, RunsPvalue=%.6f, RctStat=%.2f, AptStat=%.4f",
		summary.FreqPvalue, summary.RunsPvalue, *summary.RctStat, *summary.AptStat)
}

func TestComputeTestSummary_BiasedData(t *testing.T) {
	t.Parallel()

	// Create highly biased data: 90% ones
	data := make([]byte, 100)
	for i := 0; i < 100; i++ {
		data[i] = 0xFF // All bits set
	}

	summary := ComputeTestSummary(data, 40, 100)

	if summary == nil {
		t.Fatal("expected non-nil TestSummary")
	}

	// Biased data should have low p-values (close to 0.0)
	if summary.FreqPvalue > 0.05 {
		t.Errorf("expected FreqPvalue < 0.05 for biased data, got %f", summary.FreqPvalue)
	}

	// RCT stat should be high (many repetitions)
	if summary.RctStat == nil {
		t.Error("expected RctStat to be set")
	} else if *summary.RctStat < 10 {
		t.Errorf("expected RctStat >= 10 for biased data, got %f", *summary.RctStat)
	}

	// APT stat should be high (high proportion of same value)
	if summary.AptStat == nil {
		t.Error("expected AptStat to be set")
	} else if *summary.AptStat < 0.8 {
		t.Errorf("expected AptStat >= 0.8 for biased data, got %f", *summary.AptStat)
	}

	t.Logf("Biased data: FreqPvalue=%.6f, RunsPvalue=%.6f, RctStat=%.2f, AptStat=%.4f",
		summary.FreqPvalue, summary.RunsPvalue, *summary.RctStat, *summary.AptStat)
}

func TestComputeTestSummary_SingleByte(t *testing.T) {
	t.Parallel()

	data := []byte{0x42}
	summary := ComputeTestSummary(data, 40, 4096)

	if summary == nil {
		t.Fatal("expected non-nil TestSummary")
	}

	// Single byte should produce valid statistics
	if summary.FreqPvalue < 0 || summary.FreqPvalue > 1 {
		t.Errorf("expected FreqPvalue in [0,1], got %f", summary.FreqPvalue)
	}

	if summary.RctStat == nil {
		t.Error("expected RctStat to be set")
	}
	if summary.AptStat == nil {
		t.Error("expected AptStat to be set")
	}
}

func TestComputeTestSummary_UniformDistribution(t *testing.T) {
	t.Parallel()

	// Create uniform distribution: cycle through all byte values
	data := make([]byte, 1024)
	for i := 0; i < 1024; i++ {
		data[i] = byte(i % 256)
	}

	summary := ComputeTestSummary(data, 40, 512)

	if summary == nil {
		t.Fatal("expected non-nil TestSummary")
	}

	// Uniform data should have high p-values
	if summary.FreqPvalue < 0.1 {
		t.Errorf("expected FreqPvalue >= 0.1 for uniform data, got %f", summary.FreqPvalue)
	}

	// APT stat should be low (no value dominates)
	if summary.AptStat == nil {
		t.Error("expected AptStat to be set")
	} else if *summary.AptStat > 0.1 {
		t.Errorf("expected AptStat <= 0.1 for uniform data, got %f", *summary.AptStat)
	}

	t.Logf("Uniform data: FreqPvalue=%.6f, RunsPvalue=%.6f, AptStat=%.4f",
		summary.FreqPvalue, summary.RunsPvalue, *summary.AptStat)
}

func TestComputeFrequencyPValue_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		data        []byte
		ratio       float64
		expectLowP  bool // true if expecting p-value < 0.05
		expectHighP bool // true if expecting p-value > 0.95
		expectMidP  bool // true if expecting p-value in [0.05, 0.95]
	}{
		{
			name:        "PerfectBalance",
			data:        []byte{0x55, 0xAA}, // 50% ones
			ratio:       0.5,
			expectHighP: true,
		},
		{
			name:       "AllOnes",
			data:       []byte{0xFF, 0xFF, 0xFF, 0xFF},
			ratio:      1.0,
			expectLowP: true,
		},
		{
			name:       "AllZeros",
			data:       []byte{0x00, 0x00, 0x00, 0x00},
			ratio:      0.0,
			expectLowP: true,
		},
		{
			name: "SlightBias_60Percent",
			data: func() []byte {
				d := make([]byte, 100)
				// Create data with 60% ones (600 bits out of 800)
				for i := 0; i < 75; i++ {
					d[i] = 0xFF // 600 bits = 75 bytes
				}
				for i := 75; i < 100; i++ {
					d[i] = 0x00 // 200 bits = 25 bytes
				}
				return d
			}(),
			ratio:      0.75, // 75 out of 100 bytes = 0.75
			expectLowP: true, // This is significantly biased
		},
		{
			name: "HighBias_90Percent",
			data: func() []byte {
				d := make([]byte, 100)
				// Create data with 90% ones (720 bits out of 800)
				for i := 0; i < 90; i++ {
					d[i] = 0xFF // 720 bits = 90 bytes
				}
				for i := 90; i < 100; i++ {
					d[i] = 0x00 // 80 bits = 10 bytes
				}
				return d
			}(),
			ratio:      0.9,
			expectLowP: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pValue := computeFrequencyPValue(tc.data, tc.ratio)

			// Verify p-value is in valid range [0, 1]
			if pValue < 0 || pValue > 1 {
				t.Errorf("p-value out of range [0,1]: got %f", pValue)
			}

			// Check expectations
			if tc.expectLowP && pValue >= 0.05 {
				t.Errorf("expected low p-value (< 0.05), got %f", pValue)
			}
			if tc.expectHighP && pValue <= 0.95 {
				t.Errorf("expected high p-value (> 0.95), got %f", pValue)
			}
			if tc.expectMidP && (pValue < 0.05 || pValue > 0.95) {
				t.Errorf("expected mid-range p-value [0.05, 0.95], got %f", pValue)
			}

			t.Logf("%s: ratio=%.2f, p-value=%.6f", tc.name, tc.ratio, pValue)
		})
	}
}

func TestComputeFrequencyPValue_EmptyData(t *testing.T) {
	t.Parallel()

	pValue := computeFrequencyPValue([]byte{}, 0.5)

	// Empty data causes division by zero, expect NaN
	if !math.IsNaN(pValue) && !math.IsInf(pValue, 0) {
		t.Logf("Note: empty data produces p-value=%f", pValue)
	}
	// Test passes as long as function doesn't crash
}

func TestComputeFrequencyPValue_SingleBit(t *testing.T) {
	t.Parallel()

	// Single bit samples
	tests := []struct {
		name  string
		data  []byte
		ratio float64
	}{
		{"OneBitSet", []byte{0x01}, 0.125}, // 1 bit set out of 8
		{"AllBitsSet", []byte{0xFF}, 1.0},  // 8 bits set out of 8
		{"HalfBitsSet", []byte{0x0F}, 0.5}, // 4 bits set out of 8
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pValue := computeFrequencyPValue(tc.data, tc.ratio)

			if pValue < 0 || pValue > 1 {
				t.Errorf("p-value out of range: got %f", pValue)
			}

			t.Logf("%s: ratio=%.3f, p-value=%.6f", tc.name, tc.ratio, pValue)
		})
	}
}

func TestComputeFrequencyPValue_LargeDataset(t *testing.T) {
	t.Parallel()

	// Large dataset with perfect balance
	data := make([]byte, 10000)
	for i := 0; i < 10000; i++ {
		if i%2 == 0 {
			data[i] = 0x55 // 01010101
		} else {
			data[i] = 0xAA // 10101010
		}
	}

	pValue := computeFrequencyPValue(data, 0.5)

	// With large N and perfect balance, p-value should be very high
	if pValue < 0.95 {
		t.Errorf("expected high p-value for large balanced dataset, got %f", pValue)
	}

	t.Logf("Large balanced dataset (n=10000): p-value=%.6f", pValue)
}

func TestComputeRunsPValue_TableDriven(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		data        []byte
		runs        int
		expectLowP  bool
		expectHighP bool
	}{
		{
			name:        "NormalRuns",
			data:        make([]byte, 100),
			runs:        400, // ~n/2 for 800 bits
			expectHighP: true,
		},
		{
			name:       "VeryFewRuns",
			data:       make([]byte, 100),
			runs:       10, // Much less than expected
			expectLowP: true,
		},
		{
			name:       "TooManyRuns",
			data:       make([]byte, 100),
			runs:       700, // Much more than expected
			expectLowP: true,
		},
		{
			name:       "SingleByte",
			data:       []byte{0x55}, // 01010101 - 7 runs
			runs:       7,
			expectLowP: false, // Small n makes p-value sensitive
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pValue := computeRunsPValue(tc.data, tc.runs)

			// Verify p-value is in valid range
			if pValue < 0 || pValue > 1 {
				t.Errorf("p-value out of range [0,1]: got %f", pValue)
			}

			if tc.expectLowP && pValue >= 0.05 {
				t.Errorf("expected low p-value (< 0.05), got %f", pValue)
			}
			if tc.expectHighP && pValue <= 0.05 {
				t.Errorf("expected high p-value (> 0.05), got %f", pValue)
			}

			t.Logf("%s: runs=%d (n=%d bits), p-value=%.6f",
				tc.name, tc.runs, len(tc.data)*8, pValue)
		})
	}
}

func TestComputeRunsPValue_EmptyData(t *testing.T) {
	t.Parallel()

	pValue := computeRunsPValue([]byte{}, 0)

	// Empty data causes division by zero, expect NaN
	if !math.IsNaN(pValue) && !math.IsInf(pValue, 0) {
		t.Logf("Note: empty data produces p-value=%f", pValue)
	}
	// Test passes as long as function doesn't crash
}

func TestComputeRunsPValue_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		data []byte
		runs int
	}{
		{"ZeroRuns", []byte{0x00}, 0},
		{"OneRun", []byte{0xFF}, 1},
		{"MaxRuns", []byte{0xAA}, 8}, // Alternating bits = maximum runs
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			pValue := computeRunsPValue(tc.data, tc.runs)

			if pValue < 0 || pValue > 1 {
				t.Errorf("p-value out of range: got %f", pValue)
			}

			t.Logf("%s: runs=%d, p-value=%.6f", tc.name, tc.runs, pValue)
		})
	}
}

// computeRCTStat Tests

func TestComputeRCTStat_NoRepetitions(t *testing.T) {
	t.Parallel()

	// All unique values
	data := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x05}
	stat := computeRCTStat(data, 100)

	// No repetitions = stat should be 1
	if stat != 1 {
		t.Errorf("expected RCT stat=1 for no repetitions, got %f", stat)
	}

	t.Logf("No repetitions: RCT stat=%.0f", stat)
}

func TestComputeRCTStat_WithRepetitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		data         []byte
		window       uint32
		expectedStat float64
	}{
		{
			name:         "TwoConsecutive",
			data:         []byte{0x00, 0x00, 0x01, 0x02},
			window:       10,
			expectedStat: 2,
		},
		{
			name:         "FiveConsecutive",
			data:         []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0x00},
			window:       10,
			expectedStat: 5,
		},
		{
			name:         "AllSame",
			data:         []byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA},
			window:       10,
			expectedStat: 7,
		},
		{
			name:         "MultipleRuns_MaxIsThree",
			data:         []byte{0x11, 0x11, 0x11, 0x22, 0x33, 0x33, 0x44},
			window:       10,
			expectedStat: 3, // Maximum consecutive run is 3
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stat := computeRCTStat(tc.data, tc.window)

			if stat != tc.expectedStat {
				t.Errorf("expected RCT stat=%.0f, got %.0f", tc.expectedStat, stat)
			}

			t.Logf("%s: RCT stat=%.0f", tc.name, stat)
		})
	}
}

func TestComputeRCTStat_WindowBoundary(t *testing.T) {
	t.Parallel()

	// Create data with 10 consecutive 0xFF, then different values
	data := make([]byte, 20)
	for i := 0; i < 10; i++ {
		data[i] = 0xFF
	}
	for i := 10; i < 20; i++ {
		data[i] = byte(i)
	}

	// Window smaller than repetition run
	stat := computeRCTStat(data, 5)
	if stat != 5 {
		t.Errorf("expected RCT stat=5 (window limit), got %f", stat)
	}

	// Window larger than repetition run
	stat = computeRCTStat(data, 15)
	if stat != 10 {
		t.Errorf("expected RCT stat=10 (actual repetitions), got %f", stat)
	}

	// Window larger than data length
	stat = computeRCTStat(data, 100)
	if stat != 10 {
		t.Errorf("expected RCT stat=10 (data limit), got %f", stat)
	}

	t.Logf("Window boundary tests passed")
}

func TestComputeRCTStat_EmptyData(t *testing.T) {
	t.Parallel()

	stat := computeRCTStat(nil, 100)
	if stat != 0 {
		t.Errorf("expected RCT stat=0 for empty data, got %f", stat)
	}

	stat = computeRCTStat([]byte{}, 100)
	if stat != 0 {
		t.Errorf("expected RCT stat=0 for empty slice, got %f", stat)
	}
}

func TestComputeRCTStat_ZeroWindow(t *testing.T) {
	t.Parallel()

	data := []byte{0x00, 0x00, 0x00}
	stat := computeRCTStat(data, 0)

	if stat != 0 {
		t.Errorf("expected RCT stat=0 for zero window, got %f", stat)
	}
}

func TestComputeRCTStat_SingleByte(t *testing.T) {
	t.Parallel()

	data := []byte{0x42}
	stat := computeRCTStat(data, 10)

	// Single byte = one "run" of length 1
	if stat != 1 {
		t.Errorf("expected RCT stat=1 for single byte, got %f", stat)
	}
}

// computeAPTStat Tests

func TestComputeAPTStat_UniformDistribution(t *testing.T) {
	t.Parallel()

	// Uniform: each value appears once
	data := make([]byte, 256)
	for i := 0; i < 256; i++ {
		data[i] = byte(i)
	}

	stat := computeAPTStat(data, 256)

	// Each value appears once, so max proportion = 1/256
	expectedStat := 1.0 / 256.0
	if math.Abs(stat-expectedStat) > 0.001 {
		t.Errorf("expected APT stat=%.6f for uniform distribution, got %.6f", expectedStat, stat)
	}

	t.Logf("Uniform distribution: APT stat=%.6f", stat)
}

func TestComputeAPTStat_BiasedDistribution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		data         []byte
		window       uint32
		expectedStat float64
	}{
		{
			name:         "AllSame",
			data:         []byte{0xAA, 0xAA, 0xAA, 0xAA, 0xAA},
			window:       10,
			expectedStat: 1.0, // 100% same value
		},
		{
			name:         "HalfSame",
			data:         []byte{0x00, 0x00, 0x01, 0x01},
			window:       10,
			expectedStat: 0.5, // 2 out of 4
		},
		{
			name: "Seventy30Split",
			data: func() []byte {
				d := make([]byte, 100)
				for i := 0; i < 70; i++ {
					d[i] = 0xFF
				}
				for i := 70; i < 100; i++ {
					d[i] = 0x00
				}
				return d
			}(),
			window:       100,
			expectedStat: 0.7, // 70 out of 100
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			stat := computeAPTStat(tc.data, tc.window)

			if math.Abs(stat-tc.expectedStat) > 0.001 {
				t.Errorf("expected APT stat=%.3f, got %.3f", tc.expectedStat, stat)
			}

			t.Logf("%s: APT stat=%.3f", tc.name, stat)
		})
	}
}

func TestComputeAPTStat_WindowSmallerThanData(t *testing.T) {
	t.Parallel()

	// Create data: first 50 bytes are 0xFF, next 50 are diverse
	data := make([]byte, 100)
	for i := 0; i < 50; i++ {
		data[i] = 0xFF
	}
	for i := 50; i < 100; i++ {
		data[i] = byte(i)
	}

	// Window of 50 should only see the first 50 bytes (all 0xFF)
	stat := computeAPTStat(data, 50)
	if stat != 1.0 {
		t.Errorf("expected APT stat=1.0 for window of all same values, got %.3f", stat)
	}

	// Window of 100 should see mixed data
	stat = computeAPTStat(data, 100)
	if stat < 0.5 || stat > 0.6 {
		t.Errorf("expected APT stat in [0.5, 0.6], got %.3f", stat)
	}

	t.Logf("Window smaller than data: stat(50)=1.0, stat(100)=%.3f", stat)
}

func TestComputeAPTStat_WindowLargerThanData(t *testing.T) {
	t.Parallel()

	data := []byte{0xAA, 0xAA, 0xAA, 0xBB, 0xBB}
	stat := computeAPTStat(data, 100)

	// Should use actual data length (5), max freq is 3
	expectedStat := 3.0 / 5.0
	if math.Abs(stat-expectedStat) > 0.001 {
		t.Errorf("expected APT stat=%.3f when window > data, got %.3f", expectedStat, stat)
	}

	t.Logf("Window larger than data: APT stat=%.3f", stat)
}

func TestComputeAPTStat_EmptyData(t *testing.T) {
	t.Parallel()

	stat := computeAPTStat(nil, 100)
	if stat != 0 {
		t.Errorf("expected APT stat=0 for nil data, got %f", stat)
	}

	stat = computeAPTStat([]byte{}, 100)
	if stat != 0 {
		t.Errorf("expected APT stat=0 for empty slice, got %f", stat)
	}
}

func TestComputeAPTStat_ZeroWindow(t *testing.T) {
	t.Parallel()

	data := []byte{0x00, 0x00, 0x00}
	stat := computeAPTStat(data, 0)

	if stat != 0 {
		t.Errorf("expected APT stat=0 for zero window, got %f", stat)
	}
}

func TestComputeAPTStat_SingleByte(t *testing.T) {
	t.Parallel()

	data := []byte{0x42}
	stat := computeAPTStat(data, 10)

	// Single byte appears once, proportion = 1.0
	if stat != 1.0 {
		t.Errorf("expected APT stat=1.0 for single byte, got %f", stat)
	}
}

func TestComputeAPTStat_MultipleValuesSameFrequency(t *testing.T) {
	t.Parallel()

	// Equal distribution: 0x00, 0x00, 0x01, 0x01, 0x02, 0x02
	data := []byte{0x00, 0x00, 0x01, 0x01, 0x02, 0x02}
	stat := computeAPTStat(data, 10)

	// Max frequency is 2, proportion = 2/6 = 0.333...
	expectedStat := 2.0 / 6.0
	if math.Abs(stat-expectedStat) > 0.001 {
		t.Errorf("expected APT stat=%.3f, got %.3f", expectedStat, stat)
	}

	t.Logf("Equal frequencies: APT stat=%.3f", stat)
}

// Integration Tests - Test all functions together

func TestTestSummary_IntegrationWithRealWorldData(t *testing.T) {
	t.Parallel()

	// Simulate real-world entropy data: pseudo-random with good distribution
	data := make([]byte, 1024)
	for i := 0; i < 1024; i++ {
		// Mix of patterns to simulate realistic entropy
		data[i] = byte((i*7 + 13) % 256)
	}

	summary := ComputeTestSummary(data, 40, 512)

	if summary == nil {
		t.Fatal("expected non-nil TestSummary")
	}

	// All p-values should be in valid range
	if summary.FreqPvalue < 0 || summary.FreqPvalue > 1 {
		t.Errorf("FreqPvalue out of range: %f", summary.FreqPvalue)
	}
	if summary.RunsPvalue < 0 || summary.RunsPvalue > 1 {
		t.Errorf("RunsPvalue out of range: %f", summary.RunsPvalue)
	}

	// Stats should be present and reasonable
	if summary.RctStat == nil {
		t.Error("RctStat should be set")
	} else if *summary.RctStat < 1 || *summary.RctStat > 40 {
		t.Logf("Warning: RctStat=%.2f seems unusual", *summary.RctStat)
	}

	if summary.AptStat == nil {
		t.Error("AptStat should be set")
	} else if *summary.AptStat < 0 || *summary.AptStat > 1 {
		t.Errorf("AptStat out of range [0,1]: %f", *summary.AptStat)
	}

	t.Logf("Real-world simulation: FreqP=%.4f, RunsP=%.4f, RCT=%.2f, APT=%.4f",
		summary.FreqPvalue, summary.RunsPvalue, *summary.RctStat, *summary.AptStat)
}

// Benchmark Tests

func BenchmarkComputeTestSummary(b *testing.B) {
	data := make([]byte, 1024)
	for i := 0; i < 1024; i++ {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeTestSummary(data, 40, 512)
	}
}

func BenchmarkComputeFrequencyPValue(b *testing.B) {
	data := make([]byte, 1024)
	for i := 0; i < 1024; i++ {
		data[i] = byte(i % 2)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeFrequencyPValue(data, 0.5)
	}
}

func BenchmarkComputeRunsPValue(b *testing.B) {
	data := make([]byte, 1024)
	for i := 0; i < 1024; i++ {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeRunsPValue(data, 512)
	}
}

func BenchmarkComputeRCTStat(b *testing.B) {
	data := make([]byte, 1024)
	for i := 0; i < 1024; i++ {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeRCTStat(data, 1024)
	}
}

func BenchmarkComputeAPTStat(b *testing.B) {
	data := make([]byte, 1024)
	for i := 0; i < 1024; i++ {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		computeAPTStat(data, 1024)
	}
}
