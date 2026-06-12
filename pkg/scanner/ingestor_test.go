package scanner

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
	"PICs_Manager/pkg/database"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

func TestMediaWorkerUsesProvidedSeriesContext(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "sample.png")
	writeTestPNG(t, imagePath)

	ingestor := &mongoIngestor{
		logger: log.New(io.Discard, "", 0),
	}

	series := &models.Series{
		ID:   primitive.NewObjectID(),
		Name: "sample",
		Path: dir,
	}

	jobs := make(chan mediaJob, 1)
	results := make(chan mediaResult, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go ingestor.mediaWorker(&wg, t.Context(), jobs, results)

	jobs <- mediaJob{filePath: imagePath, series: series}
	close(jobs)
	wg.Wait()
	close(results)

	var count int
	for result := range results {
		if result.writeModel == nil {
			t.Fatal("expected a write model")
		}
		count++
	}
	if count != 1 {
		t.Fatalf("expected 1 write model, got %d", count)
	}
}

func TestNewIngestorRejectsMissingDatabaseStore(t *testing.T) {
	_, err := NewIngestor(t.TempDir(), nil, scannerConfigForPNG(), 1, 1)
	if err == nil {
		t.Fatal("expected missing database store error")
	}
	if !strings.Contains(err.Error(), "数据库存储未初始化") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMediaFilesInDirIncludesSameNameVariantsAsRelativePaths(t *testing.T) {
	dir := t.TempDir()
	mainPath := filepath.Join(dir, "Dup_1.png")
	variantPath := filepath.Join(dir, sameNameDirName, "Dup_1", "abc123", "Dup_1.png")
	writeTestPNG(t, mainPath)
	if err := os.MkdirAll(filepath.Dir(variantPath), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	writeTestPNGWithColor(t, variantPath, color.RGBA{G: 255, A: 255})

	files, err := mediaFilesInDir(dir, scannerConfigForPNG())
	if err != nil {
		t.Fatalf("mediaFilesInDir returned error: %v", err)
	}
	got := map[string]bool{}
	for _, file := range files {
		got[file] = true
	}
	if !got["Dup_1.png"] {
		t.Fatal("main file missing")
	}
	if !got[".same-name/Dup_1/abc123/Dup_1.png"] {
		t.Fatalf("same-name variant missing: %v", files)
	}
}

func TestMediaWorkerUsesRelativeFileNameForSameNameVariant(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, sameNameDirName, "Dup_1", "abc123", "Dup_1.png")
	if err := os.MkdirAll(filepath.Dir(imagePath), 0755); err != nil {
		t.Fatalf("MkdirAll returned error: %v", err)
	}
	writeTestPNG(t, imagePath)

	ingestor := &mongoIngestor{
		logger:     log.New(io.Discard, "", 0),
		scannerCfg: scannerConfigForPNG(),
	}

	series := &models.Series{
		ID:   primitive.NewObjectID(),
		Name: "Dup",
		Path: dir,
	}

	relativeName := ".same-name/Dup_1/abc123/Dup_1.png"
	jobs := make(chan mediaJob, 1)
	results := make(chan mediaResult, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go ingestor.mediaWorker(&wg, t.Context(), jobs, results)

	jobs <- mediaJob{filePath: imagePath, fileName: relativeName, series: series}
	close(jobs)
	wg.Wait()
	close(results)

	result, ok := <-results
	if !ok {
		t.Fatal("expected one result")
	}
	if result.err != nil {
		t.Fatalf("mediaWorker returned error: %v", result.err)
	}
	if result.writeModel == nil {
		t.Fatal("expected a write model")
	}
	if _, ok := <-results; ok {
		t.Fatal("expected exactly one result")
	}
}

func TestMediaWorkerAcceptsNonImageWithoutPHashBuckets(t *testing.T) {
	dir := t.TempDir()
	textPath := filepath.Join(dir, "Notes_1.txt")
	if err := os.WriteFile(textPath, []byte("plain text"), 0644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}

	ingestor := &mongoIngestor{
		logger: log.New(io.Discard, "", 0),
		scannerCfg: config.ScannerConfig{
			MediaTypes: []config.MediaTypeConfig{
				{
					Type:         "text",
					Extensions:   []string{".txt"},
					FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
				},
			},
		},
	}
	series := &models.Series{
		ID:   primitive.NewObjectID(),
		Name: "Notes",
		Path: dir,
	}

	jobs := make(chan mediaJob, 1)
	results := make(chan mediaResult, 1)
	var wg sync.WaitGroup
	wg.Add(1)
	go ingestor.mediaWorker(&wg, t.Context(), jobs, results)
	jobs <- mediaJob{filePath: textPath, series: series}
	close(jobs)
	wg.Wait()
	close(results)

	result, ok := <-results
	if !ok {
		t.Fatal("expected one result")
	}
	if result.err != nil {
		t.Fatalf("mediaWorker returned error: %v", result.err)
	}
	if result.writeModel == nil {
		t.Fatal("expected a write model")
	}
}

func TestUpdateAllSeriesMetadataReturnsCountErrors(t *testing.T) {
	wantErr := errors.New("count failed")
	series := &models.Series{ID: primitive.NewObjectID(), Name: "Series"}
	ingestor := &mongoIngestor{
		dbStore:    fakeMetadataStore{images: fakeImageStore{countErr: wantErr}},
		logger:     log.New(io.Discard, "", 0),
		numWorkers: 1,
	}

	err := ingestor.updateAllSeriesMetadata(t.Context(), map[string]*models.Series{"series": series})

	if !errors.Is(err, wantErr) {
		t.Fatalf("expected count error, got %v", err)
	}
	if !strings.Contains(err.Error(), "Series") {
		t.Fatalf("expected series name in error, got %v", err)
	}
}

func TestUpdateAllSeriesMetadataUsesThumbnailMedia(t *testing.T) {
	series := &models.Series{ID: primitive.NewObjectID(), Name: "Series"}
	seriesStore := &fakeSeriesStore{}
	db := fakeMetadataStore{
		series: seriesStore,
		images: fakeImageStore{
			count: 1,
			thumbnailMedia: &models.Image{
				Thumbnail: "data:image/jpeg;base64,abcd",
			},
		},
	}
	ingestor := &mongoIngestor{
		dbStore:    db,
		logger:     log.New(io.Discard, "", 0),
		numWorkers: 1,
	}

	err := ingestor.updateAllSeriesMetadata(t.Context(), map[string]*models.Series{"series": series})

	if err != nil {
		t.Fatalf("updateAllSeriesMetadata returned error: %v", err)
	}
	if seriesStore.writeCount != 1 {
		t.Fatalf("expected one series metadata write, got %d", seriesStore.writeCount)
	}
}

type fakeMetadataStore struct {
	series *fakeSeriesStore
	images fakeImageStore
}

func (s fakeMetadataStore) Series() database.SeriesStore {
	if s.series != nil {
		return s.series
	}
	return &fakeSeriesStore{}
}
func (s fakeMetadataStore) Images() database.ImageStore         { return s.images }
func (s fakeMetadataStore) EnsureIndexes(context.Context) error { return nil }
func (s fakeMetadataStore) Diagnostics(context.Context) (database.Diagnostics, error) {
	return database.Diagnostics{}, nil
}
func (s fakeMetadataStore) DropAllCollections(context.Context) error { return nil }
func (s fakeMetadataStore) Close(context.Context) error              { return nil }

type fakeSeriesStore struct {
	writeCount int
}

func (s fakeSeriesStore) GetByID(context.Context, primitive.ObjectID) (*models.Series, error) {
	return nil, nil
}
func (s fakeSeriesStore) ListCursor(context.Context, string, int) ([]models.Series, int64, string, error) {
	return nil, 0, "", nil
}
func (s fakeSeriesStore) SearchByNameCursor(context.Context, string, string, int) ([]models.Series, int64, string, error) {
	return nil, 0, "", nil
}
func (s *fakeSeriesStore) BulkWrite(_ context.Context, models []mongo.WriteModel) error {
	s.writeCount += len(models)
	return nil
}
func (s *fakeSeriesStore) FindManyByNames(context.Context, []string) ([]models.Series, []string, error) {
	return nil, nil, nil
}
func (s *fakeSeriesStore) GetByIDs(context.Context, []primitive.ObjectID) ([]models.Series, error) {
	return nil, nil
}

type fakeImageStore struct {
	count          int64
	countErr       error
	thumbnailMedia *models.Image
}

func (s fakeImageStore) GetByID(context.Context, primitive.ObjectID) (*models.Image, error) {
	return nil, nil
}
func (s fakeImageStore) ListBySeriesIDCursor(context.Context, primitive.ObjectID, string, int) ([]models.Image, int64, string, error) {
	return nil, 0, "", nil
}
func (s fakeImageStore) FindSimilarByPHash(context.Context, string, int) ([]models.Image, error) {
	return nil, nil
}
func (s fakeImageStore) Delete(context.Context, primitive.ObjectID) error { return nil }
func (s fakeImageStore) CountBySeriesID(context.Context, primitive.ObjectID) (int64, error) {
	return s.count, s.countErr
}
func (s fakeImageStore) BulkWrite(context.Context, []mongo.WriteModel) error { return nil }
func (s fakeImageStore) GetFirstThumbnailMedia(context.Context, primitive.ObjectID) (*models.Image, error) {
	return s.thumbnailMedia, nil
}
func (s fakeImageStore) GetAllBySeriesID(context.Context, primitive.ObjectID) ([]models.Image, error) {
	return nil, nil
}

func scannerConfigForPNG() config.ScannerConfig {
	return config.ScannerConfig{
		MediaTypes: []config.MediaTypeConfig{
			{
				Type:         "image",
				Extensions:   []string{".png"},
				FilePatterns: []string{`^(.*?)_(\d+)(\.[a-zA-Z0-9_]+)?$`},
			},
		},
	}
}

func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	writeTestPNGWithColor(t, path, color.RGBA{R: 255, A: 255})
}

func writeTestPNGWithColor(t *testing.T, path string, c color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, c)
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("Create returned error: %v", err)
	}
	defer file.Close()

	if err := png.Encode(file, img); err != nil {
		t.Fatalf("Encode returned error: %v", err)
	}
}
