// 脚本: 003_setup_schema_validation.js
// 功能: 为集合添加数据校验规则
// 用法: mongosh "mongodb://localhost:27017/media_manager" < 003_setup_schema_validation.js

print("脚本开始: 003_setup_schema_validation.js");

db = db.getSiblingDB('media_manager');

print("正在为 'images' 集合添加Schema Validation...");
db.runCommand({
    collMod: "images",
    validator: {
        $jsonSchema: {
            bsonType: "object",
            required: ["seriesId", "fileHash", "fileName", "filePath", "createdAt", "updatedAt"],
            properties: {
                seriesId: {
                    bsonType: "objectId",
                    description: "必须是objectId且必填"
                },
                fileHash: {
                    bsonType: "string",
                    description: "必须是string且必填"
                },
                fileName: {
                    bsonType: "string",
                    description: "必须是string且必填"
                },
                filePath: {
                    bsonType: "string",
                    description: "必须是string且必填"
                },
                createdAt: {
                    bsonType: "date",
                    description: "必须是date且必填"
                },
                updatedAt: {
                    bsonType: "date",
                    description: "必须是date且必填"
                }
            }
        }
    },
    validationLevel: "strict", // "strict" (默认): 对所有插入和更新都校验; "moderate": 只对符合校验规则的文档进行更新校验
    validationAction: "error"   // "error" (默认): 不符合规则的操作会报错; "warn": 只记录警告
});
print("Schema Validation添加完毕。");

print("脚本结束: 003_setup_schema_validation.js");