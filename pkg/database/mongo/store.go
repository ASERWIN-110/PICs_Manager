package mongo

import (
	"PICs_Manager/config"
	"PICs_Manager/internal/models"
	"PICs_Manager/pkg/database"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"regexp"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Store 是 database.Store 接口的MongoDB实现。
type Store struct {
	db     *mongo.Database
	series *seriesStore
	images *imageStore
}

// 确保 Store 实现了 database.Store 接口 (编译时检查)
var _ database.Store = (*Store)(nil)

// seriesStore 封装了与 "series" 集合相关的所有操作。
type seriesStore struct {
	coll *mongo.Collection
}

// GetAllSeries 返回数据库中所有系列的原始文档列表。
func (s *seriesStore) GetAllSeries(ctx context.Context) ([]models.Series, error) {
	var seriesList []models.Series
	// 使用空的 filter (bson.D{}) 来匹配所有文档
	cursor, err := s.coll.Find(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &seriesList); err != nil {
		return nil, err
	}
	return seriesList, nil
}

// imageStore 封装了与 "images" 集合相关的所有操作。
type imageStore struct {
	coll *mongo.Collection
}

// NewStore 创建并返回一个新的 Store 实例，并建立与MongoDB的连接。
func NewStore(ctx context.Context, cfg *config.Config) (database.Store, error) {
	slog.Info("正在连接到 MongoDB...", "uri", cfg.Database.URI)
	clientCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	clientOpts := options.Client().ApplyURI(cfg.Database.URI)
	client, err := mongo.Connect(clientCtx, clientOpts)
	if err != nil {
		return nil, err
	}

	if err := client.Ping(clientCtx, nil); err != nil {
		return nil, err
	}
	slog.Info("MongoDB 连接成功")

	db := client.Database(cfg.Database.Name)
	ss := &seriesStore{coll: db.Collection("series")}
	is := &imageStore{coll: db.Collection("images")}

	store := &Store{
		db:     db,
		series: ss,
		images: is,
	}
	return store, nil
}

func (s *Store) Series() database.SeriesStore {
	return s.series
}

func (s *Store) Images() database.ImageStore {
	return s.images
}

func (s *Store) EnsureIndexes(ctx context.Context) error {
	slog.Info("正在确保数据库索引存在...")
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
			Keys:    bson.D{{Key: "perceptualHash", Value: 1}},
			Options: options.Index().SetName("idx_phash"),
		},

		{
			Keys:    bson.D{{Key: "seriesId", Value: 1}, {Key: "fileName", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("idx_seriesid_filename_unique"),
		},
	}
	if _, err := s.images.coll.Indexes().CreateMany(ctx, imageIndexes); err != nil {
		slog.Error("为 images 集合创建索引失败", "error", err)
		return err
	}
	slog.Info("Images 集合索引已验证/创建。")

	seriesIndexes := []mongo.IndexModel{
		{
			Keys:    bson.D{{Key: "path", Value: 1}},
			Options: options.Index().SetName("idx_path"),
		},
		{
			Keys:    bson.D{{Key: "name", Value: 1}},
			Options: options.Index().SetUnique(true).SetName("idx_name_unique").SetDefaultLanguage("none"),
		},
	}
	if _, err := s.series.coll.Indexes().CreateMany(ctx, seriesIndexes); err != nil {
		slog.Error("为 series 集合创建索引失败", "error", err)
		return err
	}
	slog.Info("Series 集合索引已验证/创建。")
	return nil
}

// --- seriesStore 方法实现 ---

func (s *seriesStore) Create(ctx context.Context, series *models.Series) error {
	series.CreatedAt = time.Now()
	series.UpdatedAt = time.Now()
	_, err := s.coll.InsertOne(ctx, series)
	return err
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

func (s *seriesStore) GetByPath(ctx context.Context, path string) (*models.Series, error) {
	var series models.Series
	err := s.coll.FindOne(ctx, bson.M{"path": path}).Decode(&series)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &series, nil
}

// List 使用聚合管道获取系列列表，并包含每个系列的第一张图片作为封面
func (s *seriesStore) List(ctx context.Context, page, limit int) ([]models.Series, int64, error) {
	var seriesList []models.Series
	skip := (page - 1) * limit

	pipeline := mongo.Pipeline{
		bson.D{{Key: "$sort", Value: bson.D{{Key: "path", Value: 1}}}},
		bson.D{{Key: "$skip", Value: int64(skip)}},
		bson.D{{Key: "$limit", Value: int64(limit)}},
		bson.D{{Key: "$lookup", Value: bson.D{
			{Key: "from", Value: "images"},
			{Key: "localField", Value: "_id"},
			{Key: "foreignField", Value: "seriesId"},
			{Key: "pipeline", Value: mongo.Pipeline{
				bson.D{{Key: "$sort", Value: bson.D{{Key: "fileName", Value: 1}}}},
				bson.D{{Key: "$limit", Value: 1}},
			}},
			{Key: "as", Value: "coverImage"},
		}}},
		bson.D{{Key: "$addFields", Value: bson.D{
			{Key: "coverImage", Value: bson.D{{Key: "$arrayElemAt", Value: bson.A{"$coverImage", 0}}}},
		}}},
		bson.D{{Key: "$project", Value: bson.D{
			{Key: "name", Value: 1},
			{Key: "path", Value: 1},
			{Key: "imageCount", Value: 1},
			{Key: "createdAt", Value: 1},
			{Key: "updatedAt", Value: 1},
			{Key: "thumbnail", Value: "$coverImage.thumbnail"},
		}}},
	}

	cursor, err := s.coll.Aggregate(ctx, pipeline)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &seriesList); err != nil {
		return nil, 0, err
	}

	total, err := s.coll.CountDocuments(ctx, bson.D{})
	if err != nil {
		return nil, 0, err
	}
	return seriesList, total, nil
}

func (s *seriesStore) Update(ctx context.Context, series *models.Series) error {
	series.UpdatedAt = time.Now()
	filter := bson.M{"_id": series.ID}
	update := bson.M{"$set": bson.M{"name": series.Name, "updatedAt": series.UpdatedAt}}
	_, err := s.coll.UpdateOne(ctx, filter, update)
	return err
}

func (s *seriesStore) Delete(ctx context.Context, id primitive.ObjectID) error {
	_, err := s.coll.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

// --- imageStore 方法实现 ---

func (i *imageStore) CreateBatch(ctx context.Context, images []*models.Image) ([]primitive.ObjectID, error) {
	if len(images) == 0 {
		return nil, nil
	}
	docs := make([]interface{}, len(images))
	for k, image := range images {
		image.CreatedAt = time.Now()
		image.UpdatedAt = time.Now()
		docs[k] = image
	}
	res, err := i.coll.InsertMany(ctx, docs)
	if err != nil {
		return nil, err
	}
	insertedIDs := make([]primitive.ObjectID, len(res.InsertedIDs))
	for k, id := range res.InsertedIDs {
		insertedIDs[k] = id.(primitive.ObjectID)
	}
	return insertedIDs, nil
}

func (i *imageStore) GetByFileHash(ctx context.Context, hash string) (*models.Image, error) {
	var image models.Image
	err := i.coll.FindOne(ctx, bson.M{"fileHash": hash}).Decode(&image)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil
		}
		return nil, err
	}
	return &image, nil
}

func (i *imageStore) ListBySeriesID(ctx context.Context, seriesID primitive.ObjectID, page, limit int) ([]models.Image, int64, error) {
	var imageList []models.Image
	skip := (page - 1) * limit
	filter := bson.M{"seriesId": seriesID}

	findOpts := options.Find().SetSkip(int64(skip)).SetLimit(int64(limit)).SetSort(bson.M{"fileName": 1})
	cursor, err := i.coll.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &imageList); err != nil {
		return nil, 0, err
	}
	total, err := i.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return imageList, total, nil
}

func (i *imageStore) SearchByName(ctx context.Context, query string, page, limit int) ([]models.Image, int64, error) {
	var imageList []models.Image
	skip := (page - 1) * limit
	filter := bson.M{"fileName": bson.M{"$regex": query, "$options": "i"}}

	findOpts := options.Find().SetSkip(int64(skip)).SetLimit(int64(limit))
	cursor, err := i.coll.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &imageList); err != nil {
		return nil, 0, err
	}
	total, err := i.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	return imageList, total, nil
}

func (i *imageStore) FindSimilarByPHash(ctx context.Context, pHash string, limit int) ([]models.Image, error) {
	var imageList []models.Image
	filter := bson.M{"perceptualHash": pHash}
	findOpts := options.Find().SetLimit(int64(limit))
	cursor, err := i.coll.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	if err = cursor.All(ctx, &imageList); err != nil {
		return nil, err
	}
	return imageList, nil
}

func (i *imageStore) Delete(ctx context.Context, id primitive.ObjectID) error {
	_, err := i.coll.DeleteOne(ctx, bson.M{"_id": id})
	return err
}

func (s *seriesStore) UpdateMetadata(ctx context.Context, seriesID primitive.ObjectID, imageCount int, thumbnail string) error {
	filter := bson.M{"_id": seriesID}
	update := bson.M{"$set": bson.M{
		"imageCount": imageCount,
		"thumbnail":  thumbnail,
		"updatedAt":  time.Now(),
	}}
	_, err := s.coll.UpdateOne(ctx, filter, update)
	return err
}

func (i *imageStore) CountBySeriesID(ctx context.Context, seriesID primitive.ObjectID) (int64, error) {
	filter := bson.M{"seriesId": seriesID}
	return i.coll.CountDocuments(ctx, filter)
}

func (i *imageStore) GetByFilePath(ctx context.Context, path string) (*models.Image, error) {
	var image models.Image
	err := i.coll.FindOne(ctx, bson.M{"filePath": path}).Decode(&image)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil // Not found
		}
		return nil, err
	}
	return &image, nil
}

// CheckSeriesCompleteness 检查一个系列的完整性
// 它对比 Series.ImageCount 和 images 集合中的实际数量
func (s *Store) CheckSeriesCompleteness(ctx context.Context, seriesID primitive.ObjectID) (isComplete bool, expected int, actual int64, err error) {
	// 1. 获取预期的图片数量
	series, err := s.series.GetByID(ctx, seriesID)
	if err != nil {
		// 如果系列都找不到了，直接返回错误
		return false, 0, 0, fmt.Errorf("无法获取系列 %s: %w", seriesID.Hex(), err)
	}
	if series == nil {
		return false, 0, 0, fmt.Errorf("系列 %s 不存在", seriesID.Hex())
	}
	expected = series.ImageCount

	// 2. 获取实际的图片数量 (我们已经有这个函数了)
	actual, err = s.images.CountBySeriesID(ctx, seriesID)
	if err != nil {
		return false, expected, 0, fmt.Errorf("无法统计系列 %s 的图片数量: %w", seriesID.Hex(), err)
	}

	// 3. 对比并返回结果
	isComplete = int64(expected) == actual
	return isComplete, expected, actual, nil
}

// FindMissingFiles 检查一个系列在文件系统和数据库中的差异，返回在文件系统上存在但在数据库中缺失的文件名列表。
func (s *Store) FindMissingFiles(ctx context.Context, series *models.Series) (missingFileNames []string, err error) {
	// --- 步骤 1: 获取文件系统中的所有图片文件名 ---
	fsFileNames := make(map[string]bool)
	entries, err := os.ReadDir(series.Path)
	if err != nil {
		if os.IsNotExist(err) {
			slog.Warn("系列文件夹在文件系统上不存在", "path", series.Path)
			// 如果文件夹本身不存在，那么数据库里所有的图片记录都可以视为“多余”的，但这属于另一种检查
			// 在这里我们返回空列表，表示没有“丢失”的文件
			return []string{}, nil
		}
		return nil, fmt.Errorf("无法读取系列文件夹 %s: %w", series.Path, err)
	}

	for _, entry := range entries {
		// 这里可以加入一个辅助函数来判断是否是图片文件，以增加精确性
		// isImageFile(entry.Name())
		if !entry.IsDir() { // 简单起见，我们只排除了目录
			fsFileNames[entry.Name()] = true
		}
	}
	slog.Info("在文件系统上找到系列图片", "series", series.Name, "count", len(fsFileNames))

	// --- 步骤 2: 获取数据库中该系列的所有图片文件名 ---
	dbFileNames := make(map[string]bool)
	// 我们只需要 fileName 字段，使用投影(projection)来提高查询效率
	opts := options.Find().SetProjection(bson.M{"fileName": 1})
	cursor, err := s.images.coll.Find(ctx, bson.M{"seriesId": series.ID}, opts)
	if err != nil {
		return nil, fmt.Errorf("从数据库查询图片列表失败: %w", err)
	}
	defer cursor.Close(ctx)

	for cursor.Next(ctx) {
		var img models.Image
		if err := cursor.Decode(&img); err == nil {
			dbFileNames[img.FileName] = true
		}
	}
	slog.Info("在数据库中找到系列图片", "series", series.Name, "count", len(dbFileNames))

	// --- 步骤 3: 对比两个列表，找出差异 ---
	for name := range fsFileNames {
		if _, foundInDB := dbFileNames[name]; !foundInDB {
			// 如果一个文件存在于文件系统，但不存在于数据库记录中，则它是“丢失”的
			missingFileNames = append(missingFileNames, name)
		}
	}

	if len(missingFileNames) > 0 {
		slog.Warn("在系列中发现丢失的图片文件", "series", series.Name, "count", len(missingFileNames), "files", missingFileNames)
	} else {
		slog.Info("系列完整性正常，未发现丢失的文件记录。", "series", series.Name)
	}

	return missingFileNames, nil
}

// SearchByName 按系列名称进行不区分大小写的模糊搜索，并支持分页。
func (s *seriesStore) SearchByName(ctx context.Context, nameQuery string, page, limit int) ([]models.Series, int64, error) {
	var seriesList []models.Series
	skip := (page - 1) * limit

	// 使用 primitive.Regex 来安全地构建正则表达式，防止注入
	// QuoteMeta 会转义查询字符串中的所有特殊正则字符
	filter := bson.M{"name": bson.M{"$regex": primitive.Regex{Pattern: regexp.QuoteMeta(nameQuery), Options: "i"}}}

	// 设置查找选项，包括分页和排序
	findOpts := options.Find().
		SetSkip(int64(skip)).
		SetLimit(int64(limit)).
		SetSort(bson.D{{Key: "updatedAt", Value: -1}}) // 按更新时间倒序

	cursor, err := s.coll.Find(ctx, filter, findOpts)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &seriesList); err != nil {
		return nil, 0, err
	}

	// 获取匹配的总数以支持前端分页
	total, err := s.coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}

	return seriesList, total, nil
}

// FindOrCreateByName 使用 Upsert 模式原子性地查找或创建系列，这是处理并发的推荐方法。
func (s *seriesStore) FindOrCreateByName(ctx context.Context, seriesName string, seriesPath string) (*models.Series, error) {
	// 1. 定义查询条件：严格按唯一的系列名称查找
	filter := bson.M{"name": seriesName}

	// 2. 定义更新操作
	update := bson.M{
		// "$set" 操作符：无论文档是否存在，这些字段都会被更新。
		// 这对于始终记录最新的文件夹路径和更新时间非常有用。
		"$set": bson.M{
			"path":      seriesPath,
			"updatedAt": time.Now(),
		},
		// "$setOnInsert" 操作符：只在“插入”新文档时，这些字段才会被设置。
		// 这用于初始化一个全新的系列。
		"$setOnInsert": bson.M{
			"_id":        primitive.NewObjectID(),
			"name":       seriesName,
			"imageCount": 0,
			"createdAt":  time.Now(),
		},
	}

	// 3. 设置 Upsert 选项为 true
	// 这告诉 MongoDB：如果根据 filter 没找到文档，就根据 update 的内容创建一个新文档。
	opts := options.Update().SetUpsert(true)

	// 4. 执行这一个原子性的数据库命令
	res, err := s.coll.UpdateOne(ctx, filter, update, opts)
	if err != nil {
		return nil, fmt.Errorf("Upsert series '%s' 失败: %w", seriesName, err)
	}

	// 5. 获取完整的系列文档以返回给调用者
	var series models.Series
	var findFilter bson.M

	if res.UpsertedID != nil {
		// 如果 res.UpsertedID 不为空，说明执行了插入操作，我们用返回的新ID来查询，这是最精确的。
		findFilter = bson.M{"_id": res.UpsertedID}
	} else {
		// 如果为空，说明是更新了已有的文档，我们用原来的查询条件 name 来查找。
		findFilter = bson.M{"name": seriesName}
	}

	// 执行查找，获取包含所有字段（尤其是_id）的完整系列对象
	err = s.coll.FindOne(ctx, findFilter).Decode(&series)
	if err != nil {
		return nil, fmt.Errorf("无法获取 Upsert 后的系列 '%s': %w", seriesName, err)
	}

	return &series, nil
}

// BulkWrite 执行批量的写入操作 (插入、更新、删除)
func (i *imageStore) BulkWrite(ctx context.Context, models []mongo.WriteModel) error {
	if len(models) == 0 {
		return nil // 如果没有操作，直接返回
	}

	// SetOrdered(false) 表示即使其中某条指令出错，也会继续执行其他的，这让批量操作更健壮
	opts := options.BulkWrite().SetOrdered(false)

	// 调用 MongoDB 驱动原生的 BulkWrite 方法
	_, err := i.coll.BulkWrite(ctx, models, opts)
	if err != nil {
		slog.Error("imageStore BulkWrite 发生错误", "error", err)
		return err
	}
	return nil
}

// BulkWrite 执行批量的写入操作
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

// FindManyByNames
// 使用 $in 操作符批量按名称查找系列。
func (s *seriesStore) FindManyByNames(ctx context.Context, names []string) ([]models.Series, []string, error) {
	if len(names) == 0 {
		return nil, nil, nil
	}

	// 1. 执行批量查询
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

	// 2. 构建“已找到”的名称集合
	foundNamesSet := make(map[string]struct{}, len(foundSeries))
	for _, series := range foundSeries {
		foundNamesSet[series.Name] = struct{}{}
	}

	// 3. 找出“差集”（未找到的名称）
	var notFoundNames []string
	for _, name := range names {
		if _, found := foundNamesSet[name]; !found {
			notFoundNames = append(notFoundNames, name)
		}
	}

	return foundSeries, notFoundNames, nil
}

// FindImagesByPathPrefix
// 根据路径前缀查找图片，用于更新被移动文件夹下的图片路径
func (i *imageStore) FindImagesByPathPrefix(ctx context.Context, pathPrefix string) ([]models.Image, error) {
	var imageList []models.Image
	// 使用正则表达式匹配所有以 pathPrefix 开头的路径
	filter := bson.M{"filePath": bson.M{"$regex": primitive.Regex{Pattern: "^" + regexp.QuoteMeta(pathPrefix), Options: ""}}}

	cursor, err := i.coll.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &imageList); err != nil {
		return nil, err
	}
	return imageList, nil
}

// GetFirstImage 按文件名排序，获取系列中的第一张图片。
// 这通常用于获取系列的封面缩略图。
func (i *imageStore) GetFirstImage(ctx context.Context, seriesID primitive.ObjectID) (*models.Image, error) {
	var image models.Image
	filter := bson.M{"seriesId": seriesID}

	// 设置查找选项：按 fileName 升序排序，只取第一条
	opts := options.FindOne().SetSort(bson.D{{Key: "fileName", Value: 1}})

	err := i.coll.FindOne(ctx, filter, opts).Decode(&image)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil // 系列下可能还没有图片，是正常情况
		}
		return nil, err
	}
	return &image, nil
}

// DropAllCollections 删除当前数据库中的所有已知集合，主要用于测试环境的重置。
func (s *Store) DropAllCollections(ctx context.Context) error {
	slog.Warn("正在删除所有集合...", "database", s.db.Name())
	if err := s.series.coll.Drop(ctx); err != nil {
		slog.Error("删除 series 集合失败", "error", err)
		// 即使出错也继续尝试删除其他集合
	}
	if err := s.images.coll.Drop(ctx); err != nil {
		slog.Error("删除 images 集合失败", "error", err)
		return err
	}
	slog.Info("所有集合已成功删除。")
	return nil
}

// GetByName 按系列名称精确查找一个系列。
func (s *seriesStore) GetByName(ctx context.Context, name string) (*models.Series, error) {
	var series models.Series
	filter := bson.M{"name": name}
	err := s.coll.FindOne(ctx, filter).Decode(&series)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil // Not found is not an error
		}
		return nil, err
	}
	return &series, nil
}

// GetAllByFileName 查找所有具有指定文件名的图片（可能跨越多个系列）
func (i *imageStore) GetAllByFileName(ctx context.Context, fileName string) ([]models.Image, error) {
	var imageList []models.Image
	filter := bson.M{"fileName": fileName}

	cursor, err := i.coll.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	if err = cursor.All(ctx, &imageList); err != nil {
		return nil, err
	}
	return imageList, nil
}

func (i *imageStore) UpdateMetadataByPath(ctx context.Context, filePath, fileHash, pHash, thumbnail string) error {
	filter := bson.M{"filePath": filePath}
	update := bson.M{"$set": bson.M{
		"fileHash":       fileHash,
		"perceptualHash": pHash,
		"thumbnail":      thumbnail,
		"updatedAt":      time.Now(),
	}}

	res, err := i.coll.UpdateOne(ctx, filter, update)
	if err != nil {
		return err
	}

	// res.MatchedCount 是 *mongo.UpdateResult 的一个合法字段
	if res.MatchedCount == 0 {
		return fmt.Errorf("校准失败：在数据库中未找到路径为 %s 的记录", filePath)
	}
	return nil
}

// GetByIDs 根据一个ID切片，一次性获取多个系列文档。
// 这个函数在“以图搜图”后，根据找到的图片所属的系列ID来获取系列信息时非常有用。
func (s *seriesStore) GetByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Series, error) {
	// 如果传入的ID列表为空，直接返回空切片，避免无效的数据库查询。
	if len(ids) == 0 {
		return []models.Series{}, nil
	}

	// 构造查询条件：_id 必须在 (in) 我们提供的ID列表中。
	// 这是MongoDB中非常常用的 `$in` 操作符。
	filter := bson.M{"_id": bson.M{"$in": ids}}

	// 执行查询
	cursor, err := s.coll.Find(ctx, filter)
	if err != nil {
		return nil, err // 如果查询失败，直接返回错误
	}
	defer cursor.Close(ctx) // 确保游标在使用完毕后关闭

	// 创建一个切片来存放解码后的结果
	var series []models.Series
	// 使用 cursor.All 一次性将所有查询结果解码到 series 切片中
	if err = cursor.All(ctx, &series); err != nil {
		return nil, err // 如果解码失败，返回错误
	}

	return series, nil
}

// GetAllBySeriesID 获取指定系列ID下的所有图片文档。
// 这与分页获取的 ListBySeriesID 不同，它会一次性返回全部结果。
// 这在我们前端的实现中，当用户点击展开一个系列时被调用。
func (i *imageStore) GetAllBySeriesID(ctx context.Context, seriesID primitive.ObjectID) ([]models.Image, error) {
	// 构造查询条件：seriesId 字段必须等于我们提供的 seriesID
	filter := bson.M{"seriesId": seriesID}

	// 执行查询
	cursor, err := i.coll.Find(ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)

	// 创建一个切片来存放解码后的结果
	var images []models.Image
	// 将所有查询结果解码到 images 切片中
	if err = cursor.All(ctx, &images); err != nil {
		return nil, err
	}

	return images, nil
}
