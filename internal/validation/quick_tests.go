// Package validation implements entropy quality assessment routines for the
// TDC entropy gateway. It provides quick statistical tests derived from
// NIST SP 800-22, continuous health tests per NIST SP 800-90B Section 4.4,
// and min-entropy estimators per NIST SP 800-90B Section 6.
package validation

import (
	"math"
	"math/bits"

	protocolbuffer "entropy-tdc-gateway/pkg/pb"
)

// FrequencyTest evaluates bit balance in data by computing the ratio of set
// bits to total bits. The test passes when the ratio falls within [0.45, 0.55].
// Returns the observed ratio and a boolean indicating pass/fail. Empty input
// yields (0, false).
func FrequencyTest(data []byte) (float64, bool) {
	if len(data) == 0 {
		return 0, false
	}
	ones := 0
	for _, b := range data {
		ones += bits.OnesCount8(b)
	}

	total := len(data) * 8
	ratio := float64(ones) / float64(total)

	passed := ratio >= 0.45 && ratio <= 0.55
	return ratio, passed
}

// RunsTest evaluates bit alternation by counting uninterrupted sequences of
// identical bit values. The test passes when the observed run count deviates
// by less than 10% from the expected value of n/2. Returns the run count and
// a boolean indicating pass/fail. Empty input yields (0, false).
func RunsTest(data []byte) (int, bool) {
	if len(data) == 0 {
		return 0, false
	}

	runs := 1
	lastBit := (data[0] >> 7) & 1
	firstByte := true

	for _, b := range data {
		startBit := 7
		// Skip the first bit of the first byte (already used as lastBit)
		if firstByte {
			startBit = 6
			firstByte = false
		}

		for i := startBit; i >= 0; i-- {
			bit := (b >> uint(i)) & 1
			if bit != lastBit {
				runs++
				lastBit = bit
			}
		}
	}

	n := len(data) * 8
	expected := float64(n) / 2.0
	deviation := math.Abs(float64(runs)-expected) / expected

	passed := deviation < 0.1
	return runs, passed
}

// ComputeTestSummary aggregates the results of all validation tests into a
// single protobuf TestSummary. It runs the frequency test, runs test, and
// computes RCT/APT summary statistics with approximate p-values.
func ComputeTestSummary(data []byte, rctWindow, aptWindow uint32) *protocolbuffer.TestSummary {
	if len(data) == 0 {
		return &protocolbuffer.TestSummary{}
	}

	freqRatio, _ := FrequencyTest(data)
	freqPValue := computeFrequencyPValue(data, freqRatio)

	runsCount, _ := RunsTest(data)
	runsPValue := computeRunsPValue(data, runsCount)

	rctStat := computeRCTStat(data, rctWindow)
	aptStat := computeAPTStat(data, aptWindow)

	return &protocolbuffer.TestSummary{
		FreqPvalue: freqPValue,
		RunsPvalue: runsPValue,
		RctStat:    &rctStat,
		AptStat:    &aptStat,
		RctWindow:  &rctWindow,
		AptWindow:  &aptWindow,
	}
}

// computeFrequencyPValue approximates the p-value for the frequency test
// using a chi-squared statistic with one degree of freedom.
func computeFrequencyPValue(data []byte, ratio float64) float64 {
	n := float64(len(data) * 8)
	ones := ratio * n

	// Chi-squared statistic
	expected := n / 2.0
	chiSq := math.Pow(ones-expected, 2) / expected

	// Approximate p-value using complementary error function
	// For 1 degree of freedom: P(X > chiSq) ≈ erfc(sqrt(chiSq/2))
	pValue := math.Erfc(math.Sqrt(chiSq / 2))

	// Clamp to [0, 1]
	if pValue < 0 {
		pValue = 0
	}
	if pValue > 1 {
		pValue = 1
	}

	return pValue
}

// computeRunsPValue approximates the p-value for the runs test using a
// two-tailed z-test against the expected run count of n/2.
func computeRunsPValue(data []byte, runs int) float64 {
	n := float64(len(data) * 8)

	// Expected runs and variance for random data
	expectedRuns := n / 2.0
	variance := n / 4.0

	// Z-score
	z := math.Abs(float64(runs)-expectedRuns) / math.Sqrt(variance)

	// Approximate p-value using complementary error function
	// Two-tailed test
	pValue := math.Erfc(z / math.Sqrt(2))

	// Clamp to [0, 1]
	if pValue < 0 {
		pValue = 0
	}
	if pValue > 1 {
		pValue = 1
	}

	return pValue
}

// computeRCTStat computes a summary statistic for the Repetition Count Test
// by finding the longest run of consecutive identical byte values within the
// first window bytes of data.
func computeRCTStat(data []byte, window uint32) float64 {
	if len(data) == 0 || window == 0 {
		return 0
	}

	maxRepetitions := 1
	currentRepetitions := 1
	lastByte := data[0]

	for i := 1; i < len(data) && i < int(window); i++ {
		if data[i] == lastByte {
			currentRepetitions++
			if currentRepetitions > maxRepetitions {
				maxRepetitions = currentRepetitions
			}
		} else {
			currentRepetitions = 1
			lastByte = data[i]
		}
	}

	return float64(maxRepetitions)
}

// computeAPTStat computes a summary statistic for the Adaptive Proportion
// Test by measuring the proportion of the most frequent byte value within
// the first window bytes of data.
func computeAPTStat(data []byte, window uint32) float64 {
	if len(data) == 0 || window == 0 {
		return 0
	}

	// Count frequency of each byte value
	freq := make(map[byte]int)
	windowSize := int(window)
	if windowSize > len(data) {
		windowSize = len(data)
	}

	for i := 0; i < windowSize; i++ {
		freq[data[i]]++
	}

	// Find maximum frequency
	maxFreq := 0
	for _, count := range freq {
		if count > maxFreq {
			maxFreq = count
		}
	}

	// Return proportion
	return float64(maxFreq) / float64(windowSize)
}
