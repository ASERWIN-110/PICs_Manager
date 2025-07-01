// 脚本: 001_create_collections_and_indexes.js
// 功能: 创建集合并建立核心索引
// 用法: mongosh "mongodb://localhost:27017/media_manager" < 001_create_collections_and_indexes.js

print("脚本开始: 001_create_collections_and_indexes.js");

// 切换到 media_manager 数据库
db = db.getSiblingDB('media_manager');

// --- 创建 'series' 集合并添加索引 ---
print("正在处理 'series' 集合...");
db.createCollection("series");

// 为 series.path 创建唯一索引，用于扫描器快速关联
db.series.createIndex(
    { "path": 1 },
    { unique: true, name: "idx_path_unique" }
);
print("为 'series.path' 创建了唯一索引。");


// --- 创建 'images' 集合并添加索引 ---
print("正在处理 'images' 集合...");
db.createCollection("images");

// 为 images.fileHash 创建唯一索引，用于精确文件查重
db.images.createIndex(
    { "fileHash": 1 },
    { unique: true, name: "idx_filehash_unique" }
);
print("为 'images.fileHash' 创建了唯一索引。");

// 为 images.seriesId 和 images._id 创建复合索引，用于高效浏览系列下的图片
db.images.createIndex(
    { "seriesId": 1, "_id": 1 },
    { name: "idx_seriesid_id_for_paging" }
);
print("为 'images.seriesId' 和 '_id' 创建了复合索引。");

// 为 images.perceptualHash 创建索引，用于相似图片查找
db.images.createIndex(
    { "perceptualHash": 1 },
    { name: "idx_phash" }
);
print("为 'images.perceptualHash' 创建了索引。");

print("脚本结束: 001_create_collections_and_indexes.js");