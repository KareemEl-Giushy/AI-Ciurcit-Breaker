package inspector

import (
	"crypto/rand"
	"math"
	"strings"
	"testing"
)

func TestEntropyCalculator_UniformRandom(t *testing.T) {
	ec := NewEntropyCalculator()

	// Generate uniformly distributed bytes (all 256 byte values represented equally)
	buf := make([]byte, 256*100)
	for i := 0; i < len(buf); i++ {
		buf[i] = byte(i % 256)
	}

	ec.AddBytes(buf)

	entropy := ec.CumulativeEntropy()
	// Theoretical maximum Shannon entropy for 256 uniform values is log2(256) = 8.0
	if math.Abs(entropy-8.0) > 0.001 {
		t.Errorf("expected entropy ~8.0 for uniform distribution, got %.4f", entropy)
	}
}

func TestEntropyCalculator_SingleCharRepetition(t *testing.T) {
	ec := NewEntropyCalculator()

	repetitive := strings.Repeat("A", 500)
	ec.AddToken(repetitive)

	entropy := ec.CumulativeEntropy()
	// Theoretical entropy for single repeating character is 0.0
	if entropy != 0.0 {
		t.Errorf("expected entropy 0.0 for single repeated char, got %.4f", entropy)
	}

	if !ec.IsDegenerated(50, 1.5) {
		t.Errorf("expected degeneration detection for repeated stream")
	}
}

func TestEntropyCalculator_EnglishText(t *testing.T) {
	ec := NewEntropyCalculator()

	text := "The quick brown fox jumps over the lazy dog. Information theory provides fundamental limits on signal processing."
	ec.AddToken(text)

	entropy := ec.CumulativeEntropy()
	// Standard English text character entropy is typically between 3.5 and 4.8 bits/byte
	if entropy < 3.0 || entropy > 5.0 {
		t.Errorf("expected English text entropy between 3.0 and 5.0, got %.4f", entropy)
	}

	if ec.IsDegenerated(50, 1.5) {
		t.Errorf("did not expect natural English text to be flagged as degenerated")
	}
}

func TestEntropyCalculator_RollingWindowEviction(t *testing.T) {
	ec := NewEntropyCalculator()

	// 1. Ingest 1024 high-entropy random bytes
	randomBytes := make([]byte, RollingWindowSize)
	_, _ = rand.Read(randomBytes)
	ec.AddBytes(randomBytes)

	highEntropy := ec.RollingEntropy()
	if highEntropy < 6.0 {
		t.Errorf("expected high rolling entropy, got %.4f", highEntropy)
	}

	// 2. Ingest 1024 repetitive bytes, pushing out all random bytes
	repetitiveBytes := make([]byte, RollingWindowSize)
	for i := range repetitiveBytes {
		repetitiveBytes[i] = 'X'
	}
	ec.AddBytes(repetitiveBytes)

	collapsedEntropy := ec.RollingEntropy()
	if collapsedEntropy != 0.0 {
		t.Errorf("expected rolling entropy to collapse to 0.0 after eviction, got %.4f", collapsedEntropy)
	}
}

// Benchmark Zero-Allocation Ingestion on 64-byte chunks
func BenchmarkEntropyCalculator_AddBytes_64B(b *testing.B) {
	ec := NewEntropyCalculator()
	chunk := []byte("The reverse proxy forward request and calculate stream token entropy!")

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ec.AddBytes(chunk)
	}
}

// Benchmark Zero-Allocation Ingestion on 1KB chunks
func BenchmarkEntropyCalculator_AddBytes_1KB(b *testing.B) {
	ec := NewEntropyCalculator()
	chunk := make([]byte, 1024)
	for i := range chunk {
		chunk[i] = byte(i % 128)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ec.AddBytes(chunk)
	}
}

// Benchmark Zero-Allocation String Token Ingestion
func BenchmarkEntropyCalculator_AddToken(b *testing.B) {
	ec := NewEntropyCalculator()
	token := " entropy calculation token stream"

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		ec.AddToken(token)
	}
}

// Benchmark Cumulative Entropy Calculation
func BenchmarkEntropyCalculator_CumulativeEntropy(b *testing.B) {
	ec := NewEntropyCalculator()
	chunk := []byte("Streaming LLM chunk bytes benchmark calculation without allocations.")
	for i := 0; i < 50; i++ {
		ec.AddBytes(chunk)
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		_ = ec.CumulativeEntropy()
	}
}
