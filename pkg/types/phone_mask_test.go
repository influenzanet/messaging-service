package types

import "testing"

func TestMaskPhone(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"+391234567890", "+39***7890"},
		{"+447911123456", "+44***3456"},
		// Six characters cannot keep both ends and hide anything, so all of it is hidden.
		{"+44207", "***"},
		{"+1555", "***"},
		{"", "***"},
		// The shortest number that keeps both ends.
		{"+441234", "+44***1234"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := MaskPhone(tt.input); got != tt.expected {
				t.Errorf("MaskPhone(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}
