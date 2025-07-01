package models

import (
	"time"

	"go.mongodb.org/mongo-driver/bson/primitive"
)

// Timestamps 结构体嵌入到其他模型中，用于追踪创建和更新时间。
// 这种方式遵循了 "Don't Repeat Yourself" (DRY) 原则。
type Timestamps struct {
	CreatedAt time.Time `bson:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt"`
}

// Series 代表一个媒体系列或相册，对应MongoDB中的一个文档。
type Series struct {
	// ID 是MongoDB文档的唯一标识符。
	// `primitive.ObjectID` 是MongoDB官方驱动中用于表示_id的类型。
	// `omitempty` 标签表示如果Go字段为空，则在序列化为BSON时不包含该字段，
	// 这在插入新文档时非常有用，可以让MongoDB自动生成ID。
	ID primitive.ObjectID `bson:"_id,omitempty"`

	// Name 是该系列的名称，。
	Name string `bson:"name"`

	// Path 是该系列在文件系统上的原始路径，用于扫描器定位。
	Path string `bson:"path"`

	// ImageCount 缓存了该系列下的图片数量，避免了昂贵的实时计数查询。
	ImageCount int `bson:"imageCount"`

	// 系列目录下第一张图片的缩略图
	Thumbnail string `bson:"thumbnail,omitempty"`

	// 嵌入Timestamps结构体，自动获得 CreatedAt 和 UpdatedAt 字段。
	Timestamps
}

// Image 代表一个单独的媒体文件，对应MongoDB中的一个文档。
type Image struct {
	ID primitive.ObjectID `bson:"_id,omitempty"`

	// SeriesID 是一个外键，指向其所属的 Series 文档的 _id。
	// 我们会在此字段上建立索引以加速查询。
	SeriesID primitive.ObjectID `bson:"seriesId"`

	// FileHash 是文件的内容哈希（例如 SHA-256），用于精确的重复文件检测。
	FileHash string `bson:"fileHash"`

	// PerceptualHash 是文件的感知哈希，用于查找视觉上相似的图片。
	PerceptualHash string `bson:"perceptualHash"`

	// FileName 是原始文件名。
	FileName string `bson:"fileName"`

	// FilePath 是文件的完整存储路径。
	FilePath string `bson:"filePath"`

	// Thumbnail 字段可以存储缩略图的信息，一个Base64编码的字符串。
	Thumbnail string `bson:"thumbnail"`

	// 嵌入Timestamps结构体。
	Timestamps
}
