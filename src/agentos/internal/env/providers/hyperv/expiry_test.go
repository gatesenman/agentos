package hyperv

import (
	"strings"
	"testing"
	"time"
)

func TestParseSystemTime(t *testing.T) {
	// Captured from Insider build 27902: EA-07-08-00-0B-00-12-00-09-00-2C-00-00-00-00-00
	b := []byte{0xEA, 0x07, 0x08, 0x00, 0x0B, 0x00, 0x12, 0x00, 0x09, 0x00, 0x2C, 0x00, 0x00, 0x00, 0x00, 0x00}
	tm, ok := parseSystemTime(b)
	if !ok || !tm.Equal(time.Date(2026, 8, 11, 18, 9, 44, 0, time.UTC)) {
		t.Fatalf("got %v %v", tm, ok)
	}
	if _, ok := parseSystemTime(make([]byte, 16)); ok {
		t.Fatal("zero time must be rejected")
	}
	if r := expiredBuildReason(tm, "27902", tm.Add(-time.Hour)); r != "" {
		t.Fatal("not expired yet")
	}
	if r := expiredBuildReason(tm, "27902", tm.Add(time.Hour)); !strings.Contains(r, "27902") || !strings.Contains(r, "0x80090006") {
		t.Fatal(r)
	}
	if m := friendlyStartError("... 无效签名 (0x80090006)"); !strings.Contains(m, "Insider") {
		t.Fatal(m)
	}
}
