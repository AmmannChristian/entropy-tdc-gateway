package entropy

import (
	"bytes"
	"testing"
)

func TestVonNeumannExtractor_EmptyInput(t *testing.T) {
	if result := VonNeumannExtractor(nil); result != nil {
		t.Fatalf("expected nil, got %v", result)
	}
}

func TestVonNeumannExtractor_AllZeros(t *testing.T) {
	input := []byte{0x00, 0x00, 0x00}
	result := VonNeumannExtractor(input)
	if len(result) != 0 {
		t.Fatalf("expected zero-length output, got %d", len(result))
	}
}

func TestVonNeumannExtractor_AllOnes(t *testing.T) {
	input := []byte{0xFF, 0xFF}
	result := VonNeumannExtractor(input)
	if len(result) != 0 {
		t.Fatalf("expected zero-length output, got %d", len(result))
	}
}

func TestVonNeumannExtractor_AlternatingBits(t *testing.T) {
	input := []byte{0xAA, 0xAA}
	expected := []byte{0xFF}

	result := VonNeumannExtractor(input)
	if len(result) != len(expected) {
		t.Fatalf("expected %d byte output, got %d", len(expected), len(result))
	}
	if !bytes.Equal(result, expected) {
		t.Fatalf("expected %v, got %v", expected, result)
	}
}

func TestPackBits(t *testing.T) {
	testCases := []struct {
		name     string
		bits     []byte
		expected []byte
	}{
		{
			name:     "empty",
			bits:     nil,
			expected: nil,
		},
		{
			name:     "singleZero",
			bits:     []byte{0},
			expected: []byte{0x00},
		},
		{
			name:     "singleOne",
			bits:     []byte{1},
			expected: []byte{0x80},
		},
		{
			name:     "eightOnes",
			bits:     []byte{1, 1, 1, 1, 1, 1, 1, 1},
			expected: []byte{0xFF},
		},
		{
			name:     "eightAlternating",
			bits:     []byte{1, 0, 1, 0, 1, 0, 1, 0},
			expected: []byte{0xAA},
		},
		{
			name:     "nineOnes",
			bits:     []byte{1, 1, 1, 1, 1, 1, 1, 1, 1},
			expected: []byte{0xFF, 0x80},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result := packBits(testCase.bits)
			if !bytes.Equal(result, testCase.expected) {
				t.Fatalf("expected %v, got %v", testCase.expected, result)
			}
		})
	}
}

func TestSHA256Conditioner(t *testing.T) {
	if result := SHA256Conditioner(nil); result != nil {
		t.Fatalf("expected nil for empty input, got %v", result)
	}

	input := []byte("entropy")
	repeated := []byte("entropy")
	different := []byte("entropy!")

	first := SHA256Conditioner(input)
	second := SHA256Conditioner(repeated)
	third := SHA256Conditioner(different)

	if len(first) != 32 {
		t.Fatalf("expected 32 byte hash, got %d", len(first))
	}

	if !bytes.Equal(first, second) {
		t.Fatal("expected deterministic hash output for identical inputs")
	}

	if bytes.Equal(first, third) {
		t.Fatal("expected different inputs to produce different hashes")
	}
}

func TestDualStageWhitening(t *testing.T) {
	const sampleSize = 14720
	raw := make([]byte, sampleSize)
	for index := 0; index < len(raw); index++ {
		raw[index] = byte((index * 7919) % 256)
	}

	result := DualStageWhitening(raw)

	if len(result) != 32 {
		t.Fatalf("expected 32 bytes, got %d", len(result))
	}

	zeroBuffer := make([]byte, len(result))
	if bytes.Equal(result, zeroBuffer) {
		t.Fatal("expected non-zero whitening output")
	}
}

func TestParityPreprocess(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		exp  []byte
	}{
		{"short input", []byte{0xFF}, nil},
		{"xor pairs", []byte{0x0F, 0xF0, 0xAA}, []byte{0xFF, 0x5A}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := ParityPreprocess(tc.in); !bytes.Equal(got, tc.exp) {
				t.Fatalf("for %s expected %v, got %v", tc.name, tc.exp, got)
			}
		})
	}
}

func BenchmarkVonNeumann_14720B(b *testing.B) {
	const sampleSize = 14720
	input := make([]byte, sampleSize)
	for index := 0; index < len(input); index++ {
		input[index] = byte((index * 7919) % 256)
	}

	b.ResetTimer()
	for range b.N {
		_ = VonNeumannExtractor(input)
	}
}

func BenchmarkDualStage_14720B(b *testing.B) {
	const sampleSize = 14720
	input := make([]byte, sampleSize)
	for index := 0; index < len(input); index++ {
		input[index] = byte((index * 7919) % 256)
	}

	b.ResetTimer()
	for range b.N {
		_ = DualStageWhitening(input)
	}
}

func BenchmarkParityPreprocess_14720B(b *testing.B) {
	const sampleSize = 14720
	input := make([]byte, sampleSize)
	for index := 0; index < len(input); index++ {
		input[index] = byte((index * 7919) % 256)
	}

	b.ResetTimer()
	for range b.N {
		_ = ParityPreprocess(input)
	}
}

// TestComputeWhiteningStats covers edge cases for stats computation
func TestComputeWhiteningStats(t *testing.T) {
	t.Run("zero bytes", func(t *testing.T) {
		stats := ComputeWhiteningStats(0, 0)
		if stats.InputBits != 0 || stats.OutputBits != 0 || stats.ExtractionRate != 0 {
			t.Errorf("expected all zeros, got input=%d output=%d rate=%f",
				stats.InputBits, stats.OutputBits, stats.ExtractionRate)
		}
	})

	t.Run("input only", func(t *testing.T) {
		stats := ComputeWhiteningStats(16, 0)
		if stats.InputBits != 128 || stats.OutputBits != 0 || stats.ExtractionRate != 0 {
			t.Errorf("expected 128/0/0, got %d/%d/%f",
				stats.InputBits, stats.OutputBits, stats.ExtractionRate)
		}
	})

	t.Run("perfect 1:1 ratio", func(t *testing.T) {
		stats := ComputeWhiteningStats(32, 32)
		if stats.InputBits != 256 || stats.OutputBits != 256 || stats.ExtractionRate != 1.0 {
			t.Errorf("expected 256/256/1.0, got %d/%d/%f",
				stats.InputBits, stats.OutputBits, stats.ExtractionRate)
		}
	})

	t.Run("compression 2:1", func(t *testing.T) {
		stats := ComputeWhiteningStats(64, 32)
		expectedRate := 0.5
		if stats.InputBits != 512 || stats.OutputBits != 256 || stats.ExtractionRate != expectedRate {
			t.Errorf("expected 512/256/0.5, got %d/%d/%f",
				stats.InputBits, stats.OutputBits, stats.ExtractionRate)
		}
	})
}

// TestToBitCount covers toBitCount edge cases
func TestToBitCount(t *testing.T) {
	tests := []struct {
		name     string
		input    int
		expected uint32
	}{
		{"zero bytes", 0, 0},
		{"negative bytes", -10, 0},
		{"one byte", 1, 8},
		{"sixteen bytes", 16, 128},
		{"1000 bytes", 1000, 8000},
		{"overflow boundary", int(4294967295/8) + 1, 4294967295},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := toBitCount(tc.input)
			if result != tc.expected {
				t.Errorf("toBitCount(%d) = %d, want %d", tc.input, result, tc.expected)
			}
		})
	}
}
