// src/services/api.ts
import axios from 'axios';
import type { SeriesListResponse } from '../types/entities';
import type { Image } from '../types/entities';
import type { AppConfig } from '../types/config';

// 创建一个axios实例，统一配置后端API的基础URL
const apiClient = axios.create({
    baseURL: 'http://localhost:8080/api/v1', // 请确保这与您Go后端的地址和端口一致
    timeout: 10000,
});

/**
 * 获取系列列表（支持分页）
 * @param page - 请求的页码
 * @param limit - 每页的项目数量
 * @returns Promise<SeriesListResponse>
 */

export const fetchSeriesList = async (page: number, limit: number = 20): Promise<SeriesListResponse> => {
    try {
        const response = await apiClient.get('/series', {
            params: {
                page,
                limit,
            },
        });
        // 我们直接返回后端发来的数据，axios会将其包裹在data属性中
        return response.data;
    } catch (error) {
        console.error('Failed to fetch series list:', error);
        // 抛出错误，让调用方可以捕获并处理
        throw error;
    }
};

/**
 * 根据系列ID获取其下的所有图片
 * @param seriesId - 系列的ID
 * @returns Promise<Image[]>
 */

export const fetchImagesBySeriesId = async (seriesId: string): Promise<Image[]> => {
    try {
        // 假设后端的API端点是 /api/series/{id}/images
        const response = await apiClient.get(`/series/${seriesId}/images`);
        return response.data; // 假设后端直接返回图片数组
    } catch (error) {
        console.error(`Failed to fetch images for series ${seriesId}:`, error);
        throw error;
    }
};

/**
 * 根据文本查询搜索系列
 * @param query - 搜索关键词
 * @returns Promise<SeriesListResponse>
 */
export const searchSeriesByText = async (query: string): Promise<SeriesListResponse> => {
    try {
        // 假设后端文本搜索的 API 端点是 /api/search/text
        const response = await apiClient.get('/search/text', {
            params: { q: query },
        });
        // 假设搜索结果也遵循与获取列表时相同的分页结构
        return response.data;
    } catch (error) {
        console.error(`Failed to search for "${query}":`, error);
        throw error;
    }
};

/**
 * 上传一张图片，以搜索视觉上相似的结果
 * @param file - 用户选择的图片文件
 * @returns Promise<SeriesListResponse> - 假设后端返回与文本搜索相同的数据结构
 */
export const searchByImage = async (file: File): Promise<SeriesListResponse> => {
    // 1. 创建一个 FormData 对象来包装文件数据
    const formData = new FormData();
    // 'image' 这个键名必须与后端 r.FormFile("image") 中期望的键名一致
    formData.append('image', file);

    try {
        // 2. 发送 POST 请求，将 formData 作为请求体
        const response = await apiClient.post('/search/image', formData, {
            // 3. 设置正确的请求头，axios 通常会自动处理，但明确指定更佳
            headers: {
                'Content-Type': 'multipart/form-data',
            },
        });
        return response.data;
    } catch (error) {
        console.error('Failed to search by image:', error);
        throw error;
    }
};

// 获取当前配置
export const getConfig = async (): Promise<AppConfig> => {
    const response = await apiClient.get('/config');
    return response.data;
};

// 更新配置
export const updateConfig = async (config: AppConfig): Promise<AppConfig> => {
    const response = await apiClient.put('/config', config);
    return response.data;
};

// 开始一个扫描任务
export const startScanTask = async (path: string): Promise<{ taskId: string }> => {
    const response = await apiClient.post('/tasks/scan', { path });
    return response.data;
};

// 获取任务状态
export const getTaskStatus = async (taskId: string): Promise<{ status: string; progress: number }> => {
    const response = await apiClient.get(`/tasks/${taskId}`);
    return response.data;
};