package validation

import "testing"

func TestFrequencyTestEmptyInputs(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
	}{
		{name: "nil"},
		{name: "empty", in: []byte{}},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			ratio, ok := FrequencyTest(tc.in)
			if ratio != 0 {
				t.Fatalf("expected ratio 0, got %f", ratio)
			}
			if ok {
				t.Fatalf("expected ok=false, got true")
			}
		})
	}
}

func TestFrequencyTestNonEmpty(t *testing.T) {
	t.Parallel()

	ratio, ok := FrequencyTest([]byte{0b10101010})
	if ratio != 0.5 {
		t.Fatalf("expected ratio 0.5, got %f", ratio)
	}
	if !ok {
		t.Fatalf("expected ok=true for balanced byte")
	}

	ratio, ok = FrequencyTest([]byte{0xff, 0x00})
	if ratio != 0.5 {
		t.Fatalf("expected ratio 0.5, got %f", ratio)
	}
	if !ok {
		t.Fatalf("expected ok=true for balanced multi byte")
	}
}

func TestFrequencyTestBiased(t *testing.T) {
	t.Parallel()

	ratio, ok := FrequencyTest([]byte{0xff})
	if ratio <= 0.55 {
		t.Fatalf("expected ratio > 0.55, got %f", ratio)
	}
	if ok {
		t.Fatalf("expected ok=false for biased data")
	}
}

func TestRunsTestHandlesEmpty(t *testing.T) {
	t.Parallel()

	count, ok := RunsTest(nil)
	if count != 0 {
		t.Fatalf("expected 0 runs for empty input, got %d", count)
	}
	if ok {
		t.Fatalf("expected ok=false for empty input")
	}
}

func TestRunsTestStructuredDataPasses(t *testing.T) {
	t.Parallel()

	count, ok := RunsTest([]byte{0x33, 0x33})
	if !ok {
		t.Fatalf("expected structured pattern to pass, got ok=false")
	}
	if count == 0 {
		t.Fatalf("expected runs count > 0")
	}
}

func TestRunsTestBiasedFails(t *testing.T) {
	t.Parallel()

	count, ok := RunsTest([]byte{0xff, 0xff})
	if ok {
		t.Fatalf("expected biased data to fail runs test")
	}
	if count == 0 {
		t.Fatalf("expected runs count to be recorded")
	}
}
