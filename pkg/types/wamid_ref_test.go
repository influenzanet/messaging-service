package types

import (
	"encoding/base64"
	"regexp"
	"strings"
	"testing"
)

// Meta's id for a message to +391234567890: the number is inside it, base64-encoded.
const sampleWAMID = "wamid.HBgLMzkxMjM0NTY3ODkwFQIAERgSQUJDREVGMDEyMzQ1Njc4OQA="

// The reason the id is not logged as it is.
// Fails if the sample stops being a realistic id, which would make the tests below prove nothing.
func TestSampleWAMIDCarriesTheNumber(t *testing.T) {
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sampleWAMID, "wamid."))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(decoded), "391234567890") {
		t.Fatalf("the sample id does not carry the number: %q", decoded)
	}
}

// The reference is pinned: user-management computes it for the verification codes it sends,
// and the two services' logs only match while both give the same reference for the same id.
// Fails if the hash, its length or its encoding changes here without the same change there.
func TestWAMIDLogRefIsTheSameInEveryService(t *testing.T) {
	if got := WAMIDLogRef(sampleWAMID); got != "a2205e2a010d" {
		t.Fatalf("WAMIDLogRef = %q, want a2205e2a010d (user-management pins the same value)", got)
	}
}

// Fails if the reference holds any part of the id, so the number, or if two ids that differ
// in one character share a reference, or if an empty id gets a reference that looks real.
func TestWAMIDLogRef(t *testing.T) {
	ref := WAMIDLogRef(sampleWAMID)
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(ref) {
		t.Errorf("reference %q is not 12 hex digits", ref)
	}
	for _, part := range []string{"391234567890", "1234567890", "HBgL", "wamid"} {
		if strings.Contains(ref, part) {
			t.Errorf("reference %q holds %q", ref, part)
		}
	}
	other := strings.Replace(sampleWAMID, "OQA=", "OQB=", 1)
	if WAMIDLogRef(other) == ref {
		t.Errorf("two different ids share the reference %q", ref)
	}
	if got := WAMIDLogRef(""); got != "none" {
		t.Errorf("empty id: %q, want none", got)
	}
}

// The archive keeps the full hash, so that two messages cannot share a key, and the log
// reference is its prefix, so that a row is found from a log line.
// Fails if the hash is shortened, holds any part of the id, is not the prefix of the log
// reference, or is made up for an empty id (which would let every row without an id match a
// status that carries none).
func TestWAMIDHash(t *testing.T) {
	hash := WAMIDHash(sampleWAMID)
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(hash) {
		t.Fatalf("hash %q is not the 64 hex digits of a SHA-256", hash)
	}
	if !strings.HasPrefix(hash, WAMIDLogRef(sampleWAMID)) {
		t.Errorf("the log reference %q is not the prefix of the archived hash %q", WAMIDLogRef(sampleWAMID), hash)
	}
	for _, part := range []string{"391234567890", "HBgL", "wamid"} {
		if strings.Contains(hash, part) {
			t.Errorf("hash %q holds %q", hash, part)
		}
	}
	if got := WAMIDHash(""); got != "" {
		t.Errorf("empty id hashed to %q, want none", got)
	}
}
