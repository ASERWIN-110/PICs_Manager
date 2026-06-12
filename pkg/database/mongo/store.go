package mongo

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
	"PICs_Manager/pkg/database"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/bits"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Store 是 database.Store 接口的MongoDB实现。
type Store struct {
	mu           sync.RWMutex
	client       *mongo.Client
	db           *mongo.Database
	series       *seriesStore
	media        *mediaStore
	mediaByType  map[string]*imageStore
	mediaTypeSeq []string
}

var _ database.Store = (*Store)(nil)

// seriesStore 封装了与 "series" 集合相关的所有操作。
type seriesStore struct {
	coll *mongo.Collection
}

// imageStore 封装单一媒体类型的物理集合。
type imageStore struct {
	mediaType string
	coll      *mongo.Collection
}

// mediaStore is an aggregate read view over all configured media collections.
// Writes must go through Store.Media(mediaType) so non-image media never lands
// in the legacy images collection.
type mediaStore struct {
	stores []*imageStore
	images *imageStore
}

const pHashMaxHammingDistance = 10
const pHashBucketCount = pHashMaxHammingDistance + 1

type pageCursor struct {
	Path      string `json:"path,omitempty"`
	FileName  string `json:"fileName,omitempty"`
	UpdatedAt int64  `json:"updatedAt,omitempty"`
	ID        string `json:"id"`
}

func encodePageCursor(cursor pageCursor) (string, error) {
	if cursor.ID == "" {
		return "", nil
	}
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodePageCursor(raw string) (pageCursor, error) {
	if strings.TrimSpace(raw) == "" {
		return pageCursor{}, nil
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return pageCursor{}, err
	}
	var cursor pageCursor
	if err := json.Unmarshal(payload, &cursor); err != nil {
		return pageCursor{}, err
	}
	if cursor.ID != "" {
		if _, err := primitive.ObjectIDFromHex(cursor.ID); err != nil {
			return pageCursor{}, err
		}
	}
	return cursor, nil
}

func trimCursorPage[T any](items []T, limit int, encodeNext func(T) (string, error)) ([]T, string, error) {
	if limit <= 0 || len(items) <= limit {
		return items, "", nil
	}
	nextCursor, err := encodeNext(items[limit-1])
	if err != nil {
		return nil, "", err
	}
	return items[:limit], nextCursor, nil
}

// NewStore 创建并返回一个新的 Store 实例，并建立与MongoDB的连接。
func NewStore(ctx context.Context, cfg *config.Config) (database.Store, error) {
	slog.Info("正在连接到 MongoDB...", "uri", redactMongoURI(cfg.Database.URI))
	clientCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	clientOpts := options.Client().ApplyURI(cfg.Database.URI)
	client, err := mongo.Connect(clientCtx, clientOpts)
	if err != nil {
		return nil, err
	}

	if err := client.Ping(clientCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, err
	}
	slog.Info("MongoDB 连接成功")

	db := client.Database(cfg.Database.Name)
	ss := &seriesStore{coll: db.Collection("series")}
	mediaByType, mediaSeq := buildMediaCollections(db, cfg)
	mediaStores := make([]*imageStore, 0, len(mediaSeq))
	for _, mediaType := range mediaSeq {
		mediaStores = append(mediaStores, mediaByType[mediaType])
	}

	store := &Store{
		client:       client,
		db:           db,
		series:       ss,
		media:        &mediaStore{stores: mediaStores, images: mediaByType["image"]},
		mediaByType:  mediaByType,
		mediaTypeSeq: mediaSeq,
	}
	return store, nil
}

func buildMediaCollections(db *mongo.Database, cfg *config.Config) (map[string]*imageStore, []string) {
	types := []string{"image"}
	if cfg != nil {
		for _, mediaType := range cfg.Scanner.MediaTypes {
			types = append(types, mediaType.Type)
		}
	}
	types = append(types, "video", "audio", "text")

	stores := make(map[string]*imageStore)
	seq := make([]string, 0, len(types))
	for _, typ := range types {
		mediaType := normalizedMediaType(typ)
		if mediaType == "" {
			continue
		}
		if _, exists := stores[mediaType]; exists {
			continue
		}
		stores[mediaType] = &imageStore{
			mediaType: mediaType,
			coll:      db.Collection(mediaCollectionName(mediaType)),
		}
		seq = append(seq, mediaType)
	}
	sort.Strings(seq)
	return stores, seq
}

func orderedMediaStores(stores map[string]*imageStore, seq []string) []*imageStore {
	ordered := make([]*imageStore, 0, len(seq))
	for _, mediaType := range seq {
		if store := stores[mediaType]; store != nil {
			ordered = append(ordered, store)
		}
	}
	return ordered
}

func normalizedMediaType(mediaType string) string {
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if mediaType == "" {
		return ""
	}
	re := regexp.MustCompile(`[^a-z0-9_]+`)
	mediaType = re.ReplaceAllString(mediaType, "_")
	mediaType = strings.Trim(mediaType, "_")
	return mediaType
}

func mediaCollectionName(mediaType string) string {
	switch normalizedMediaType(mediaType) {
	case "image":
		return "images"
	case "video":
		return "videos"
	case "audio":
		return "audios"
	case "text":
		return "texts"
	default:
		return "media_" + normalizedMediaType(mediaType)
	}
}

func redactMongoURI(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	username := parsed.User.Username()
	if username == "" {
		parsed.User = url.UserPassword("", "xxxxx")
		return parsed.String()
	}
	parsed.User = url.UserPassword(username, "xxxxx")
	return parsed.String()
}

func (s *Store) Series() database.SeriesStore {
	return s.series
}

func (s *Store) Images() database.ImageStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.media
}

func (s *Store) Media(mediaType string) database.ImageStore {
	mediaType = normalizedMediaType(mediaType)
	if mediaType == "" {
		mediaType = "unknown"
	}
	s.mu.RLock()
	if store, ok := s.mediaByType[mediaType]; ok {
		s.mu.RUnlock()
		return store
	}
	s.mu.RUnlock()

	s.mu.Lock()
	defer s.mu.Unlock()
	if store, ok := s.mediaByType[mediaType]; ok {
		return store
	}
	store := &imageStore{
		mediaType: mediaType,
		coll:      s.db.Collection(mediaCollectionName(mediaType)),
	}
	s.mediaByType[mediaType] = store
	s.mediaTypeSeq = append(s.mediaTypeSeq, mediaType)
	sort.Strings(s.mediaTypeSeq)
	s.media = &mediaStore{stores: orderedMediaStores(s.mediaByType, s.mediaTypeSeq), images: s.mediaByType["image"]}

	indexCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := ensureMediaCollectionIndexes(indexCtx, store); err != nil {
		slog.Error("为运行期新增媒体集合创建索引失败", "mediaType", mediaType, "collection", store.coll.Name(), "error", err)
	}
	return store
}

func (s *Store) Close(ctx context.Context) error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Disconnect(ctx)
}

func (s *Store) Diagnostics(ctx context.Context) (database.Diagnostics, error) {
	var diagnostics database.Diagnostics
	var err error
	if diagnostics.SeriesCount, err = s.series.coll.CountDocuments(ctx, bson.D{}); err != nil {
		return diagnostics, err
	}
	if diagnostics.SeriesWithThumbnail, err = s.series.coll.CountDocuments(ctx, bson.M{
		"thumbnail": bson.M{"$exists": true, "$ne": ""},
	}); err != nil {
		return diagnostics, err
	}
	if diagnostics.SeriesWithThumbnailFlag, err = s.series.coll.CountDocuments(ctx, bson.M{"hasThumbnail": true}); err != nil {
		return diagnostics, err
	}
	if diagnostics.SeriesMissingThumbnailFlag, err = s.series.coll.CountDocuments(ctx, bson.M{
		"hasThumbnail": bson.M{"$exists": false},
	}); err != nil {
		return diagnostics, err
	}
	for _, store := range s.mediaStores() {
		count, err := store.coll.CountDocuments(ctx, bson.D{})
		if err != nil {
			return diagnostics, err
		}
		diagnostics.MediaCount += count
	}
	imageStore := s.imageStore()
	if imageStore == nil {
		return diagnostics, fmt.Errorf("image media collection is not configured")
	}
	if diagnostics.ImageMediaCount, err = imageStore.coll.CountDocuments(ctx, bson.M{"mediaType": "image"}); err != nil {
		return diagnostics, err
	}
	if diagnostics.ImagesWithPHash, err = imageStore.coll.CountDocuments(ctx, bson.M{
		"mediaType":      "image",
		"perceptualHash": bson.M{"$ne": ""},
	}); err != nil {
		return diagnostics, err
	}
	if diagnostics.ImagesWithPHashBuckets, err = imageStore.coll.CountDocuments(ctx, bson.M{
		"mediaType":      "image",
		"perceptualHash": bson.M{"$ne": ""},
		"pHashBuckets.0": bson.M{"$exists": true},
	}); err != nil {
		return diagnostics, err
	}
	if diagnostics.ImagesMissingPHashBuckets, err = imageStore.coll.CountDocuments(ctx, bson.M{
		"mediaType":      "image",
		"perceptualHash": bson.M{"$ne": ""},
		"pHashBuckets.0": bson.M{"$exists": false},
	}); err != nil {
		return diagnostics, err
	}
	if diagnostics.SeriesIndexes, err = indexNames(ctx, s.series.coll); err != nil {
		return diagnostics, err
	}
	if diagnostics.ImageIndexes, err = indexNames(ctx, imageStore.coll); err != nil {
		return diagnostics, err
	}
	return diagnostics, nil
}

func (s *Store) mediaStores() []*imageStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return orderedMediaStores(s.mediaByType, s.mediaTypeSeq)
}

func (s *Store) imageStore() *imageStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.mediaByType["image"]
}

func indexNames(ctx context.Context, coll *mongo.Collection) ([]string, error) {
	cursor, err := coll.Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var indexes []struct {
		Name string `bson:"name"`
	}
	if err := cursor.All(ctx, &indexes); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(indexes))
	for _, index := range indexes {
		names = append(names, index.Name)
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) EnsureIndexes(ctx context.Context) error {
	slog.Info("正在确保数据库索引存在...")
	for _, store := range s.mediaStores() {
		if err := ensureMediaCollectionIndexes(ctx, store); err != nil {
			slog.Error("为媒体集合创建索引失败", "collection", store.coll.Name(), "error", err)
			return err
		}
		slog.Info("媒体集合索引已验证/创建。", "collection", store.coll.Name())
	}

	seriesIndexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "path", Value: 1}},
			Options: options.Index().SetName("idx_path"),
		},
		{
			Keys:    bson.D{{Key: "path", Value: 1}, {Key: "_id", Value: 1}},
			Options: options.Index().SetName("idx_path_id"),
		},
		{
			Keys:    bson.D{{Key: "updatedAt", Value: -1}, {Key: "_id", Value: -1}},
			Options: options.Index().SetName("idx_updatedat_id_desc"),
		},
		{
			Keys:    bson.D{{Key: "name", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("idx_name_unique"),
		},
	}
	if _, err := s.series.coll.Indexes().CreateMany(ctx, seriesIndexes); err != nil {
		slog.Error("为 series 集合创建索引失败", "error", err)
		return err
	}
	slog.Info("Series 集合索引已验证/创建。")
	if err := s.backfillSeriesThumbnailFlags(ctx); err != nil {
		return err
	}
	if err := s.backfillMediaThumbnailFlags(ctx); err != nil {
		return err
	}
	if err := s.backfillPHashBuckets(ctx); err != nil {
		return err
	}
	return nil
}

func ensureMediaCollectionIndexes(ctx context.Context, store *imageStore) error {
	imageIndexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "filePath", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("idx_filepath_unique"),
		},
		{
			Keys:    bson.D{{Key: "fileHash", Value: 1}},
			Options: options.Index().SetName("idx_filehash"),
		},
		{
			Keys:    bson.D{{Key: "seriesId", Value: 1}, {Key: "_id", Value: 1}},
			Options: options.Index().SetName("idx_seriesid_id"),
		},
		{
			Keys:    bson.D{{Key: "seriesId", Value: 1}, {Key: "fileName", Value: 1}, {Key: "_id", Value: 1}},
			Options: options.Index().SetName("idx_seriesid_filename_id"),
		},
		{
			Keys:    bson.D{{Key: "perceptualHash", Value: 1}},
			Options: options.Index().SetName("idx_phash"),
		},
		{
			Keys:    bson.D{{Key: "mediaType", Value: 1}, {Key: "pHashBuckets", Value: 1}},
			Options: options.Index().SetName("idx_phash_buckets_mediatype"),
		},
		{
			Keys:    bson.D{{Key: "mediaType", Value: 1}},
			Options: options.Index().SetName("idx_mediatype"),
		},
		{
			Keys:    bson.D{{Key: "seriesId", Value: 1}, {Key: "thumbnail", Value: 1}, {Key: "fileName", Value: 1}},
			Options: options.Index().SetName("idx_seriesid_thumbnail_filename"),
		},
		{
			Keys:    bson.D{{Key: "seriesId", Value: 1}, {Key: "fileName", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("idx_seriesid_filename_unique"),
		},
	}
	_, err := store.coll.Indexes().CreateMany(ctx, imageIndexes)
	return err
}

func (s *Store) backfillSeriesThumbnailFlags(ctx context.Context) error {
	withThumbnail, err := s.series.coll.UpdateMany(ctx, bson.M{
		"thumbnail": bson.M{"$exists": true, "$ne": ""},
	}, bson.M{"$set": bson.M{"hasThumbnail": true}})
	if err != nil {
		return fmt.Errorf("回填系列缩略图标记失败: %w", err)
	}
	withoutThumbnail, err := s.series.coll.UpdateMany(ctx, bson.M{
		"$or": bson.A{
			bson.M{"thumbnail": bson.M{"$exists": false}},
			bson.M{"thumbnail": ""},
		},
	}, bson.M{"$set": bson.M{"hasThumbnail": false}})
	if err != nil {
		return fmt.Errorf("回填系列缩略图标记失败: %w", err)
	}
	if withThumbnail.ModifiedCount+withoutThumbnail.ModifiedCount > 0 {
		slog.Info("已补齐系列缩略图标记", "withThumbnail", withThumbnail.ModifiedCount, "withoutThumbnail", withoutThumbnail.ModifiedCount)
	}
	return nil
}

func (s *Store) backfillMediaThumbnailFlags(ctx context.Context) error {
	for _, store := range s.mediaStores() {
		withThumbnail, err := store.coll.UpdateMany(ctx, bson.M{
			"thumbnail": bson.M{"$exists": true, "$ne": ""},
		}, bson.M{"$set": bson.M{"hasThumbnail": true}})
		if err != nil {
			return fmt.Errorf("回填媒体缩略图标记失败 %s: %w", store.coll.Name(), err)
		}
		withoutThumbnail, err := store.coll.UpdateMany(ctx, bson.M{
			"$or": bson.A{
				bson.M{"thumbnail": bson.M{"$exists": false}},
				bson.M{"thumbnail": ""},
			},
		}, bson.M{"$set": bson.M{"hasThumbnail": false}})
		if err != nil {
			return fmt.Errorf("回填媒体缩略图标记失败 %s: %w", store.coll.Name(), err)
		}
		if withThumbnail.ModifiedCount+withoutThumbnail.ModifiedCount > 0 {
			slog.Info("已补齐媒体缩略图标记", "collection", store.coll.Name(), "withThumbnail", withThumbnail.ModifiedCount, "withoutThumbnail", withoutThumbnail.ModifiedCount)
		}
	}
	return nil
}

func (s *Store) backfillPHashBuckets(ctx context.Context) error {
	filter := bson.M{
		"mediaType":      "image",
		"perceptualHash": bson.M{"$ne": ""},
		"pHashBuckets.0": bson.M{"$exists": false},
	}
	opts := options.Find().
		SetProjection(bson.M{"perceptualHash": 1}).
		SetBatchSize(500)
	imageStore := s.imageStore()
	if imageStore == nil {
		return fmt.Errorf("image media collection is not configured")
	}
	cursor, err := imageStore.coll.Find(ctx, filter, opts)
	if err != nil {
		return fmt.Errorf("查询待补齐 pHash bucket 的图片失败: %w", err)
	}
	defer cursor.Close(ctx)

	writes := make([]mongo.WriteModel, 0, 500)
	updated := 0
	flush := func() error {
		if len(writes) == 0 {
			return nil
		}
		if _, err := imageStore.coll.BulkWrite(ctx, writes, options.BulkWrite().SetOrdered(false)); err != nil {
			return err
		}
		updated += len(writes)
		writes = writes[:0]
		return nil
	}

	for cursor.Next(ctx) {
		var doc struct {
			ID             primitive.ObjectID `bson:"_id"`
			PerceptualHash string             `bson:"perceptualHash"`
		}
		if err := cursor.Decode(&doc); err != nil {
			return fmt.Errorf("解码 pHash bucket 回填记录失败: %w", err)
		}
		buckets, err := buildPHashBuckets(doc.PerceptualHash)
		if err != nil || len(buckets) == 0 {
			continue
		}
		writes = append(writes, mongo.NewUpdateOneModel().
			SetFilter(bson.M{"_id": doc.ID}).
			SetUpdate(bson.M{"$set": bson.M{"pHashBuckets": buckets}}))
		if len(writes) >= 500 {
			if err := flush(); err != nil {
				return fmt.Errorf("回填 pHash bucket 失败: %w", err)
			}
		}
	}
	if err := cursor.Err(); err != nil {
		return fmt.Errorf("遍历 pHash bucket 回填游标失败: %w", err)
	}
	if err := flush(); err != nil {
		return fmt.Errorf("回填 pHash bucket 失败: %w", err)
	}
	if updated > 0 {
		slog.Info("已补齐图片 pHash bucket", "count", updated)
	}
	return nil
}

func (s *seriesStore) GetByID(ctx context.Context, id primitive.ObjectID) (*models.Series, error) {
	var series models.Series
	err := s.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&series)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &series, nil
}

func (s *seriesStore) ListCursor(ctx context.Context, cursor string, limit int) ([]models.Series, int64, string, error) {
	decoded, err := decodePageCursor(cursor)
	if err != nil {
		return nil, 0, "", fmt.Errorf("无效分页游标: %w", err)
	}
	filter := bson.M{}
	if decoded.ID != "" {
		id, _ := primitive.ObjectIDFromHex(decoded.ID)
		filter["$or"] = bson.A{
			bson.M{"path": bson.M{"$gt": decoded.Path}},
			bson.M{"path": decoded.Path, "_id": bson.M{"$gt": id}},
		}
	}
	opts := options.Find().
		SetSort(bson.D{{Key: "path", Value: 1}, {Key: "_id", Value: 1}}).
		SetLimit(int64(limit + 1)).
		SetProjection(bson.M{"thumbnail": 0})
	cursorDB, err := s.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, 0, "", err
	}
	defer cursorDB.Close(ctx)

	var seriesList []models.Series
	if err = cursorDB.All(ctx, &seriesList); err != nil {
		return nil, 0, "", err
	}
	total, err := s.coll.CountDocuments(ctx, bson.D{})
	if err != nil {
		return nil, 0, "", err
	}
	seriesList, nextCursor, err := trimCursorPage(seriesList, limit, func(last models.Series) (string, error) {
		return encodePageCursor(pageCursor{Path: last.Path, ID: last.ID.Hex()})
	})
	if err != nil {
		return nil, 0, "", err
	}
	return seriesList, total, nextCursor, nil
}

func (i *imageStore) GetByID(ctx context.Context, id primitive.ObjectID) (*models.Image, error) {
	var image models.Image
	err := i.coll.FindOne(ctx, bson.M{"_id": id}).Decode(&image)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &image, nil
}

func (m *mediaStore) GetByID(ctx context.Context, id primitive.ObjectID) (*models.Image, error) {
	for _, store := range m.stores {
		item, err := store.GetByID(ctx, id)
		if err != nil {
			return nil, err
		}
		if item != nil {
			return item, nil
		}
	}
	return nil, nil
}

func (i *imageStore) ListBySeriesIDCursor(ctx context.Context, seriesID primitive.ObjectID, cursor string, limit int) ([]models.Image, int64, string, error) {
	decoded, err := decodePageCursor(cursor)
	if err != nil {
		return nil, 0, "", fmt.Errorf("无效分页游标: %w", err)
	}
	filter := bson.M{"seriesId": seriesID}
	if decoded.ID != "" {
		id, _ := primitive.ObjectIDFromHex(decoded.ID)
		filter["$or"] = bson.A{
			bson.M{"fileName": bson.M{"$gt": decoded.FileName}},
			bson.M{"fileName": decoded.FileName, "_id": bson.M{"$gt": id}},
		}
	}

	findOpts := options.Find().
		SetLimit(int64(limit + 1)).
		SetSort(bson.D{{Key: "fileName", Value: 1}, {Key: "_id", Value: 1}}).
		SetProjection(bson.M{"thumbnail": 0})
	cursorDB, err := i.coll.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, 0, "", err
	}
	defer cursorDB.Close(ctx)

	var imageList []models.Image
	if err = cursorDB.All(ctx, &imageList); err != nil {
		return nil, 0, "", err
	}
	total, err := i.coll.CountDocuments(ctx, bson.M{"seriesId": seriesID})
	if err != nil {
		return nil, 0, "", err
	}
	imageList, nextCursor, err := trimCursorPage(imageList, limit, func(last models.Image) (string, error) {
		return encodePageCursor(pageCursor{FileName: last.FileName, ID: last.ID.Hex()})
	})
	if err != nil {
		return nil, 0, "", err
	}
	return imageList, total, nextCursor, nil
}

func (m *mediaStore) ListBySeriesIDCursor(ctx context.Context, seriesID primitive.ObjectID, cursor string, limit int) ([]models.Image, int64, string, error) {
	if limit <= 0 {
		return []models.Image{}, 0, "", nil
	}
	var merged []models.Image
	var total int64
	for _, store := range m.stores {
		items, count, _, err := store.ListBySeriesIDCursor(ctx, seriesID, cursor, limit)
		if err != nil {
			return nil, 0, "", err
		}
		merged = append(merged, items...)
		total += count
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].FileName != merged[j].FileName {
			return merged[i].FileName < merged[j].FileName
		}
		return merged[i].ID.Hex() < merged[j].ID.Hex()
	})
	merged, nextCursor, err := trimCursorPage(merged, limit, func(last models.Image) (string, error) {
		return encodePageCursor(pageCursor{FileName: last.FileName, ID: last.ID.Hex()})
	})
	if err != nil {
		return nil, 0, "", err
	}
	return merged, total, nextCursor, nil
}

func (i *imageStore) FindSimilarByPHash(ctx context.Context, pHash string, limit int) ([]models.Image, error) {
	if limit <= 0 {
		return []models.Image{}, nil
	}
	target, err := parsePHash(pHash)
	if err != nil {
		return nil, fmt.Errorf("无效的感知哈希 %q: %w", pHash, err)
	}
	targetBuckets, err := buildPHashBuckets(pHash)
	if err != nil {
		return nil, fmt.Errorf("无法构建感知哈希检索桶 %q: %w", pHash, err)
	}

	filter := bson.M{
		"mediaType":      "image",
		"pHashBuckets":   bson.M{"$in": targetBuckets},
		"perceptualHash": bson.M{"$ne": ""},
	}
	findOpts := options.Find().SetProjection(bson.M{
		"seriesId":       1,
		"fileHash":       1,
		"mediaType":      1,
		"perceptualHash": 1,
		"fileName":       1,
		"filePath":       1,
		"createdAt":      1,
		"updatedAt":      1,
	}).SetBatchSize(500)
	cursor, err := i.coll.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	return nearestImagesByPHashCursor(ctx, cursor, target, limit)
}

func (m *mediaStore) FindSimilarByPHash(ctx context.Context, pHash string, limit int) ([]models.Image, error) {
	if m.images == nil {
		return []models.Image{}, nil
	}
	return m.images.FindSimilarByPHash(ctx, pHash, limit)
}

type pHashCandidate struct {
	image    models.Image
	distance int
}

func nearestImagesByPHashCursor(ctx context.Context, cursor *mongo.Cursor, target []byte, limit int) ([]models.Image, error) {
	candidates := make([]pHashCandidate, 0, limit)
	for cursor.Next(ctx) {
		var img models.Image
		if err := cursor.Decode(&img); err != nil {
			return nil, err
		}
		value, err := parsePHash(img.PerceptualHash)
		if err != nil {
			continue
		}
		distance := hammingDistanceBytes(target, value)
		if distance < 0 || distance > pHashMaxHammingDistance {
			continue
		}
		candidates = appendBoundedPHashCandidate(candidates, pHashCandidate{image: img, distance: distance}, limit)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	result := make([]models.Image, len(candidates))
	for idx := range candidates {
		result[idx] = candidates[idx].image
	}
	return result, nil
}

func appendBoundedPHashCandidate(candidates []pHashCandidate, candidate pHashCandidate, limit int) []pHashCandidate {
	candidates = append(candidates, candidate)
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].distance != candidates[j].distance {
			return candidates[i].distance < candidates[j].distance
		}
		return candidates[i].image.FileName < candidates[j].image.FileName
	})
	if len(candidates) > limit {
		candidates = candidates[:limit]
	}
	return candidates
}

func parsePHash(raw string) ([]byte, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return nil, fmt.Errorf("empty perceptual hash")
	}

	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		fields := strings.Fields(strings.Trim(value, "[]"))
		if len(fields) == 0 {
			return nil, fmt.Errorf("empty byte-list perceptual hash")
		}
		bytes := make([]byte, len(fields))
		for i, field := range fields {
			parsed, err := strconv.ParseUint(field, 10, 8)
			if err != nil {
				return nil, err
			}
			bytes[i] = byte(parsed)
		}
		return bytes, nil
	}

	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) > 0 {
		return decoded, nil
	}

	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return nil, err
	}
	bytes := make([]byte, 8)
	binary.BigEndian.PutUint64(bytes, parsed)
	return bytes, nil
}

func buildPHashBuckets(raw string) ([]string, error) {
	value, err := parsePHash(raw)
	if err != nil {
		return nil, err
	}
	if len(value) != 8 {
		return nil, fmt.Errorf("expected 64-bit perceptual hash, got %d bytes", len(value))
	}
	hash := binary.BigEndian.Uint64(value)
	buckets := make([]string, 0, pHashBucketCount)
	bitOffset := 0
	for idx := 0; idx < pHashBucketCount; idx++ {
		width := 64 / pHashBucketCount
		if idx < 64%pHashBucketCount {
			width++
		}
		shift := 64 - bitOffset - width
		mask := uint64(1<<width) - 1
		chunk := (hash >> shift) & mask
		buckets = append(buckets, fmt.Sprintf("%02d:%02x", idx, chunk))
		bitOffset += width
	}
	return buckets, nil
}

func hammingDistanceBytes(left, right []byte) int {
	if len(left) != len(right) {
		return -1
	}
	distance := 0
	for i := range left {
		distance += bits.OnesCount8(left[i] ^ right[i])
	}
	return distance
}

func (i *imageStore) Delete(ctx context.Context, id primitive.ObjectID) error {
	_, err := i.coll.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (m *mediaStore) Delete(ctx context.Context, id primitive.ObjectID) error {
	for _, store := range m.stores {
		if err := store.Delete(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (i *imageStore) CountBySeriesID(ctx context.Context, seriesID primitive.ObjectID) (int64, error) {
	filter := bson.M{"seriesId": seriesID}
	return i.coll.CountDocuments(ctx, filter)
}

func (m *mediaStore) CountBySeriesID(ctx context.Context, seriesID primitive.ObjectID) (int64, error) {
	var total int64
	for _, store := range m.stores {
		count, err := store.CountBySeriesID(ctx, seriesID)
		if err != nil {
			return 0, err
		}
		total += count
	}
	return total, nil
}

func (s *seriesStore) SearchByNameCursor(ctx context.Context, nameQuery string, cursor string, limit int) ([]models.Series, int64, string, error) {
	decoded, err := decodePageCursor(cursor)
	if err != nil {
		return nil, 0, "", fmt.Errorf("无效分页游标: %w", err)
	}
	filter := literalRegexFilter("name", nameQuery)
	if decoded.ID != "" {
		id, _ := primitive.ObjectIDFromHex(decoded.ID)
		updatedAt := time.Unix(0, decoded.UpdatedAt)
		filter["$or"] = bson.A{
			bson.M{"updatedAt": bson.M{"$lt": updatedAt}},
			bson.M{"updatedAt": updatedAt, "_id": bson.M{"$lt": id}},
		}
	}
	findOpts := options.Find().
		SetLimit(int64(limit + 1)).
		SetSort(bson.D{{Key: "updatedAt", Value: -1}, {Key: "_id", Value: -1}}).
		SetProjection(bson.M{"thumbnail": 0})
	cursorDB, err := s.coll.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, 0, "", err
	}
	defer cursorDB.Close(ctx)

	var seriesList []models.Series
	if err = cursorDB.All(ctx, &seriesList); err != nil {
		return nil, 0, "", err
	}
	total, err := s.coll.CountDocuments(ctx, literalRegexFilter("name", nameQuery))
	if err != nil {
		return nil, 0, "", err
	}
	seriesList, nextCursor, err := trimCursorPage(seriesList, limit, func(last models.Series) (string, error) {
		return encodePageCursor(pageCursor{UpdatedAt: last.UpdatedAt.UnixNano(), ID: last.ID.Hex()})
	})
	if err != nil {
		return nil, 0, "", err
	}
	return seriesList, total, nextCursor, nil
}

func literalRegexFilter(field, query string) bson.M {
	return bson.M{field: bson.M{"$regex": primitive.Regex{Pattern: regexp.QuoteMeta(query), Options: "i"}}}
}

func (i *imageStore) BulkWrite(ctx context.Context, models []mongo.WriteModel) error {
	if len(models) == 0 {
		return nil
	}

	opts := options.BulkWrite().SetOrdered(false)
	_, err := i.coll.BulkWrite(ctx, models, opts)
	if err != nil {
		slog.Error("imageStore BulkWrite 发生错误", "error", err)
		return err
	}
	return nil
}

func (m *mediaStore) BulkWrite(ctx context.Context, models []mongo.WriteModel) error {
	if len(models) == 0 {
		return nil
	}
	return fmt.Errorf("aggregate media store cannot bulk write; use media-specific collection")
}

func (s *seriesStore) BulkWrite(ctx context.Context, models []mongo.WriteModel) error {
	if len(models) == 0 {
		return nil
	}
	opts := options.BulkWrite().SetOrdered(false)
	_, err := s.coll.BulkWrite(ctx, models, opts)
	if err != nil {
		slog.Error("seriesStore BulkWrite 发生错误", "error", err)
		return err
	}
	return nil
}

func (s *seriesStore) FindManyByNames(ctx context.Context, names []string) ([]models.Series, []string, error) {
	if len(names) == 0 {
		return nil, nil, nil
	}

	filter := bson.M{"name": bson.M{"$in": names}}
	cursor, err := s.coll.Find(ctx, filter)
	if err != nil {
		return nil, nil, fmt.Errorf("批量查找系列失败: %w", err)
	}
	defer cursor.Close(ctx)

	var foundSeries []models.Series
	if err = cursor.All(ctx, &foundSeries); err != nil {
		return nil, nil, fmt.Errorf("解码批量查找结果失败: %w", err)
	}

	foundNamesSet := make(map[string]struct{}, len(foundSeries))
	for _, series := range foundSeries {
		foundNamesSet[series.Name] = struct{}{}
	}

	var notFoundNames []string
	for _, name := range names {
		if _, found := foundNamesSet[name]; !found {
			notFoundNames = append(notFoundNames, name)
		}
	}

	return foundSeries, notFoundNames, nil
}

func (i *imageStore) GetFirstThumbnailMedia(ctx context.Context, seriesID primitive.ObjectID) (*models.Image, error) {
	var image models.Image
	filter := bson.M{
		"seriesId":  seriesID,
		"thumbnail": bson.M{"$exists": true, "$ne": ""},
	}

	opts := options.FindOne().SetSort(bson.D{{Key: "fileName", Value: 1}})

	err := i.coll.FindOne(ctx, filter, opts).Decode(&image)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &image, nil
}

func (m *mediaStore) GetFirstThumbnailMedia(ctx context.Context, seriesID primitive.ObjectID) (*models.Image, error) {
	if m.images == nil {
		return nil, nil
	}
	return m.images.GetFirstThumbnailMedia(ctx, seriesID)
}

// DropAllCollections 删除当前数据库中的所有已知集合，主要用于测试环境的重置。
func (s *Store) DropAllCollections(ctx context.Context) error {
	slog.Warn("正在删除所有集合...", "database", s.db.Name())
	if err := s.series.coll.Drop(ctx); err != nil {
		slog.Error("删除 series 集合失败", "error", err)
		// 即使出错也继续尝试删除其他集合
	}
	for _, store := range s.mediaStores() {
		if err := store.coll.Drop(ctx); err != nil {
			slog.Error("删除媒体集合失败", "collection", store.coll.Name(), "error", err)
			return err
		}
	}
	slog.Info("所有集合已成功删除。")
	return nil
}

func (s *seriesStore) GetByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Series, error) {
	if len(ids) == 0 {
		return []models.Series{}, nil
	}

	filter := bson.M{"_id": bson.M{"$in": ids}}

	cursor, err := s.coll.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var series []models.Series
	if err = cursor.All(ctx, &series); err != nil {
		return nil, err
	}

	return series, nil
}

// GetAllBySeriesID 获取指定系列ID下的媒体文档，但不读取缩略图大字段。
func (i *imageStore) GetAllBySeriesID(ctx context.Context, seriesID primitive.ObjectID) ([]models.Image, error) {
	filter := bson.M{"seriesId": seriesID}

	opts := options.Find().
		SetSort(bson.D{{Key: "fileName", Value: 1}}).
		SetProjection(bson.M{"thumbnail": 0})
	cursor, err := i.coll.Find(ctx, filter, opts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	var images []models.Image
	if err = cursor.All(ctx, &images); err != nil {
		return nil, err
	}

	return images, nil
}

func (m *mediaStore) GetAllBySeriesID(ctx context.Context, seriesID primitive.ObjectID) ([]models.Image, error) {
	var merged []models.Image
	for _, store := range m.stores {
		items, err := store.GetAllBySeriesID(ctx, seriesID)
		if err != nil {
			return nil, err
		}
		merged = append(merged, items...)
	}
	sort.SliceStable(merged, func(i, j int) bool {
		if merged[i].FileName != merged[j].FileName {
			return merged[i].FileName < merged[j].FileName
		}
		return merged[i].ID.Hex() < merged[j].ID.Hex()
	})
	return merged, nil
}
