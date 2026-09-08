package tailnet

import "testing"

func TestLocalBaseURLUsesHostPortFormatting(t *testing.T) {
	got := "http://" + formatHostPort("100.64.0.10", "8080")
	want := "http://100.64.0.10:8080"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}
