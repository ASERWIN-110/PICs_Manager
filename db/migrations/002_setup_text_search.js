// 脚本: 002_setup_text_search.js
// 功能: 为搜索功能创建文本索引
// 用法: mongosh "mongodb://localhost:27017/media_manager" < 002_setup_text_search.js

print("脚本开始: 002_setup_text_search.js");

db = db.getSiblingDB('media_manager');

print("正在为 'series.name' 创建文本索引...");
// 为 series.name 创建文本索引，用于模糊搜索
db.series.createIndex(
    { "name": "text" },
    { name: "idx_name_text", default_language: "none" }
);
print("文本索引创建完毕。");

print("脚本结束: 002_setup_text_search.js");