package entropy

import (
	"crypto/sha256"
	"math"

	protocolbuffer "entropy-tdc-gateway/pkg/pb"
)

// VonNeumannExtractor applies the classic Von Neumann debiasing algorithm to the
// provided raw bytes. It interprets the bitstream in big-endian order within each
// byte and emits packed bytes containing the conditioned bits.
func VonNeumannExtractor(input []byte) []byte {
	if len(input) == 0 {
		return nil
	}

	extractedBits := make([]byte, 0, len(input))
	for _, value := range input {
		for pairIndex := 0; pairIndex < 4; pairIndex++ {
			firstBitIndex := 7 - (pairIndex * 2)
			secondBitIndex := firstBitIndex - 1

			firstBit := (value >> firstBitIndex) & 1
			secondBit := (value >> secondBitIndex) & 1

			if firstBit == secondBit {
				continue
			}

			if firstBit == 0 {
				extractedBits = append(extractedBits, 0)
			} else {
				extractedBits = append(extractedBits, 1)
			}
		}
	}

	if len(extractedBits) == 0 {
		return nil
	}

	return packBits(extractedBits)
}

// SHA256Conditioner condenses the provided entropy input via SHA-256 as
// described in NIST SP 800-90B, returning exactly 32 bytes when the input is not
// empty.
func SHA256Conditioner(input []byte) []byte {
	if len(input) == 0 {
		return nil
	}

	sum := sha256.Sum256(input)
	return sum[:]
}

// DualStageWhitening applies a two-stage conditioning pipeline to raw TDC bytes.
// The first stage performs parity preprocessing (adjacent-byte XOR fold) to
// reduce local correlations. The second stage applies SHA-256 conditioning per
// NIST SP 800-90B, producing exactly 32 bytes of output when the input contains
// at least two bytes. Returns nil if the input is too short.
func DualStageWhitening(rawTDC []byte) []byte {
	stageOne := ParityPreprocess(rawTDC)
	if len(stageOne) == 0 {
		return nil
	}

	return SHA256Conditioner(stageOne)
}

// ParityPreprocess performs a lightweight XOR fold across adjacent byte pairs,
// producing len(input)-1 output bytes. This serves as the first stage of the
// dual-stage whitening pipeline, reducing local correlations before SHA-256
// conditioning.
func ParityPreprocess(input []byte) []byte {
	if len(input) < 2 {
		return nil
	}

	output := make([]byte, len(input)-1)
	for index := 0; index < len(output); index++ {
		output[index] = input[index] ^ input[index+1]
	}

	return output
}

// packBits compacts individual bit values (0 or 1) into a big-endian byte
// stream. The first bit in the slice becomes the most significant bit of the
// first byte. Any remaining bits are zero-padded in the least significant
// positions of the final byte.
func packBits(bits []byte) []byte {
	if len(bits) == 0 {
		return nil
	}

	output := make([]byte, (len(bits)+7)/8)
	for index, bit := range bits {
		if bit != 0 {
			byteIndex := index / 8
			bitPosition := 7 - (index % 8)
			output[byteIndex] |= 1 << bitPosition
		}
	}

	return output
}

// ComputeWhiteningStats computes whitening statistics from the given input and
// output byte counts. It returns a WhiteningStats protocol buffer message
// containing bit-level totals and the extraction rate. When inputBytes is zero,
// all fields are set to zero.
func ComputeWhiteningStats(inputBytes, outputBytes int) *protocolbuffer.WhiteningStats {
	if inputBytes == 0 {
		return &protocolbuffer.WhiteningStats{
			InputBits:      0,
			OutputBits:     0,
			ExtractionRate: 0,
		}
	}

	inputBits := toBitCount(inputBytes)
	outputBits := toBitCount(outputBytes)

	var extractionRate float64
	if inputBits > 0 {
		extractionRate = float64(outputBits) / float64(inputBits)
	}

	return &protocolbuffer.WhiteningStats{
		InputBits:      inputBits,
		OutputBits:     outputBits,
		ExtractionRate: extractionRate,
	}
}

// toBitCount converts a byte count to a bit count, clamping to math.MaxUint32
// on overflow. Non-positive inputs return zero.
func toBitCount(bytesCount int) uint32 {
	if bytesCount <= 0 {
		return 0
	}
	const maxBytesForUint32 = int(math.MaxUint32 / 8)
	if bytesCount >= maxBytesForUint32 {
		return math.MaxUint32
	}
	return uint32(bytesCount) << 3
}
