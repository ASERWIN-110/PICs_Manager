package mongo

import (
	"PICs_Manager/internal/models"
	"encoding/binary"
	"testing"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
)

func TestRedactMongoURI(t *testing.T) {
	raw := "mongodb://dev_user:secret@localhost:27017/?authSource=admin"
	got := redactMongoURI(raw)
	if got != "mongodb://dev_user:xxxxx@localhost:27017/?authSource=admin" {
		t.Fatalf("unexpected redacted URI: %s", got)
	}
	if got == raw {
		t.Fatal("URI was not redacted")
	}
}

func TestLiteralRegexFilterEscapesUserQuery(t *testing.T) {
	filter := literalRegexFilter("fileName", "A+B[1].png")
	field, ok := filter["fileName"].(bson.M)
	if !ok {
		t.Fatalf("unexpected filter shape: %#v", filter)
	}
	regex, ok := field["$regex"].(primitive.Regex)
	if !ok {
		t.Fatalf("unexpected regex shape: %#v", field)
	}
	if regex.Pattern != `A\+B\[1\]\.png` {
		t.Fatalf("expected escaped regex pattern, got %q", regex.Pattern)
	}
	if regex.Options != "i" {
		t.Fatalf("expected case-insensitive option, got %q", regex.Options)
	}
}

func TestAppendBoundedPHashCandidateSortsAndAppliesLimit(t *testing.T) {
	candidates := []pHashCandidate{}
	candidates = appendBoundedPHashCandidate(candidates, pHashCandidate{image: models.Image{FileName: "far.png"}, distance: 9}, 2)
	candidates = appendBoundedPHashCandidate(candidates, pHashCandidate{image: models.Image{FileName: "b.png"}, distance: 1}, 2)
	candidates = appendBoundedPHashCandidate(candidates, pHashCandidate{image: models.Image{FileName: "a.png"}, distance: 1}, 2)

	if len(candidates) != 2 {
		t.Fatalf("expected bounded candidate list length 2, got %d", len(candidates))
	}
	if candidates[0].image.FileName != "a.png" || candidates[1].image.FileName != "b.png" {
		t.Fatalf("expected distance and filename sorted candidates, got %+v", candidates)
	}
}

func TestParsePHashSupportsHexAndLegacyByteList(t *testing.T) {
	hexHash, err := parsePHash("b1f3b121268d05a1")
	if err != nil {
		t.Fatalf("parse hex pHash returned error: %v", err)
	}
	legacyHash, err := parsePHash("[177 243 177 33 38 141 5 161]")
	if err != nil {
		t.Fatalf("parse legacy pHash returned error: %v", err)
	}
	if hammingDistanceBytes(hexHash, legacyHash) != 0 {
		t.Fatalf("expected hex and legacy pHash to be identical: %v %v", hexHash, legacyHash)
	}
}

func TestBuildPHashBucketsKeepsNearHashesDiscoverable(t *testing.T) {
	target := "b1f3b121268d05a1"
	targetBytes, err := parsePHash(target)
	if err != nil {
		t.Fatalf("parse target pHash returned error: %v", err)
	}
	nearValue := binary.BigEndian.Uint64(targetBytes) ^ uint64(0x3ff)
	nearBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(nearBytes, nearValue)

	targetBuckets, err := buildPHashBuckets(target)
	if err != nil {
		t.Fatalf("build target buckets returned error: %v", err)
	}
	nearBuckets, err := buildPHashBuckets(hexString(nearBytes))
	if err != nil {
		t.Fatalf("build near buckets returned error: %v", err)
	}

	seen := make(map[string]struct{}, len(targetBuckets))
	for _, bucket := range targetBuckets {
		seen[bucket] = struct{}{}
	}
	for _, bucket := range nearBuckets {
		if _, ok := seen[bucket]; ok {
			return
		}
	}
	t.Fatalf("expected near hash to share at least one bucket: target=%v near=%v", targetBuckets, nearBuckets)
}

func TestPageCursorRoundTrip(t *testing.T) {
	id := primitive.NewObjectID()
	encoded, err := encodePageCursor(pageCursor{Path: "Series/A", ID: id.Hex()})
	if err != nil {
		t.Fatalf("encodePageCursor returned error: %v", err)
	}
	decoded, err := decodePageCursor(encoded)
	if err != nil {
		t.Fatalf("decodePageCursor returned error: %v", err)
	}
	if decoded.Path != "Series/A" || decoded.ID != id.Hex() {
		t.Fatalf("unexpected decoded cursor: %+v", decoded)
	}
}

func TestTrimCursorPageDoesNotReturnFalseNextCursor(t *testing.T) {
	items := []models.Series{
		{ID: primitive.NewObjectID(), Path: "A"},
		{ID: primitive.NewObjectID(), Path: "B"},
	}

	trimmed, nextCursor, err := trimCursorPage(items, 2, func(last models.Series) (string, error) {
		return encodePageCursor(pageCursor{Path: last.Path, ID: last.ID.Hex()})
	})
	if err != nil {
		t.Fatalf("trimCursorPage returned error: %v", err)
	}
	if len(trimmed) != 2 {
		t.Fatalf("expected 2 items, got %d", len(trimmed))
	}
	if nextCursor != "" {
		t.Fatalf("expected no next cursor, got %q", nextCursor)
	}
}

func TestTrimCursorPageUsesLastReturnedItemForNextCursor(t *testing.T) {
	items := []models.Series{
		{ID: primitive.NewObjectID(), Path: "A"},
		{ID: primitive.NewObjectID(), Path: "B"},
		{ID: primitive.NewObjectID(), Path: "C"},
	}

	trimmed, nextCursor, err := trimCursorPage(items, 2, func(last models.Series) (string, error) {
		return encodePageCursor(pageCursor{Path: last.Path, ID: last.ID.Hex()})
	})
	if err != nil {
		t.Fatalf("trimCursorPage returned error: %v", err)
	}
	if len(trimmed) != 2 {
		t.Fatalf("expected 2 items, got %d", len(trimmed))
	}
	decoded, err := decodePageCursor(nextCursor)
	if err != nil {
		t.Fatalf("decodePageCursor returned error: %v", err)
	}
	if decoded.Path != "B" || decoded.ID != items[1].ID.Hex() {
		t.Fatalf("unexpected next cursor: %+v", decoded)
	}
}

func hexString(value []byte) string {
	const table = "0123456789abcdef"
	out := make([]byte, len(value)*2)
	for i, b := range value {
		out[i*2] = table[b>>4]
		out[i*2+1] = table[b&0x0f]
	}
	return string(out)
}
