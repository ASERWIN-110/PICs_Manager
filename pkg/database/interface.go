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
	// Images returns an aggregate media read view. The method name is retained
	// for API compatibility; media writes may target per-type collections.
	Images() ImageStore
	// Media returns a per-media-type store. Use it when reads or writes must not
	// mix image/video/audio/text records.
	Media(mediaType string) ImageStore
	EnsureIndexes(ctx context.Context) error
	Diagnostics(ctx context.Context) (Diagnostics, error)
	DropAllCollections(ctx context.Context) error
	Close(ctx context.Context) error
}

type Diagnostics struct {
	SeriesCount                int64
	SeriesWithThumbnail        int64
	SeriesWithThumbnailFlag    int64
	SeriesMissingThumbnailFlag int64
	MediaCount                 int64
	ImageMediaCount            int64
	ImagesWithPHash            int64
	ImagesWithPHashBuckets     int64
	ImagesMissingPHashBuckets  int64
	SeriesIndexes              []string
	ImageIndexes               []string
}

// SeriesStore 定义了所有与 Series 模型相关的数据库操作。
type SeriesStore interface {
	GetByID(ctx context.Context, id primitive.ObjectID) (*models.Series, error)
	ListCursor(ctx context.Context, cursor string, limit int) ([]models.Series, int64, string, error)
	SearchByNameCursor(ctx context.Context, nameQuery string, cursor string, limit int) (seriesList []models.Series, total int64, nextCursor string, err error)
	BulkWrite(ctx context.Context, models []mongo.WriteModel) error
	FindManyByNames(ctx context.Context, names []string) (foundSeries []models.Series, notFoundNames []string, err error)
	GetByIDs(ctx context.Context, ids []primitive.ObjectID) ([]models.Series, error)
}

// ImageStore 定义媒体文档的数据库操作。名称保留 ImageStore 以兼容现有集合和模型。
type ImageStore interface {
	GetByID(ctx context.Context, id primitive.ObjectID) (*models.Image, error)
	ListBySeriesIDCursor(ctx context.Context, seriesID primitive.ObjectID, cursor string, limit int) ([]models.Image, int64, string, error)
	FindSimilarByPHash(ctx context.Context, pHash string, limit int) ([]models.Image, error)
	Delete(ctx context.Context, id primitive.ObjectID) error
	CountBySeriesID(ctx context.Context, seriesID primitive.ObjectID) (int64, error)
	BulkWrite(ctx context.Context, models []mongo.WriteModel) error
	GetFirstThumbnailMedia(ctx context.Context, seriesID primitive.ObjectID) (*models.Image, error)
	GetAllBySeriesID(ctx context.Context, seriesID primitive.ObjectID) ([]models.Image, error)
}
