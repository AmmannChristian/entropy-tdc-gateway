package validation

import (
	"math"
)

// EstimateMCV estimates min-entropy using the Most Common Value method from
// NIST SP 800-90B Section 6.3.1. It returns -log2(pmax) where pmax is the
// relative frequency of the most common byte value. The result ranges from
// 0.0 (all identical values) to 8.0 (uniform distribution). Empty input
// yields 0.0.
func EstimateMCV(data []byte) float64 {
	if len(data) == 0 {
		return 0.0
	}

	freq := make(map[byte]int)
	for _, b := range data {
		freq[b]++
	}

	maxCount := 0
	for _, count := range freq {
		if count > maxCount {
			maxCount = count
		}
	}

	pMax := float64(maxCount) / float64(len(data))
	if pMax >= 1.0 {
		return 0.0
	}

	minEntropy := -math.Log2(pMax)

	return minEntropy
}

// EstimateMCVWithStats returns the MCV min-entropy estimate together with
// the most common byte value, its occurrence count, and the number of
// distinct values observed. It is intended for diagnostic use.
func EstimateMCVWithStats(data []byte) (minEntropy float64, mostCommonValue byte, maxCount int, uniqueValues int) {
	if len(data) == 0 {
		return 0.0, 0, 0, 0
	}

	freq := make(map[byte]int)
	for _, b := range data {
		freq[b]++
	}

	uniqueValues = len(freq)

	maxCount = 0
	mostCommonValue = 0
	for val, count := range freq {
		if count > maxCount {
			maxCount = count
			mostCommonValue = val
		}
	}

	pMax := float64(maxCount) / float64(len(data))
	if pMax >= 1.0 {
		minEntropy = 0.0
	} else {
		minEntropy = -math.Log2(pMax)
	}

	return minEntropy, mostCommonValue, maxCount, uniqueValues
}

// EstimateCollision estimates min-entropy using the Collision method from
// NIST SP 800-90B Section 6.3.2. It locates the first repeated byte value
// and computes log2(t) where t is the one-indexed collision position. The
// result is clamped to [0.0, 8.0]. When no collision is found the function
// returns 8.0. Empty input yields 0.0.
func EstimateCollision(data []byte) float64 {
	if len(data) == 0 {
		return 0.0
	}

	if len(data) == 1 {
		return 8.0
	}

	seen := make(map[byte]bool)
	var tCollision int

	for i, b := range data {
		if seen[b] {
			tCollision = i + 1 // Position is 1-indexed for collision time
			break
		}
		seen[b] = true
	}

	if tCollision == 0 {
		return 8.0
	}

	if tCollision == 2 {
		return 1.0
	}

	minEntropy := math.Log2(float64(tCollision))
	if minEntropy > 8.0 {
		minEntropy = 8.0
	}
	if minEntropy < 0.0 {
		minEntropy = 0.0
	}

	return minEntropy
}

// EstimateCollisionWithStats returns the Collision min-entropy estimate
// together with the collision position, the colliding byte value, and the
// number of unique values observed before the collision. It is intended for
// diagnostic use.
func EstimateCollisionWithStats(data []byte) (minEntropy float64, collisionTime int, collisionValue byte, uniqueBeforeCollision int) {
	if len(data) == 0 {
		return 0.0, 0, 0, 0
	}

	if len(data) == 1 {
		return 8.0, 0, data[0], 1
	}

	seen := make(map[byte]bool)
	var tCollision int
	var collidedValue byte

	for i, b := range data {
		if seen[b] {
			tCollision = i + 1
			collidedValue = b
			break
		}
		seen[b] = true
	}

	uniqueBeforeCollision = len(seen)

	if tCollision == 0 {
		return 8.0, 0, 0, uniqueBeforeCollision
	}

	if tCollision == 2 {
		minEntropy = 1.0
	} else {
		minEntropy = math.Log2(float64(tCollision))
		if minEntropy > 8.0 {
			minEntropy = 8.0
		}
		if minEntropy < 0.0 {
			minEntropy = 0.0
		}
	}

	return minEntropy, tCollision, collidedValue, uniqueBeforeCollision
}

// EstimateMinEntropyConservative returns the lower of the MCV and Collision
// estimates. Following NIST SP 800-90B guidance, the minimum across estimators
// serves as a conservative bound suitable for security applications.
func EstimateMinEntropyConservative(data []byte) float64 {
	mcv := EstimateMCV(data)
	collision := EstimateCollision(data)

	if mcv < collision {
		return mcv
	}
	return collision
}
