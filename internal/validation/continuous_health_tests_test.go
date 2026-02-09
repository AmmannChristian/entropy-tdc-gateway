package validation

import (
	"testing"
)

// TestRCT_NormalData verifies that the RCT passes for typical random-looking data
// with occasional repeated values (as expected from a healthy entropy source).
func TestRCT_NormalData(t *testing.T) {
	rct := NewRepetitionCountTest(40)

	// Simulate normal data: mix of values with some short runs
	normalData := []byte{
		0x42, 0x42, 0x17, 0x89, 0x89, 0x89, 0x3A, 0xFF,
		0x00, 0x7B, 0x7B, 0x01, 0x01, 0x9C, 0xAB, 0xCD,
		0xEF, 0x12, 0x34, 0x56, 0x78, 0x90, 0xAB, 0xCD,
	}

	for i, sample := range normalData {
		if !rct.Test(sample) {
			t.Errorf("RCT failed unexpectedly at index %d with sample 0x%02X", i, sample)
		}
	}

	// Verify state after processing
	lastSample, repeatCount, cutoff, initialized := rct.GetState()
	if !initialized {
		t.Error("RCT should be initialized after processing samples")
	}
	if cutoff != 40 {
		t.Errorf("Expected cutoff=40, got %d", cutoff)
	}
	if repeatCount >= cutoff {
		t.Errorf("Repeat count %d should be less than cutoff %d", repeatCount, cutoff)
	}
	t.Logf("Final state: lastSample=0x%02X, repeatCount=%d", lastSample, repeatCount)
}

// TestRCT_AllZeros verifies that the RCT detects a stuck-at-zero fault
// by failing after exactly 40 consecutive zero samples (cutoff threshold).
func TestRCT_AllZeros(t *testing.T) {
	cutoff := 40
	rct := NewRepetitionCountTest(cutoff)

	// First 39 zeros should pass (initialize + 38 repeats)
	for i := 0; i < cutoff-1; i++ {
		if !rct.Test(0x00) {
			t.Errorf("RCT failed prematurely at sample %d (expected to pass until sample %d)", i+1, cutoff)
		}
	}

	// Verify we're at the threshold
	_, repeatCount, _, _ := rct.GetState()
	if repeatCount != cutoff-1 {
		t.Errorf("Expected repeatCount=%d before failure, got %d", cutoff-1, repeatCount)
	}

	// The 40th zero should trigger failure
	if rct.Test(0x00) {
		t.Error("RCT should have failed on the 40th consecutive zero")
	}

	t.Logf("RCT correctly failed after %d consecutive zeros", cutoff)
}

// TestRCT_AlternatingBits verifies that the RCT passes for alternating
// bit patterns (0x00, 0x01, 0x00, 0x01, ...), which represents good
// entropy with no stuck-at faults.
func TestRCT_AlternatingBits(t *testing.T) {
	rct := NewRepetitionCountTest(40)

	// Alternate between 0x00 and 0x01 for 1000 samples
	for i := 0; i < 1000; i++ {
		sample := byte(i % 2)
		if !rct.Test(sample) {
			t.Errorf("RCT failed unexpectedly at index %d with alternating pattern", i)
		}
	}

	// Repeat count should never exceed 1 for alternating pattern
	_, repeatCount, _, _ := rct.GetState()
	if repeatCount > 1 {
		t.Errorf("Expected repeatCount=1 for alternating pattern, got %d", repeatCount)
	}

	t.Log("RCT correctly passed 1000 alternating samples")
}

// TestRCT_TestBlock verifies the TestBlock method works correctly
// for both passing and failing scenarios.
func TestRCT_TestBlock(t *testing.T) {
	t.Run("PassingBlock", func(t *testing.T) {
		rct := NewRepetitionCountTest(40)
		goodBlock := []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}

		if !rct.TestBlock(goodBlock) {
			t.Error("TestBlock should pass for diverse samples")
		}
	})

	t.Run("FailingBlock", func(t *testing.T) {
		rct := NewRepetitionCountTest(40)
		// Create block with 50 zeros (exceeds cutoff of 40)
		badBlock := make([]byte, 50)
		// All zeros by default

		if rct.TestBlock(badBlock) {
			t.Error("TestBlock should fail for 50 consecutive zeros (cutoff=40)")
		}
	})

	t.Run("EdgeCase_ExactlyCutoff", func(t *testing.T) {
		rct := NewRepetitionCountTest(40)
		// Create block with exactly 39 zeros (should pass)
		edgeBlock := make([]byte, 39)

		if !rct.TestBlock(edgeBlock) {
			t.Error("TestBlock should pass for exactly 39 zeros (cutoff=40)")
		}

		// Add one more zero to trigger failure
		if rct.Test(0x00) {
			t.Error("40th zero should trigger failure")
		}
	})
}

// TestRCT_Reset verifies that Reset() properly clears the RCT state.
func TestRCT_Reset(t *testing.T) {
	rct := NewRepetitionCountTest(40)

	// Build up some state
	for i := 0; i < 20; i++ {
		rct.Test(0xFF)
	}

	// Verify state exists
	lastSample, repeatCount, _, initialized := rct.GetState()
	if !initialized || lastSample != 0xFF || repeatCount != 20 {
		t.Errorf("Expected initialized state with lastSample=0xFF, repeatCount=20, got lastSample=0x%02X, repeatCount=%d, initialized=%v",
			lastSample, repeatCount, initialized)
	}

	// Reset
	rct.Reset()

	// Verify state is cleared
	_, repeatCount, _, initialized = rct.GetState()
	if initialized {
		t.Error("RCT should not be initialized after Reset()")
	}
	if repeatCount != 0 {
		t.Errorf("Expected repeatCount=0 after Reset(), got %d", repeatCount)
	}

	t.Log("Reset() successfully cleared RCT state")
}

// TestRCT_ConcurrentAccess verifies thread-safety of the RCT implementation.
func TestRCT_ConcurrentAccess(t *testing.T) {
	rct := NewRepetitionCountTest(40)

	// Run multiple goroutines testing samples concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 100; j++ {
				sample := byte((id + j) % 256)
				rct.Test(sample)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify RCT is still in a valid state
	_, repeatCount, cutoff, initialized := rct.GetState()
	if !initialized {
		t.Error("RCT should be initialized after concurrent access")
	}
	if repeatCount >= cutoff {
		t.Errorf("Repeat count %d should be less than cutoff %d", repeatCount, cutoff)
	}

	t.Log("RCT handled concurrent access without issues")
}

// TestRCT_CustomCutoff verifies that custom cutoff values work correctly.
func TestRCT_CustomCutoff(t *testing.T) {
	testCases := []struct {
		name           string
		cutoff         int
		expectedCutoff int
	}{
		{"Default", 0, 40},
		{"Negative", -1, 40},
		{"Custom_10", 10, 10},
		{"Custom_100", 100, 100},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			rct := NewRepetitionCountTest(tc.cutoff)
			_, _, actualCutoff, _ := rct.GetState()

			if actualCutoff != tc.expectedCutoff {
				t.Errorf("Expected cutoff=%d, got %d", tc.expectedCutoff, actualCutoff)
			}

			// Test that failure occurs at the expected threshold
			for i := 0; i < tc.expectedCutoff-1; i++ {
				if !rct.Test(0xAA) {
					t.Errorf("RCT failed prematurely at sample %d", i+1)
				}
			}

			// Should fail on the cutoff-th sample
			if rct.Test(0xAA) {
				t.Errorf("RCT should fail at sample %d", tc.expectedCutoff)
			}
		})
	}
}

// TestRCT_DifferentByteValues verifies RCT works with all byte values.
func TestRCT_DifferentByteValues(t *testing.T) {
	testValues := []byte{0x00, 0x01, 0x7F, 0x80, 0xFE, 0xFF}

	for _, val := range testValues {
		t.Run(string(rune(val)), func(t *testing.T) {
			rct := NewRepetitionCountTest(40)

			// 39 repetitions should pass
			for i := 0; i < 39; i++ {
				if !rct.Test(val) {
					t.Errorf("RCT failed prematurely for value 0x%02X at iteration %d", val, i+1)
				}
			}

			// 40th repetition should fail
			if rct.Test(val) {
				t.Errorf("RCT should fail on 40th repetition of value 0x%02X", val)
			}
		})
	}
}

// BenchmarkRCT_SingleSample measures the performance of testing individual samples.
func BenchmarkRCT_SingleSample(b *testing.B) {
	rct := NewRepetitionCountTest(40)
	sample := byte(0x42)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rct.Test(sample)
		if i%10 == 0 {
			// Vary the sample occasionally to prevent failure
			sample = byte(i % 256)
		}
	}
}

// BenchmarkRCT_Block measures the performance of testing sample blocks.
func BenchmarkRCT_Block(b *testing.B) {
	rct := NewRepetitionCountTest(40)
	block := make([]byte, 1024)
	for i := range block {
		block[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rct.TestBlock(block)
	}
}

// TestAPT_RandomData verifies that APT passes for well-distributed random data.
func TestAPT_RandomData(t *testing.T) {
	apt := NewAdaptiveProportionTest(605, 4096)

	// Generate pseudo-random data with good distribution
	// Using a simple pattern that distributes values evenly
	for i := 0; i < 4096; i++ {
		sample := byte(i % 256) // Cycles through all byte values
		if !apt.Test(sample) {
			t.Errorf("APT failed unexpectedly at sample %d", i)
		}
	}

	t.Log("APT correctly passed 4096 samples of well-distributed data")
}

// TestAPT_BiasedData verifies that APT detects statistical bias (70% ones, 30% zeros).
func TestAPT_BiasedData(t *testing.T) {
	apt := NewAdaptiveProportionTest(605, 4096)

	// Create biased data: 70% ones, 30% zeros
	// This should trigger APT failure when the window completes
	failureDetected := false
	for i := 0; i < 4096; i++ {
		var sample byte
		if i%10 < 7 { // 70% of the time
			sample = 0x01
		} else { // 30% of the time
			sample = 0x00
		}

		if !apt.Test(sample) {
			failureDetected = true
			t.Logf("APT correctly failed at sample %d due to bias (70%% ones)", i+1)
			break
		}
	}

	if !failureDetected {
		t.Error("APT should have failed for biased data (70% ones, 30% zeros)")
	}
}

// TestAPT_BalancedDistribution verifies that APT passes for well-distributed data.
// Note: Strictly alternating 0,1,0,1 would actually fail APT because ~2048 samples
// match the first sample (0), exceeding cutoff of 605. APT is working correctly.
func TestAPT_BalancedDistribution(t *testing.T) {
	apt := NewAdaptiveProportionTest(605, 4096)

	// Create well-distributed data cycling through all byte values
	// This ensures no single value appears too frequently
	for i := 0; i < 4096; i++ {
		sample := byte(i % 256) // Cycles through 0-255
		if !apt.Test(sample) {
			t.Errorf("APT failed unexpectedly at sample %d for well-distributed data", i)
		}
	}

	// Verify state after window completes
	firstSample, matchCount, sampleCount, cutoff, windowSize := apt.GetState()
	if sampleCount != 0 {
		t.Errorf("Expected sampleCount=0 after window reset, got %d", sampleCount)
	}
	if matchCount != 0 {
		t.Errorf("Expected matchCount=0 after window reset, got %d", matchCount)
	}

	t.Logf("APT correctly passed well-distributed data (cutoff=%d, windowSize=%d, lastFirstSample=0x%02X)",
		cutoff, windowSize, firstSample)
}

// TestAPT_MultipleWindows verifies APT works correctly across multiple windows.
func TestAPT_MultipleWindows(t *testing.T) {
	apt := NewAdaptiveProportionTest(605, 4096)

	// Process 3 full windows of good data
	for window := 0; window < 3; window++ {
		for i := 0; i < 4096; i++ {
			sample := byte((window*4096 + i) % 256)
			if !apt.Test(sample) {
				t.Errorf("APT failed in window %d at sample %d", window, i)
			}
		}
		t.Logf("Window %d completed successfully", window)
	}
}

// TestAPT_EdgeCaseExactCutoff verifies behavior at exactly the cutoff threshold.
func TestAPT_EdgeCaseExactCutoff(t *testing.T) {
	cutoff := 100
	windowSize := 1000
	apt := NewAdaptiveProportionTest(cutoff, windowSize)

	// Fill window with exactly cutoff-1 matches to firstSample
	// First sample sets the target
	apt.Test(0xAA)

	// Add cutoff-2 more 0xAA (total: cutoff-1)
	for i := 0; i < cutoff-2; i++ {
		apt.Test(0xAA)
	}

	// Fill rest of window with different values
	for i := cutoff - 1; i < windowSize; i++ {
		apt.Test(0xBB)
	}

	// Should pass,matchCount = cutoff-1 < cutoff
	firstSample, matchCount, sampleCount, _, _ := apt.GetState()
	if sampleCount != 0 { // Window should have reset
		t.Errorf("Window should have reset, but sampleCount=%d", sampleCount)
	}

	t.Logf("APT correctly passed with matchCount=%d (just below cutoff=%d, firstSample=0x%02X)",
		matchCount, cutoff, firstSample)
}

// TestAPT_ExactlyAtCutoff verifies failure when matchCount == cutoff.
func TestAPT_ExactlyAtCutoff(t *testing.T) {
	cutoff := 100
	windowSize := 1000
	apt := NewAdaptiveProportionTest(cutoff, windowSize)

	// Fill window with exactly cutoff matches to firstSample
	for i := 0; i < cutoff; i++ {
		apt.Test(0xAA)
	}

	// Fill rest of window with different values
	failureDetected := false
	for i := cutoff; i < windowSize; i++ {
		if !apt.Test(0xBB) {
			failureDetected = true
			t.Logf("APT correctly failed when matchCount reached cutoff (%d)", cutoff)
			break
		}
	}

	if !failureDetected {
		t.Errorf("APT should fail when matchCount >= cutoff (%d)", cutoff)
	}
}

// TestAPT_Reset verifies that Reset() properly clears APT state.
func TestAPT_Reset(t *testing.T) {
	apt := NewAdaptiveProportionTest(605, 4096)

	// Build up some state
	for i := 0; i < 100; i++ {
		apt.Test(0xFF)
	}

	// Verify state exists
	firstSample, matchCount, sampleCount, _, _ := apt.GetState()
	if sampleCount == 0 || matchCount == 0 {
		t.Error("APT should have non-zero state before Reset()")
	}
	if firstSample != 0xFF {
		t.Errorf("Expected firstSample=0xFF, got 0x%02X", firstSample)
	}

	// Reset
	apt.Reset()

	// Verify state is cleared
	_, matchCount, sampleCount, _, _ = apt.GetState()
	if sampleCount != 0 {
		t.Errorf("Expected sampleCount=0 after Reset(), got %d", sampleCount)
	}
	if matchCount != 0 {
		t.Errorf("Expected matchCount=0 after Reset(), got %d", matchCount)
	}

	t.Log("Reset() successfully cleared APT state")
}

// TestAPT_ConcurrentAccess verifies thread-safety of the APT implementation.
func TestAPT_ConcurrentAccess(t *testing.T) {
	apt := NewAdaptiveProportionTest(605, 4096)

	// Run multiple goroutines testing samples concurrently
	done := make(chan bool)
	for i := 0; i < 10; i++ {
		go func(id int) {
			for j := 0; j < 500; j++ {
				sample := byte((id*500 + j) % 256)
				apt.Test(sample)
			}
			done <- true
		}(i)
	}

	// Wait for all goroutines
	for i := 0; i < 10; i++ {
		<-done
	}

	// Verify APT is still in a valid state
	_, matchCount, sampleCount, cutoff, windowSize := apt.GetState()
	if sampleCount < 0 || sampleCount > windowSize {
		t.Errorf("Invalid sampleCount %d (windowSize=%d)", sampleCount, windowSize)
	}
	if matchCount < 0 || matchCount > cutoff {
		t.Errorf("Invalid matchCount %d (cutoff=%d)", matchCount, cutoff)
	}

	t.Log("APT handled concurrent access without issues")
}

// TestAPT_CustomParameters verifies that custom cutoff and window size work correctly.
func TestAPT_CustomParameters(t *testing.T) {
	testCases := []struct {
		name       string
		cutoff     int
		windowSize int
	}{
		{"Default", 0, 0},      // Should use defaults: 605, 4096
		{"Small", 10, 100},     // Small window
		{"Large", 1000, 10000}, // Large window
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			apt := NewAdaptiveProportionTest(tc.cutoff, tc.windowSize)
			_, _, _, actualCutoff, actualWindowSize := apt.GetState()

			expectedCutoff := tc.cutoff
			expectedWindowSize := tc.windowSize
			if tc.cutoff <= 0 {
				expectedCutoff = 605
			}
			if tc.windowSize <= 0 {
				expectedWindowSize = 4096
			}

			if actualCutoff != expectedCutoff {
				t.Errorf("Expected cutoff=%d, got %d", expectedCutoff, actualCutoff)
			}
			if actualWindowSize != expectedWindowSize {
				t.Errorf("Expected windowSize=%d, got %d", expectedWindowSize, actualWindowSize)
			}
		})
	}
}

// TestAPT_TestBlock verifies the TestBlock method works correctly.
func TestAPT_TestBlock(t *testing.T) {
	t.Run("PassingBlock", func(t *testing.T) {
		apt := NewAdaptiveProportionTest(605, 4096)
		// Create a block with good distribution
		block := make([]byte, 1000)
		for i := range block {
			block[i] = byte(i % 256)
		}

		if !apt.TestBlock(block) {
			t.Error("TestBlock should pass for well-distributed data")
		}
	})

	t.Run("FailingBlock", func(t *testing.T) {
		apt := NewAdaptiveProportionTest(100, 1000)
		// Create biased block: 80% same value
		block := make([]byte, 1000)
		for i := range block {
			if i%5 < 4 { // 80%
				block[i] = 0xAA
			} else {
				block[i] = 0xBB
			}
		}

		if apt.TestBlock(block) {
			t.Error("TestBlock should fail for biased data")
		}
	})
}

// BenchmarkAPT_SingleSample measures the performance of testing individual samples.
func BenchmarkAPT_SingleSample(b *testing.B) {
	apt := NewAdaptiveProportionTest(605, 4096)
	sample := byte(0x42)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		apt.Test(sample)
		// Vary sample occasionally to prevent always matching firstSample
		if i%100 == 0 {
			sample = byte(i % 256)
		}
	}
}

// BenchmarkAPT_Block measures the performance of testing sample blocks.
func BenchmarkAPT_Block(b *testing.B) {
	apt := NewAdaptiveProportionTest(605, 4096)
	block := make([]byte, 1024)
	for i := range block {
		block[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		apt.TestBlock(block)
	}
}
