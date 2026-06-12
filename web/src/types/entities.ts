export interface Series {
    id: string;
    name: string;
    path: string;
    imageCount: number;
    thumbnailUrl?: string;
    createdAt: string;
    updatedAt: string;
}

export interface MediaItem {
    id: string;
    seriesId: string;
    fileHash: string;
    mediaType: string;
    perceptualHash: string;
    fileName: string;
    filePath: string;
    thumbnailUrl?: string;
    createdAt: string;
    updatedAt: string;
}

export interface Pagination {
    currentPage: number;
    totalPages: number;
    totalItems: number;
    limit: number;
    nextCursor?: string;
}

export interface SeriesListResponse<T = Series> {
    data: T[];
    pagination: Pagination;
}
