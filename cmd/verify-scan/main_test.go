package main

import (
	"PICs_Manager/internal/models"
	"PICs_Manager/pkg/database"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

func TestCopyFileCopiesContentAndMode(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src.txt")
	dst := filepath.Join(root, "nested", "dst.txt")
	if err := os.WriteFile(src, []byte("content"), 0640); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	if err := copyFile(src, dst, 0640); err != nil {
		t.Fatalf("copyFile returned error: %v", err)
	}

	content, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	if string(content) != "content" {
		t.Fatalf("unexpected copied content: %q", content)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("expected mode 0640, got %v", info.Mode().Perm())
	}
}

func TestBuildManifestDoesNotDeadlockWhenManyFilesFailHashing(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 4; i++ {
		linkPath := filepath.Join(root, fmt.Sprintf("missing-%d.bin", i))
		if err := os.Symlink(filepath.Join(root, "does-not-exist"), linkPath); err != nil {
			if errors.Is(err, os.ErrPermission) {
				t.Skipf("symlink not permitted: %v", err)
			}
			t.Fatalf("Symlink returned error: %v", err)
		}
	}

	done := make(chan error, 1)
	go func() {
		_, err := buildManifest(root, []mediaTypeRule{{typ: "binary", extensions: map[string]struct{}{".bin": {}}}}, 1)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected hashing errors")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("buildManifest deadlocked while reporting hash errors")
	}
}

func TestCountDatabaseMediaUsesDiagnostics(t *testing.T) {
	store := verifyFakeStore{
		diagnostics: database.Diagnostics{
			MediaCount:  12,
			SeriesCount: 3,
		},
	}

	mediaCount, seriesCount, err := countDatabaseMedia(context.Background(), store)
	if err != nil {
		t.Fatalf("countDatabaseMedia returned error: %v", err)
	}
	if mediaCount != 12 || seriesCount != 3 {
		t.Fatalf("unexpected counts: media=%d series=%d", mediaCount, seriesCount)
	}
}

type verifyFakeStore struct {
	diagnostics database.Diagnostics
}

func (s verifyFakeStore) Series() database.SeriesStore             { return verifyPanicSeriesStore{} }
func (s verifyFakeStore) Images() database.ImageStore              { return verifyPanicImageStore{} }
func (s verifyFakeStore) Media(string) database.ImageStore         { return verifyPanicImageStore{} }
func (s verifyFakeStore) EnsureIndexes(context.Context) error      { return nil }
func (s verifyFakeStore) DropAllCollections(context.Context) error { return nil }
func (s verifyFakeStore) Close(context.Context) error              { return nil }
func (s verifyFakeStore) Diagnostics(context.Context) (database.Diagnostics, error) {
	return s.diagnostics, nil
}

type verifyPanicSeriesStore struct{}

func (s verifyPanicSeriesStore) GetByID(context.Context, primitive.ObjectID) (*models.Series, error) {
	panic("GetByID should not be called")
}
func (s verifyPanicSeriesStore) ListCursor(context.Context, string, int) ([]models.Series, int64, string, error) {
	panic("ListCursor should not be called")
}
func (s verifyPanicSeriesStore) SearchByNameCursor(context.Context, string, string, int) ([]models.Series, int64, string, error) {
	panic("SearchByNameCursor should not be called")
}
func (s verifyPanicSeriesStore) BulkWrite(context.Context, []mongo.WriteModel) error {
	panic("BulkWrite should not be called")
}
func (s verifyPanicSeriesStore) FindManyByNames(context.Context, []string) ([]models.Series, []string, error) {
	panic("FindManyByNames should not be called")
}
func (s verifyPanicSeriesStore) GetByIDs(context.Context, []primitive.ObjectID) ([]models.Series, error) {
	panic("GetByIDs should not be called")
}

type verifyPanicImageStore struct{}

func (s verifyPanicImageStore) GetByID(context.Context, primitive.ObjectID) (*models.Image, error) {
	panic("GetByID should not be called")
}
func (s verifyPanicImageStore) ListBySeriesIDCursor(context.Context, primitive.ObjectID, string, int) ([]models.Image, int64, string, error) {
	panic("ListBySeriesIDCursor should not be called")
}
func (s verifyPanicImageStore) FindSimilarByPHash(context.Context, string, int) ([]models.Image, error) {
	panic("FindSimilarByPHash should not be called")
}
func (s verifyPanicImageStore) Delete(context.Context, primitive.ObjectID) error {
	panic("Delete should not be called")
}
func (s verifyPanicImageStore) CountBySeriesID(context.Context, primitive.ObjectID) (int64, error) {
	panic("CountBySeriesID should not be called")
}
func (s verifyPanicImageStore) BulkWrite(context.Context, []mongo.WriteModel) error {
	panic("BulkWrite should not be called")
}
func (s verifyPanicImageStore) GetFirstThumbnailMedia(context.Context, primitive.ObjectID) (*models.Image, error) {
	panic("GetFirstThumbnailMedia should not be called")
}
func (s verifyPanicImageStore) GetAllBySeriesID(context.Context, primitive.ObjectID) ([]models.Image, error) {
	panic("GetAllBySeriesID should not be called")
}
