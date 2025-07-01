package database

import (
	"PICs_Manager/internal/models"
	"context"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo"
)

// Store 是一个顶层接口，它组合了所有特定数据模型的存储接口。
type Store interface {
	Series() SeriesStore
	Images() ImageStore
	EnsureIndexes(ctx context.Context) error
	CheckSeriesCompleteness(ctx context.Context, seriesID primitive.ObjectID) (isComplete bool, expected int, actual int64, err error)
	FindMissingFiles(ctx context.Context, series *models.Series) (missingFileNames []string, err error)
	DropAllCollections(ctx context.Context) error
}

// SeriesStore 定义了所有与 Series 模型相关的数据库操作。
type SeriesStore interface {
	Create(ctx context.Context, series *models.Series) error
	GetByID(ctx context.Context, id primitive.ObjectID) (*models.Series, error)
	GetByPath(ctx context.Context, path string) (*models.Series, error)
	List(ctx context.Context, page, limit int) ([]models.Series, int64, error)
	Update(ctx context.Context, series *models.Series) error
	Delete(ctx context.Context, id primitive.ObjectID) error
	UpdateMetadata(ctx context.Context, seriesID primitive.ObjectID, imageCount int, thumbnail string) error
	GetAllSeries(ctx context.Context) ([]models.Series, error)
	SearchByName(ctx context.Context, nameQuery string, page, limit int) (seriesList []models.Series, total int64, err error)
	FindOrCreateByName(ctx context.Context, seriesName string, seriesPath string) (*models.Series, error)
	BulkWrite(ctx context.Context, models []mongo.WriteModel) error
	FindManyByNames(ctx context.Context, names []string) (foundSeries []models.Series, notFoundNames []string, err error)
	GetByName(ctx context.Context, name string) (*models.Series, error)
	GetByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Series, error)
}

// ImageStore 定义了所有与 Image 模型相关的数据库操作。
type ImageStore interface {
	CreateBatch(ctx context.Context, images []*models.Image) ([]primitive.ObjectID, error)
	GetByFileHash(ctx context.Context, hash string) (*models.Image, error)
	GetByFilePath(ctx context.Context, path string) (*models.Image, error)
	ListBySeriesID(ctx context.Context, seriesID primitive.ObjectID, page, limit int) ([]models.Image, int64, error)
	SearchByName(ctx context.Context, query string, page, limit int) ([]models.Image, int64, error)
	FindSimilarByPHash(ctx context.Context, pHash string, limit int) ([]models.Image, error)
	Delete(ctx context.Context, id primitive.ObjectID) error
	CountBySeriesID(ctx context.Context, seriesID primitive.ObjectID) (int64, error)
	BulkWrite(ctx context.Context, models []mongo.WriteModel) error
	FindImagesByPathPrefix(ctx context.Context, pathPrefix string) ([]models.Image, error)
	GetFirstImage(ctx context.Context, seriesID primitive.ObjectID) (*models.Image, error)
	GetAllByFileName(ctx context.Context, fileName string) ([]models.Image, error)
	UpdateMetadataByPath(ctx context.Context, filePath, fileHash, pHash, thumbnail string) error
	GetAllBySeriesID(ctx context.Context, seriesID primitive.ObjectID) ([]models.Image, error)
}
