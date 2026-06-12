package main

import (
	"context"
	"testing"
)

func TestScanRequiresDatabaseMatchesScannerModeNormalization(t *testing.T) {
	tests := []struct {
		name string
		mode string
		want bool
	}{
		{name: "default full", mode: "", want: true},
		{name: "full", mode: "full", want: true},
		{name: "classify only", mode: "classifyOnly", want: false},
		{name: "trimmed classify only", mode: " classifyOnly ", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := scanRequiresDatabase(tt.mode); got != tt.want {
				t.Fatalf("scanRequiresDatabase(%q) = %v, want %v", tt.mode, got, tt.want)
			}
		})
	}
}

func TestFetchCursorPageWalksToRequestedPage(t *testing.T) {
	var cursors []string
	items, total, nextCursor, err := fetchCursorPage(context.Background(), 3, "", func(cursor string) ([]string, int64, string, error) {
		cursors = append(cursors, cursor)
		switch len(cursors) {
		case 1:
			return []string{"page1"}, 10, "cursor-2", nil
		case 2:
			return []string{"page2"}, 10, "cursor-3", nil
		default:
			return []string{"page3"}, 10, "cursor-4", nil
		}
	})
	if err != nil {
		t.Fatalf("fetchCursorPage returned error: %v", err)
	}
	if total != 10 || nextCursor != "cursor-4" {
		t.Fatalf("unexpected metadata: total=%d nextCursor=%q", total, nextCursor)
	}
	if len(items) != 1 || items[0] != "page3" {
		t.Fatalf("unexpected items: %v", items)
	}
	wantCursors := []string{"", "cursor-2", "cursor-3"}
	for i := range wantCursors {
		if cursors[i] != wantCursors[i] {
			t.Fatalf("cursor[%d]=%q want %q; all=%v", i, cursors[i], wantCursors[i], cursors)
		}
	}
}

func TestFetchCursorPageUsesInitialCursor(t *testing.T) {
	var cursors []string
	items, _, _, err := fetchCursorPage(context.Background(), 1, "cursor-direct", func(cursor string) ([]string, int64, string, error) {
		cursors = append(cursors, cursor)
		return []string{"page"}, 1, "", nil
	})
	if err != nil {
		t.Fatalf("fetchCursorPage returned error: %v", err)
	}
	if len(items) != 1 || items[0] != "page" {
		t.Fatalf("unexpected items: %v", items)
	}
	if len(cursors) != 1 || cursors[0] != "cursor-direct" {
		t.Fatalf("expected one direct cursor call, got %v", cursors)
	}
}
