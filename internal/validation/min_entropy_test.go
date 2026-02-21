package validation

import (
	"math"
	"testing"
)

// TestEstimateMCV_AllZeros verifies that all identical values result in 0.0 bits/byte.
func TestEstimateMCV_AllZeros(t *testing.T) {
	data := make([]byte, 1000)
	// All zeros by default

	minEntropy := EstimateMCV(data)

	if minEntropy != 0.0 {
		t.Errorf("Expected min-entropy=0.0 for all zeros, got %.6f", minEntropy)
	}

	t.Logf("All zeros: min-entropy = %.6f bits/byte (correct)", minEntropy)
}

// TestEstimateMCV_UniformDistribution verifies that uniform distribution yields ~8.0 bits/byte.
func TestEstimateMCV_UniformDistribution(t *testing.T) {
	// Create data with perfectly uniform distribution: each byte value appears exactly once
	data := make([]byte, 256)
	for i := 0; i < 256; i++ {
		data[i] = byte(i)
	}

	minEntropy := EstimateMCV(data)

	// For uniform distribution with 256 unique values appearing once each:
	// p_max = 1/256, min_entropy = -log2(1/256) = log2(256) = 8.0
	expectedEntropy := 8.0
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f for uniform distribution, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("Uniform distribution: min-entropy = %.6f bits/byte (ideal)", minEntropy)
}

// TestEstimateMCV_TwoValues verifies 50/50 distribution of two values yields 1.0 bit/byte.
func TestEstimateMCV_TwoValues(t *testing.T) {
	// Create 50/50 distribution: 500 zeros, 500 ones
	data := make([]byte, 1000)
	for i := 0; i < 500; i++ {
		data[i] = 0x00
	}
	for i := 500; i < 1000; i++ {
		data[i] = 0x01
	}

	minEntropy := EstimateMCV(data)

	// For 50/50 distribution: p_max = 0.5, min_entropy = -log2(0.5) = 1.0
	expectedEntropy := 1.0
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f for 50/50 distribution, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("50/50 two values: min-entropy = %.6f bit/byte (correct)", minEntropy)
}

// TestEstimateMCV_EmptyData verifies empty input returns 0.0.
func TestEstimateMCV_EmptyData(t *testing.T) {
	var data []byte

	minEntropy := EstimateMCV(data)

	if minEntropy != 0.0 {
		t.Errorf("Expected min-entropy=0.0 for empty data, got %.6f", minEntropy)
	}

	t.Log("Empty data: min-entropy = 0.0 (correct)")
}

// TestEstimateMCV_SingleByte verifies single byte input returns 0.0.
func TestEstimateMCV_SingleByte(t *testing.T) {
	data := []byte{0x42}

	minEntropy := EstimateMCV(data)

	// Single value has p_max = 1.0, so min_entropy = 0.0
	if minEntropy != 0.0 {
		t.Errorf("Expected min-entropy=0.0 for single byte, got %.6f", minEntropy)
	}

	t.Log("Single byte: min-entropy = 0.0 (correct)")
}

// TestEstimateMCV_BiasedDistribution verifies biased distribution (70/30) calculation.
func TestEstimateMCV_BiasedDistribution(t *testing.T) {
	// Create 70/30 distribution: 700 zeros, 300 ones
	data := make([]byte, 1000)
	for i := 0; i < 700; i++ {
		data[i] = 0x00
	}
	for i := 700; i < 1000; i++ {
		data[i] = 0x01
	}

	minEntropy := EstimateMCV(data)

	// For this distribution, the expected value is the negative base-two
	// logarithm of 0.7.
	expectedEntropy := -math.Log2(0.7)
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f for 70/30 distribution, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("70/30 biased distribution: min-entropy = %.6f bits/byte", minEntropy)
}

// TestEstimateMCV_RepeatingPattern verifies detection of low entropy in repeating patterns.
func TestEstimateMCV_RepeatingPattern(t *testing.T) {
	// Create repeating pattern: 0x00, 0x01, 0x02, 0x03 repeated 250 times
	data := make([]byte, 1000)
	for i := 0; i < 1000; i++ {
		data[i] = byte(i % 4)
	}

	minEntropy := EstimateMCV(data)

	// Each of 4 values appears 250 times: p_max = 0.25, min_entropy = -log2(0.25) = 2.0
	expectedEntropy := 2.0
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f for repeating 4-value pattern, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("Repeating 4-value pattern: min-entropy = %.6f bits/byte", minEntropy)
}

// TestEstimateMCVWithStats verifies the extended stats function.
func TestEstimateMCVWithStats(t *testing.T) {
	// Create test data: 500 zeros, 300 ones, 200 twos
	data := make([]byte, 1000)
	for i := 0; i < 500; i++ {
		data[i] = 0x00
	}
	for i := 500; i < 800; i++ {
		data[i] = 0x01
	}
	for i := 800; i < 1000; i++ {
		data[i] = 0x02
	}

	minEntropy, mostCommonValue, maxCount, uniqueValues := EstimateMCVWithStats(data)

	// Verify statistics
	if mostCommonValue != 0x00 {
		t.Errorf("Expected most common value=0x00, got 0x%02X", mostCommonValue)
	}
	if maxCount != 500 {
		t.Errorf("Expected max count=500, got %d", maxCount)
	}
	if uniqueValues != 3 {
		t.Errorf("Expected 3 unique values, got %d", uniqueValues)
	}

	// Verify min-entropy calculation
	expectedEntropy := -math.Log2(0.5) // p_max = 500/1000 = 0.5
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("Stats: min-entropy=%.6f, most_common=0x%02X, max_count=%d, unique=%d",
		minEntropy, mostCommonValue, maxCount, uniqueValues)
}

// TestEstimateMCVWithStats_EmptyData verifies stats function handles empty input.
func TestEstimateMCVWithStats_EmptyData(t *testing.T) {
	var data []byte

	minEntropy, mostCommonValue, maxCount, uniqueValues := EstimateMCVWithStats(data)

	if minEntropy != 0.0 || mostCommonValue != 0 || maxCount != 0 || uniqueValues != 0 {
		t.Errorf("Expected all zeros for empty data, got: entropy=%.6f, mcv=0x%02X, count=%d, unique=%d",
			minEntropy, mostCommonValue, maxCount, uniqueValues)
	}

	t.Log("Empty data stats: all values correctly zero")
}

// TestEstimateMCVWithStats_AllIdentical verifies pMax >= 1.0 branch (all identical bytes).
func TestEstimateMCVWithStats_AllIdentical(t *testing.T) {
	// All bytes are the same value
	data := make([]byte, 1000)
	for i := 0; i < 1000; i++ {
		data[i] = 0x42
	}

	minEntropy, mostCommonValue, maxCount, uniqueValues := EstimateMCVWithStats(data)

	// Verify statistics
	if mostCommonValue != 0x42 {
		t.Errorf("Expected most common value=0x42, got 0x%02X", mostCommonValue)
	}
	if maxCount != 1000 {
		t.Errorf("Expected max count=1000, got %d", maxCount)
	}
	if uniqueValues != 1 {
		t.Errorf("Expected 1 unique value, got %d", uniqueValues)
	}

	// Verify min-entropy calculation: pMax = 1.0, so entropy should be 0.0
	if minEntropy != 0.0 {
		t.Errorf("Expected min-entropy=0.0 for all identical bytes (pMax=1.0), got %.6f", minEntropy)
	}

	t.Logf("All identical bytes: min-entropy=%.6f, pMax=1.0 branch covered", minEntropy)
}

// TestEstimateMCV_HighEntropyData verifies near-ideal entropy with slight bias.
func TestEstimateMCV_HighEntropyData(t *testing.T) {
	// Create data where most values appear once, but one value appears twice
	data := make([]byte, 256)
	for i := 0; i < 255; i++ {
		data[i] = byte(i)
	}
	data[255] = 0x00 // Make 0x00 appear twice

	minEntropy := EstimateMCV(data)

	// p_max = 2/256 = 1/128, min_entropy = -log2(1/128) = log2(128) = 7.0
	expectedEntropy := 7.0
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("High entropy with slight bias: min-entropy = %.6f bits/byte", minEntropy)
}

// TestEstimateMCV_LowEntropyWarning verifies detection of concerning low entropy.
func TestEstimateMCV_LowEntropyWarning(t *testing.T) {
	testCases := []struct {
		name            string
		data            []byte
		expectedBelow75 bool
	}{
		{
			name:            "AllSame",
			data:            make([]byte, 1000), // All zeros
			expectedBelow75: true,
		},
		{
			name: "90/10Split",
			data: func() []byte {
				d := make([]byte, 1000)
				for i := 0; i < 900; i++ {
					d[i] = 0x00
				}
				for i := 900; i < 1000; i++ {
					d[i] = 0x01
				}
				return d
			}(),
			expectedBelow75: true, // Strongly biased input remains below the 7.5 threshold.
		},
		{
			name: "NearUniform",
			data: func() []byte {
				d := make([]byte, 256)
				for i := 0; i < 256; i++ {
					d[i] = byte(i)
				}
				return d
			}(),
			expectedBelow75: false, // 8.0 > 7.5
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			minEntropy := EstimateMCV(tc.data)
			isBelow75 := minEntropy < 7.5

			if isBelow75 != tc.expectedBelow75 {
				t.Errorf("Expected below 7.5=%v, got min-entropy=%.6f", tc.expectedBelow75, minEntropy)
			}

			if isBelow75 {
				t.Logf("%s: LOW ENTROPY detected (%.6f < 7.5 bits/byte)", tc.name, minEntropy)
			} else {
				t.Logf("%s: Good entropy (%.6f >= 7.5 bits/byte)", tc.name, minEntropy)
			}
		})
	}
}

// BenchmarkEstimateMCV_SmallData benchmarks MCV estimation on small datasets.
func BenchmarkEstimateMCV_SmallData(b *testing.B) {
	data := make([]byte, 256)
	for i := 0; i < 256; i++ {
		data[i] = byte(i % 16) // 16 unique values
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateMCV(data)
	}
}

// BenchmarkEstimateMCV_LargeData benchmarks MCV estimation on large datasets.
func BenchmarkEstimateMCV_LargeData(b *testing.B) {
	data := make([]byte, 10000)
	for i := 0; i < 10000; i++ {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateMCV(data)
	}
}

// BenchmarkEstimateMCVWithStats benchmarks the extended stats function.
func BenchmarkEstimateMCVWithStats(b *testing.B) {
	data := make([]byte, 1000)
	for i := 0; i < 1000; i++ {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateMCVWithStats(data)
	}
}

// TestEstimateCollision_ImmediateCollision verifies early collision detection.
func TestEstimateCollision_ImmediateCollision(t *testing.T) {
	// Immediate collision: [0x00, 0x00],collision at position 2
	data := []byte{0x00, 0x00}

	minEntropy := EstimateCollision(data)

	// t_collision = 2, so min_entropy = log2(2) = 1.0
	expectedEntropy := 1.0
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f for immediate collision, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("Immediate collision (t=2): min-entropy = %.6f bit/byte", minEntropy)
}

// TestEstimateCollision_EarlyCollision verifies early collision (low entropy).
func TestEstimateCollision_EarlyCollision(t *testing.T) {
	// Early collision: [0, 1, 2, 3, 0],collision at position 5
	data := []byte{0x00, 0x01, 0x02, 0x03, 0x00}

	minEntropy := EstimateCollision(data)

	// The first repeated value appears at position five.
	expectedEntropy := math.Log2(5)
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f for early collision, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("Early collision (t=5): min-entropy = %.6f bits/byte", minEntropy)
}

// TestEstimateCollision_LateCollision verifies late collision (high entropy).
func TestEstimateCollision_LateCollision(t *testing.T) {
	// Late collision: all unique values until the 256th position
	data := make([]byte, 257)
	for i := 0; i < 256; i++ {
		data[i] = byte(i)
	}
	data[256] = 0x00 // Collision with first value

	minEntropy := EstimateCollision(data)

	// The first repeated value appears at position 257, which reaches the
	// implementation ceiling of 8.0 bits per byte.
	expectedEntropy := 8.0 // Clamped to maximum
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f for late collision, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("Late collision (t=257): min-entropy = %.6f bits/byte (clamped to max)", minEntropy)
}

// TestEstimateCollision_NoCollision verifies behavior when no collision occurs.
func TestEstimateCollision_NoCollision(t *testing.T) {
	// All unique values: [0, 1, 2, ..., 255]
	data := make([]byte, 256)
	for i := 0; i < 256; i++ {
		data[i] = byte(i)
	}

	minEntropy := EstimateCollision(data)

	// No collision = maximum entropy
	expectedEntropy := 8.0
	if minEntropy != expectedEntropy {
		t.Errorf("Expected min-entropy=%.6f for no collision, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("No collision: min-entropy = %.6f bits/byte (maximum)", minEntropy)
}

// TestEstimateCollision_EmptyData verifies empty input handling.
func TestEstimateCollision_EmptyData(t *testing.T) {
	var data []byte

	minEntropy := EstimateCollision(data)

	if minEntropy != 0.0 {
		t.Errorf("Expected min-entropy=0.0 for empty data, got %.6f", minEntropy)
	}

	t.Log("Empty data: min-entropy = 0.0 (correct)")
}

// TestEstimateCollision_SingleByte verifies single byte input.
func TestEstimateCollision_SingleByte(t *testing.T) {
	data := []byte{0x42}

	minEntropy := EstimateCollision(data)

	// Single value cannot collide,return maximum entropy
	if minEntropy != 8.0 {
		t.Errorf("Expected min-entropy=8.0 for single byte, got %.6f", minEntropy)
	}

	t.Log("Single byte: min-entropy = 8.0 (max, no collision possible)")
}

// TestEstimateCollisionWithStats verifies the stats function.
func TestEstimateCollisionWithStats(t *testing.T) {
	// Data: [0, 1, 2, 3, 4, 2],collision at position 6
	data := []byte{0x00, 0x01, 0x02, 0x03, 0x04, 0x02}

	minEntropy, collisionTime, collisionValue, uniqueBeforeCollision := EstimateCollisionWithStats(data)

	// Verify statistics
	if collisionTime != 6 {
		t.Errorf("Expected collision time=6, got %d", collisionTime)
	}
	if collisionValue != 0x02 {
		t.Errorf("Expected collision value=0x02, got 0x%02X", collisionValue)
	}
	if uniqueBeforeCollision != 5 {
		t.Errorf("Expected 5 unique values before collision, got %d", uniqueBeforeCollision)
	}

	// Verify min-entropy calculation
	expectedEntropy := math.Log2(6)
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("Stats: min-entropy=%.6f, collision_time=%d, collision_value=0x%02X, unique=%d",
		minEntropy, collisionTime, collisionValue, uniqueBeforeCollision)
}

// TestEstimateCollisionWithStats_NoCollision verifies stats with no collision.
func TestEstimateCollisionWithStats_NoCollision(t *testing.T) {
	data := []byte{0x00, 0x01, 0x02, 0x03}

	minEntropy, collisionTime, collisionValue, uniqueBeforeCollision := EstimateCollisionWithStats(data)

	if collisionTime != 0 {
		t.Errorf("Expected collision time=0 (no collision), got %d", collisionTime)
	}
	if collisionValue != 0 {
		t.Errorf("Expected collision value=0 (no collision), got 0x%02X", collisionValue)
	}
	if minEntropy != 8.0 {
		t.Errorf("Expected min-entropy=8.0, got %.6f", minEntropy)
	}
	if uniqueBeforeCollision != 4 {
		t.Errorf("Expected 4 unique values, got %d", uniqueBeforeCollision)
	}

	t.Logf("No collision: min-entropy=%.6f, unique=%d", minEntropy, uniqueBeforeCollision)
}

// TestEstimateCollisionWithStats_SingleByte verifies len(data)==1 branch.
func TestEstimateCollisionWithStats_SingleByte(t *testing.T) {
	data := []byte{0x42}

	minEntropy, collisionTime, collisionValue, uniqueBeforeCollision := EstimateCollisionWithStats(data)

	// Single byte: no collision possible, returns max entropy
	if minEntropy != 8.0 {
		t.Errorf("Expected min-entropy=8.0 for single byte, got %.6f", minEntropy)
	}
	if collisionTime != 0 {
		t.Errorf("Expected collision time=0 (no collision possible), got %d", collisionTime)
	}
	if collisionValue != 0x42 {
		t.Errorf("Expected collision value=0x42, got 0x%02X", collisionValue)
	}
	if uniqueBeforeCollision != 1 {
		t.Errorf("Expected 1 unique value, got %d", uniqueBeforeCollision)
	}

	t.Logf("Single byte: min-entropy=%.6f, no collision (len==1 branch covered)", minEntropy)
}

// TestEstimateCollisionWithStats_ImmediateCollision verifies tCollision==2 branch.
func TestEstimateCollisionWithStats_ImmediateCollision(t *testing.T) {
	// Immediate collision: two identical bytes
	data := []byte{0xAA, 0xAA}

	minEntropy, collisionTime, collisionValue, uniqueBeforeCollision := EstimateCollisionWithStats(data)

	// Verify statistics
	if collisionTime != 2 {
		t.Errorf("Expected collision time=2, got %d", collisionTime)
	}
	if collisionValue != 0xAA {
		t.Errorf("Expected collision value=0xAA, got 0x%02X", collisionValue)
	}
	if uniqueBeforeCollision != 1 {
		t.Errorf("Expected 1 unique value before collision, got %d", uniqueBeforeCollision)
	}

	// Verify min-entropy: tCollision==2 branch returns exactly 1.0
	if minEntropy != 1.0 {
		t.Errorf("Expected min-entropy=1.0 for tCollision==2, got %.6f", minEntropy)
	}

	t.Logf("Immediate collision (t=2): min-entropy=%.6f (tCollision==2 branch covered)", minEntropy)
}

// TestEstimateMinEntropyConservative verifies the conservative estimator.
func TestEstimateMinEntropyConservative(t *testing.T) {
	testCases := []struct {
		name             string
		data             []byte
		expectedMCVLower bool // True if MCV should be lower than Collision
	}{
		{
			name: "MCVLower",
			// Biased data: 90% zeros, collision at t=2
			data: func() []byte {
				d := make([]byte, 100)
				for i := 0; i < 90; i++ {
					d[i] = 0x00
				}
				for i := 90; i < 100; i++ {
					d[i] = byte(i)
				}
				return d
			}(),
			expectedMCVLower: true,
		},
		{
			name: "CollisionLower",
			// Early collision but more diverse values
			data:             []byte{0x00, 0x01, 0x02, 0x00, 0x03, 0x04, 0x05, 0x06},
			expectedMCVLower: false, // Collision (t=4) gives ~2.0, MCV higher
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			mcv := EstimateMCV(tc.data)
			collision := EstimateCollision(tc.data)
			conservative := EstimateMinEntropyConservative(tc.data)

			// Conservative should be the minimum
			expectedMin := mcv
			if collision < mcv {
				expectedMin = collision
			}

			if math.Abs(conservative-expectedMin) > 0.0001 {
				t.Errorf("Expected conservative=%.6f (min of mcv=%.6f, collision=%.6f), got %.6f",
					expectedMin, mcv, collision, conservative)
			}

			isMCVLower := mcv < collision
			if isMCVLower != tc.expectedMCVLower {
				t.Logf("Warning: Expected MCV lower=%v, got MCV=%.6f, Collision=%.6f",
					tc.expectedMCVLower, mcv, collision)
			}

			t.Logf("%s: MCV=%.6f, Collision=%.6f, Conservative=%.6f",
				tc.name, mcv, collision, conservative)
		})
	}
}

// TestEstimateCollision_RepeatingPattern verifies collision with patterns.
func TestEstimateCollision_RepeatingPattern(t *testing.T) {
	// Pattern: [0, 1, 2, 3, 0, 1, 2, 3, ...],collision at t=5
	data := make([]byte, 100)
	for i := 0; i < 100; i++ {
		data[i] = byte(i % 4)
	}

	minEntropy := EstimateCollision(data)

	// First collision at position 5 (when we see '0' again)
	// The first repeated value appears at position five.
	expectedEntropy := math.Log2(5)
	if math.Abs(minEntropy-expectedEntropy) > 0.0001 {
		t.Errorf("Expected min-entropy=%.6f for repeating pattern, got %.6f", expectedEntropy, minEntropy)
	}

	t.Logf("Repeating 4-value pattern: min-entropy = %.6f bits/byte", minEntropy)
}

// BenchmarkEstimateCollision_SmallData benchmarks collision estimation on small datasets.
func BenchmarkEstimateCollision_SmallData(b *testing.B) {
	data := make([]byte, 256)
	for i := 0; i < 256; i++ {
		data[i] = byte(i % 16)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateCollision(data)
	}
}

// BenchmarkEstimateCollision_LargeData benchmarks collision estimation on large datasets.
func BenchmarkEstimateCollision_LargeData(b *testing.B) {
	data := make([]byte, 10000)
	for i := 0; i < 10000; i++ {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateCollision(data)
	}
}

// BenchmarkEstimateCollisionWithStats benchmarks the extended stats function.
func BenchmarkEstimateCollisionWithStats(b *testing.B) {
	data := make([]byte, 1000)
	for i := 0; i < 1000; i++ {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateCollisionWithStats(data)
	}
}

// BenchmarkEstimateMinEntropyConservative benchmarks the combined estimator.
func BenchmarkEstimateMinEntropyConservative(b *testing.B) {
	data := make([]byte, 1000)
	for i := 0; i < 1000; i++ {
		data[i] = byte(i % 256)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		EstimateMinEntropyConservative(data)
	}
}
