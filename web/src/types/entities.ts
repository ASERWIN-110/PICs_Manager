// src/types/entities.ts

// Go 的 time.Time 在序列化为 JSON 时通常是 ISO 8601 格式的字符串
export interface Timestamps {
    createdAt: string;
    updatedAt: string;
}

// 对应后端的 Series struct
// 我们使用 "extends" 来继承 Timestamps 中的字段
export interface Series extends Timestamps {
    ID: string;           // 对应 a`primitive.ObjectID`
    Name: string;
    Path: string;
    ImageCount: number;
    Thumbnail: string;    // 这将用于显示缩略图，可能是Base64或一个URL
}

// 对应后端的 Image struct
export interface Image extends Timestamps {
    ID: string;
    SeriesID: string;
    FileHash: string;
    PerceptualHash: string;
    FileName: string;
    FilePath: string;
    Thumbnail: string;    // 图片自身的缩略图
}

// --- API响应的包装结构 (这部分保持不变) ---

export interface Pagination {
    currentPage: number;
    totalPages: number;
    totalItems: number;
}

export interface SeriesListResponse {
    data: Series[];
    pagination: Pagination;
}