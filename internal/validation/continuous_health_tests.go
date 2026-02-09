package validation

import (
	"sync"
)

// RepetitionCountTest implements the Repetition Count Test from
// NIST SP 800-90B Section 4.4.1. It detects stuck-at faults by counting
// consecutive identical samples. The test fails when a sample value repeats
// C or more consecutive times, where C is derived from a chosen false-positive
// probability alpha. The default cutoff of 40 corresponds to alpha = 2^-40.
// All methods are safe for concurrent use.
type RepetitionCountTest struct {
	mu          sync.Mutex
	cutoff      int  // C = cutoff threshold (typically 40 for α=2^-40)
	lastSample  byte // Most recently observed sample
	repeatCount int  // Current consecutive repeat count
	initialized bool // Whether we've seen the first sample
}

// NewRepetitionCountTest returns a new RepetitionCountTest with the given
// cutoff threshold. If cutoff is zero or negative, it defaults to 40.
func NewRepetitionCountTest(cutoff int) *RepetitionCountTest {
	if cutoff <= 0 {
		cutoff = 40 // Default from NIST SP 800-90B for α=2^-40
	}

	return &RepetitionCountTest{
		cutoff:      cutoff,
		initialized: false,
	}
}

// Test evaluates a single sample byte against the repetition count
// threshold. It returns true if the test passes or false if the cutoff
// has been reached.
func (rct *RepetitionCountTest) Test(sample byte) bool {
	rct.mu.Lock()
	defer rct.mu.Unlock()

	// Initialize on first sample
	if !rct.initialized {
		rct.lastSample = sample
		rct.repeatCount = 1
		rct.initialized = true
		return true
	}

	// Check if current sample matches the last one
	if sample == rct.lastSample {
		rct.repeatCount++
		if rct.repeatCount >= rct.cutoff {
			// Test FAILED,too many consecutive identical samples
			return false
		}
	} else {
		// Different sample,reset counter
		rct.repeatCount = 1
		rct.lastSample = sample
	}

	return true
}

// TestBlock evaluates a contiguous block of samples, returning false on the
// first sample that causes a test failure.
func (rct *RepetitionCountTest) TestBlock(samples []byte) bool {
	for _, sample := range samples {
		if !rct.Test(sample) {
			return false
		}
	}
	return true
}

// Reset clears all internal state, returning the test to its uninitialized
// condition.
func (rct *RepetitionCountTest) Reset() {
	rct.mu.Lock()
	defer rct.mu.Unlock()

	rct.lastSample = 0
	rct.repeatCount = 0
	rct.initialized = false
}

// GetState returns the current internal state as (lastSample, repeatCount,
// cutoff, initialized) for diagnostic purposes.
func (rct *RepetitionCountTest) GetState() (byte, int, int, bool) {
	rct.mu.Lock()
	defer rct.mu.Unlock()

	return rct.lastSample, rct.repeatCount, rct.cutoff, rct.initialized
}

// AdaptiveProportionTest implements the Adaptive Proportion Test from
// NIST SP 800-90B Section 4.4.2. It detects statistical bias by counting
// how often the first sample in a window of W samples recurs. The test
// fails when the recurrence count reaches the cutoff C. The default values
// of C=605 and W=4096 correspond to alpha = 2^-40 with H=0.5.
// All methods are safe for concurrent use.
type AdaptiveProportionTest struct {
	mu          sync.Mutex
	cutoff      int  // C = cutoff threshold (typically 605 for α=2^-40, H=0.5)
	windowSize  int  // W = window size in samples (typically 4096)
	firstSample byte // First sample in current window
	matchCount  int  // Number of matches to firstSample in current window
	sampleCount int  // Number of samples collected in current window
}

// NewAdaptiveProportionTest returns a new AdaptiveProportionTest with the
// given cutoff and window size. Non-positive values default to 605 and 4096
// respectively.
func NewAdaptiveProportionTest(cutoff, windowSize int) *AdaptiveProportionTest {
	if cutoff <= 0 {
		cutoff = 605 // Default from NIST SP 800-90B for α=2^-40, H=0.5
	}
	if windowSize <= 0 {
		windowSize = 4096 // Default window size from NIST SP 800-90B
	}

	return &AdaptiveProportionTest{
		cutoff:      cutoff,
		windowSize:  windowSize,
		sampleCount: 0,
		matchCount:  0,
	}
}

// Test evaluates a single sample byte. It returns true while the current window
// is still being filled. When a completed window exceeds the cutoff, it returns
// false indicating potential statistical bias. The window resets automatically.
func (apt *AdaptiveProportionTest) Test(sample byte) bool {
	apt.mu.Lock()
	defer apt.mu.Unlock()

	// Initialize window on first sample
	if apt.sampleCount == 0 {
		apt.firstSample = sample
		apt.matchCount = 1
	} else {
		// Check if sample matches the first sample in window
		if sample == apt.firstSample {
			apt.matchCount++
		}
	}

	apt.sampleCount++

	// Check if window is complete
	if apt.sampleCount >= apt.windowSize {
		passed := apt.matchCount < apt.cutoff

		// Reset for next window
		apt.sampleCount = 0
		apt.matchCount = 0

		return passed
	}

	// Still collecting samples,test passes
	return true
}

// TestBlock evaluates an entire block of samples. Returns false if any
// window within the block fails the APT. This is more efficient than calling
// Test() repeatedly when processing batches of entropy.
func (apt *AdaptiveProportionTest) TestBlock(samples []byte) bool {
	for _, sample := range samples {
		if !apt.Test(sample) {
			return false
		}
	}
	return true
}

// Reset clears the APT state, starting a new window. This is typically
// called after a test failure or when restarting entropy collection.
func (apt *AdaptiveProportionTest) Reset() {
	apt.mu.Lock()
	defer apt.mu.Unlock()

	apt.firstSample = 0
	apt.matchCount = 0
	apt.sampleCount = 0
}

// GetState returns the current internal state for debugging and monitoring.
// Returns: (firstSample, matchCount, sampleCount, cutoff, windowSize)
func (apt *AdaptiveProportionTest) GetState() (byte, int, int, int, int) {
	apt.mu.Lock()
	defer apt.mu.Unlock()

	return apt.firstSample, apt.matchCount, apt.sampleCount, apt.cutoff, apt.windowSize
}
