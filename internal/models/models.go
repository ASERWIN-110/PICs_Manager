package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

type Timestamps struct {
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

// Series 代表一个媒体系列或相册。
type Series struct {
	ID   primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	Name string             `bson:"name" json:"name"`
	Path string             `bson:"path" json:"path"`
	// ImageCount 缓存该系列下的媒体数量。字段名保留 imageCount 以兼容现有数据和 API。
	ImageCount int    `bson:"imageCount" json:"imageCount"`
	Thumbnail  string `bson:"thumbnail,omitempty" json:"thumbnail,omitempty"`
	// HasThumbnail 用于列表接口判断是否返回缩略图 URL，避免读取 base64 缩略图正文。
	HasThumbnail bool `bson:"hasThumbnail,omitempty" json:"-"`
	Timestamps
}

// Image 代表一个单独的媒体文件。类型名保留 Image 以兼容现有集合和 API。
type Image struct {
	ID        primitive.ObjectID `bson:"_id,omitempty" json:"id"`
	SeriesID  primitive.ObjectID `bson:"seriesId" json:"seriesId"`
	FileHash  string             `bson:"fileHash" json:"fileHash"`
	MediaType string             `bson:"mediaType" json:"mediaType"`
	// PerceptualHash 仅用于图片媒体的相似检索。
	PerceptualHash string `bson:"perceptualHash" json:"perceptualHash"`
	// PHashBuckets 是由 PerceptualHash 派生的检索桶，用于避免以图搜图全表扫描。
	PHashBuckets []string `bson:"pHashBuckets,omitempty" json:"-"`
	FileName     string   `bson:"fileName" json:"fileName"`
	FilePath     string   `bson:"filePath" json:"filePath"`
	Thumbnail    string   `bson:"thumbnail" json:"thumbnail"`
	// HasThumbnail 用于媒体列表判断是否返回缩略图 URL，避免读取 base64 缩略图正文。
	HasThumbnail bool `bson:"hasThumbnail,omitempty" json:"-"`
	Timestamps
}
