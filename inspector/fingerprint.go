package inspector

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/go-dedup/simhash"
)

var defaultSimhash = simhash.NewSimhash()

// NormalizeJSONArgs parses JSON arguments and re-marshals them into canonical form.
func NormalizeJSONArgs(args string) string {
	normalized := strings.TrimSpace(args)
	var parsed any
	if err := json.Unmarshal([]byte(args), &parsed); err == nil {
		if canonical, err := json.Marshal(parsed); err == nil {
			normalized = string(canonical)
		}
	}
	return normalized
}

// ComputeToolFingerprint calculates a deterministic SHA256 cryptographic hash
// over the tool name and its arguments: SHA256(tool_name + ":" + normalized_args).
func ComputeToolFingerprint(toolName string, args string) string {
	toolName = strings.TrimSpace(toolName)
	normalizedArgs := NormalizeJSONArgs(args)

	h := sha256.New()
	h.Write([]byte(toolName))
	h.Write([]byte(":"))
	h.Write([]byte(normalizedArgs))

	return hex.EncodeToString(h.Sum(nil))
}

// ComputeSimHash generates a 64-bit Locality Sensitive Hash (SimHash)
// over the tool name and its arguments using github.com/go-dedup/simhash.
// Unlike cryptographic hashes (SHA256), SimHash produces fingerprints where
// small mutations (e.g. slight prompt variations or retry counters) result in
// a very small Hamming distance (differing bit count).
func ComputeSimHash(toolName string, args string) uint64 {
	toolName = strings.TrimSpace(strings.ToLower(toolName))
	normalizedArgs := NormalizeJSONArgs(args)
	combined := toolName + " " + normalizedArgs

	if strings.TrimSpace(combined) == "" {
		return 0
	}
	shingles := defaultSimhash.Shingle(3, [][]byte{[]byte(combined)})
	return defaultSimhash.SimhashBytes(shingles)
}

// SimHashHexString formats a 64-bit SimHash into a canonical 16-character hexadecimal string.
func SimHashHexString(h uint64) string {
	return fmt.Sprintf("0x%016x", h)
}

// HammingDistance calculates the number of differing bits between two 64-bit SimHashes.
func HammingDistance(h1, h2 uint64) int {
	return int(simhash.Compare(h1, h2))
}

// SimHashSimilarity converts Hamming distance between two 64-bit SimHashes into a similarity score [0.0, 1.0].
func SimHashSimilarity(h1, h2 uint64) float64 {
	dist := HammingDistance(h1, h2)
	return 1.0 - (float64(dist) / 64.0)
}

// AreToolCallsSimilarSimHash evaluates if two tool calls share the same tool name
// and have a SimHash Hamming distance <= maxDistance (default: 3 bits).
func AreToolCallsSimilarSimHash(tool1, args1, tool2, args2 string, maxDistance int) (bool, int, float64) {
	if strings.TrimSpace(strings.ToLower(tool1)) != strings.TrimSpace(strings.ToLower(tool2)) {
		return false, 64, 0.0
	}

	h1 := ComputeSimHash(tool1, args1)
	h2 := ComputeSimHash(tool2, args2)

	dist := HammingDistance(h1, h2)
	similarity := SimHashSimilarity(h1, h2)

	return dist <= maxDistance, dist, similarity
}

// JaccardSimilarity computes the Jaccard Similarity index J(A, B) = |A ∩ B| / |A ∪ B|
// using character 3-grams (k-shingles) over normalized string arguments.
// Returns a similarity score in the range [0.0, 1.0].
func JaccardSimilarity(s1, s2 string) float64 {
	s1 = strings.TrimSpace(s1)
	s2 = strings.TrimSpace(s2)

	if s1 == s2 {
		return 1.0
	}
	if s1 == "" || s2 == "" {
		return 0.0
	}

	const k = 3
	if len(s1) < k || len(s2) < k {
		set1 := make(map[string]struct{})
		for _, r := range s1 {
			set1[string(r)] = struct{}{}
		}
		set2 := make(map[string]struct{})
		for _, r := range s2 {
			set2[string(r)] = struct{}{}
		}

		intersection := 0
		for item := range set1 {
			if _, exists := set2[item]; exists {
				intersection++
			}
		}
		union := len(set1) + len(set2) - intersection
		if union == 0 {
			return 0.0
		}
		return float64(intersection) / float64(union)
	}

	// Generate 3-gram k-shingles
	set1 := make(map[string]struct{}, len(s1)-k+1)
	for i := 0; i <= len(s1)-k; i++ {
		set1[s1[i:i+k]] = struct{}{}
	}

	set2 := make(map[string]struct{}, len(s2)-k+1)
	for i := 0; i <= len(s2)-k; i++ {
		set2[s2[i:i+k]] = struct{}{}
	}

	intersection := 0
	for shingle := range set1 {
		if _, exists := set2[shingle]; exists {
			intersection++
		}
	}

	union := len(set1) + len(set2) - intersection
	if union == 0 {
		return 0.0
	}

	return float64(intersection) / float64(union)
}

// AreToolCallsSimilar evaluates whether two tool calls target the same tool name
// and have arguments exceeding the specified Jaccard similarity threshold.
func AreToolCallsSimilar(tool1, args1, tool2, args2 string, threshold float64) (bool, float64) {
	if strings.TrimSpace(strings.ToLower(tool1)) != strings.TrimSpace(strings.ToLower(tool2)) {
		return false, 0.0
	}

	norm1 := NormalizeJSONArgs(args1)
	norm2 := NormalizeJSONArgs(args2)

	similarity := JaccardSimilarity(norm1, norm2)
	return similarity >= threshold, similarity
}
