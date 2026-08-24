package inspector

import (
	"math"
)

// RollingWindowSize is the fixed size of the circular ring buffer for localized entropy tracking.
const RollingWindowSize = 1024

// EntropyCalculator is a zero-allocation Shannon entropy engine designed for incoming chunk streams.
// It maintains both cumulative stream statistics and a fixed sliding ring buffer without allocating heap memory.
type EntropyCalculator struct {
	// Cumulative frequency distribution
	cumFreq  [256]uint32
	cumTotal uint32

	// Rolling window distribution for localized loop/collapse detection
	ringBuf  [RollingWindowSize]byte
	ringFreq [256]uint16
	ringHead uint16
	ringLen  uint16
}

// NewEntropyCalculator creates a new initialized EntropyCalculator on the stack.
func NewEntropyCalculator() EntropyCalculator {
	return EntropyCalculator{}
}

// AddBytes ingests raw byte chunks into the entropy calculator in-place with 0 heap allocations.
func (ec *EntropyCalculator) AddBytes(chunk []byte) {
	for i := 0; i < len(chunk); i++ {
		b := chunk[i]

		// 1. Update cumulative frequency
		ec.cumFreq[b]++
		ec.cumTotal++

		// 2. Update rolling ring buffer
		if ec.ringLen < RollingWindowSize {
			ec.ringBuf[ec.ringHead] = b
			ec.ringFreq[b]++
			ec.ringHead = (ec.ringHead + 1) % RollingWindowSize
			ec.ringLen++
		} else {
			// Evict oldest byte
			old := ec.ringBuf[ec.ringHead]
			ec.ringFreq[old]--

			// Insert new byte
			ec.ringBuf[ec.ringHead] = b
			ec.ringFreq[b]++
			ec.ringHead = (ec.ringHead + 1) % RollingWindowSize
		}
	}
}

// AddToken ingests a string token delta into the entropy engine without string-to-byte conversion allocations.
func (ec *EntropyCalculator) AddToken(token string) {
	for i := 0; i < len(token); i++ {
		b := token[i]

		// 1. Update cumulative frequency
		ec.cumFreq[b]++
		ec.cumTotal++

		// 2. Update rolling ring buffer
		if ec.ringLen < RollingWindowSize {
			ec.ringBuf[ec.ringHead] = b
			ec.ringFreq[b]++
			ec.ringHead = (ec.ringHead + 1) % RollingWindowSize
			ec.ringLen++
		} else {
			old := ec.ringBuf[ec.ringHead]
			ec.ringFreq[old]--

			ec.ringBuf[ec.ringHead] = b
			ec.ringFreq[b]++
			ec.ringHead = (ec.ringHead + 1) % RollingWindowSize
		}
	}
}

// CumulativeEntropy calculates the Shannon entropy across all ingested bytes:
// H(X) = log2(N) - (1/N) * sum(c_i * log2(c_i))
// Returns bits per byte in range [0.0, 8.0]. Zero allocations.
func (ec *EntropyCalculator) CumulativeEntropy() float64 {
	if ec.cumTotal == 0 {
		return 0.0
	}

	n := float64(ec.cumTotal)
	sum := 0.0

	for i := 0; i < 256; i++ {
		c := ec.cumFreq[i]
		if c > 0 {
			fc := float64(c)
			sum += fc * math.Log2(fc)
		}
	}

	entropy := math.Log2(n) - (sum / n)
	if entropy < 0.0 {
		return 0.0
	}
	return entropy
}

// RollingEntropy calculates Shannon entropy across the active rolling window (up to 1024 bytes).
// Returns bits per byte in range [0.0, 8.0]. Zero allocations.
func (ec *EntropyCalculator) RollingEntropy() float64 {
	if ec.ringLen == 0 {
		return 0.0
	}

	n := float64(ec.ringLen)
	sum := 0.0

	for i := 0; i < 256; i++ {
		c := ec.ringFreq[i]
		if c > 0 {
			fc := float64(c)
			sum += fc * math.Log2(fc)
		}
	}

	entropy := math.Log2(n) - (sum / n)
	if entropy < 0.0 {
		return 0.0
	}
	return entropy
}

// TotalBytes returns the total number of bytes processed by the calculator.
func (ec *EntropyCalculator) TotalBytes() uint32 {
	return ec.cumTotal
}

// IsDegenerated checks if the stream shows signs of repetitive degeneration / infinite loop collapse.
// Triggers when at least minSamples bytes have been processed and localized rolling entropy falls below threshold.
func (ec *EntropyCalculator) IsDegenerated(minSamples int, threshold float64) bool {
	if int(ec.ringLen) < minSamples {
		return false
	}
	return ec.RollingEntropy() < threshold
}

// CalculateStringEntropy calculates the Shannon entropy in bits/byte for a given string with 0 heap allocations.
func CalculateStringEntropy(s string) float64 {
	if len(s) == 0 {
		return 0.0
	}
	ec := NewEntropyCalculator()
	ec.AddToken(s)
	return ec.CumulativeEntropy()
}

// Reset clears all counters and buffers in-place for reuse.
func (ec *EntropyCalculator) Reset() {
	for i := 0; i < 256; i++ {
		ec.cumFreq[i] = 0
		ec.ringFreq[i] = 0
	}
	ec.cumTotal = 0
	ec.ringHead = 0
	ec.ringLen = 0
}
