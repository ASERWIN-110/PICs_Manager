import axios from 'axios';
import type { MediaItem, Pagination, SeriesListResponse } from '../types/entities';
import type { AppConfig } from '../types/config';

interface ApiError {
    code: string;
    message: string;
}

interface ApiEnvelope<T> {
    data?: T;
    meta?: {
        pagination?: Pagination;
    };
    error?: ApiError;
}

export interface TaskStatusResponse {
    status: string;
    progress: number;
    error?: string;
}

export interface AuthStatus {
    enabled: boolean;
    requireViewerForRead: boolean;
    allowLocalAdmin: boolean;
    pairingAvailable: boolean;
}

export interface DeviceInfo {
    id: string;
    name: string;
    scope: string;
    createdAt: string;
    lastSeenAt?: string;
    expiresAt?: string;
    revokedAt?: string;
}

const DEVICE_TOKEN_KEY = 'pics-manager-device-token';

const apiBaseURL = import.meta.env.VITE_API_BASE_URL ?? 'http://localhost:8080/api/v1';

const apiClient = axios.create({
    baseURL: apiBaseURL,
    timeout: 30000,
});

apiClient.interceptors.request.use(config => {
    const token = getDeviceToken();
    if (token) {
        config.headers.Authorization = `Bearer ${token}`;
    }
    return config;
});

export const getDeviceToken = (): string => localStorage.getItem(DEVICE_TOKEN_KEY) ?? '';

export const setDeviceToken = (token: string) => {
    if (token) {
        localStorage.setItem(DEVICE_TOKEN_KEY, token);
    } else {
        localStorage.removeItem(DEVICE_TOKEN_KEY);
    }
};

export const resolveApiAssetUrl = (path?: string): string | undefined => {
    if (!path) {
        return undefined;
    }
    if (/^https?:\/\//i.test(path) || path.startsWith('data:')) {
        return path;
    }
    const normalizedBase = apiBaseURL.replace(/\/api\/v1\/?$/, '');
    return `${normalizedBase}${path}`;
};

const contentDispositionFileName = (header?: string): string => {
    if (!header) {
        return '';
    }
    const utf8Match = /filename\*=UTF-8''([^;]+)/i.exec(header);
    if (utf8Match?.[1]) {
        return decodeURIComponent(utf8Match[1]);
    }
    const match = /filename="?([^";]+)"?/i.exec(header);
    return match?.[1] ?? '';
};

export const downloadApiFile = async (path: string, fallbackName: string) => {
    const response = await apiClient.get(path, { responseType: 'blob', timeout: 120000 });
    const fileName = contentDispositionFileName(response.headers['content-disposition']) || fallbackName || 'download';
    const url = window.URL.createObjectURL(response.data);
    const link = document.createElement('a');
    link.href = url;
    link.download = fileName;
    document.body.appendChild(link);
    link.click();
    link.remove();
    window.URL.revokeObjectURL(url);
};

const getErrorMessage = (err: unknown, fallback: string): string => {
    if (axios.isAxiosError<ApiEnvelope<unknown>>(err)) {
        const envelope = err.response?.data;
        if (envelope?.error?.message) {
            return envelope.error.message;
        }
        if (err.code === 'ECONNABORTED') {
            return '请求超时，后端仍可能在处理较大的媒体库。';
        }
        return err.message || fallback;
    }
    if (err instanceof Error) {
        return err.message;
    }
    return fallback;
};

const requestData = async <T>(request: Promise<{ data: ApiEnvelope<T> }>, fallback: string): Promise<T> => {
    try {
        const response = await request;
        return unwrapData(response.data);
    } catch (err) {
        throw new Error(getErrorMessage(err, fallback));
    }
};

const requestList = async <T>(request: Promise<{ data: ApiEnvelope<T[]> }>, fallback: string): Promise<SeriesListResponse<T>> => {
    try {
        const response = await request;
        return unwrapList(response.data);
    } catch (err) {
        throw new Error(getErrorMessage(err, fallback));
    }
};

const unwrapData = <T>(envelope: ApiEnvelope<T>): T => {
    if (envelope.error) {
        throw new Error(envelope.error.message);
    }
    if (envelope.data === undefined) {
        throw new Error('API response missing data');
    }
    return envelope.data;
};

const unwrapList = <T>(envelope: ApiEnvelope<T[]>): SeriesListResponse<T> => {
    const data = unwrapData(envelope);
    const pagination = envelope.meta?.pagination;
    if (!pagination) {
        throw new Error('API response missing pagination metadata');
    }
    return { data, pagination };
};

export const fetchSeriesList = async (page: number, limit: number = 20, cursor: string = ''): Promise<SeriesListResponse> => {
    return requestList(
        apiClient.get<ApiEnvelope<SeriesListResponse['data']>>('/series', {
            params: { page, limit, cursor },
        }),
        '无法加载系列列表',
    );
};

export const fetchMediaBySeriesId = async (
    seriesId: string,
    mediaType: string,
    page: number,
    limit: number = 60,
    cursor: string = '',
): Promise<SeriesListResponse<MediaItem>> => {
    const normalizedType = mediaType.trim() || 'image';
    return requestList(
        apiClient.get<ApiEnvelope<MediaItem[]>>(`/series/${seriesId}/media/${encodeURIComponent(normalizedType)}`, {
            params: { page, limit, cursor },
        }),
        '无法加载媒体列表',
    );
};

export const searchSeriesByText = async (query: string, page: number = 1, limit: number = 20, cursor: string = ''): Promise<SeriesListResponse> => {
    return requestList(
        apiClient.get<ApiEnvelope<SeriesListResponse['data']>>('/search/text', {
            params: { q: query, page, limit, cursor },
        }),
        '搜索失败',
    );
};

export const searchByImage = async (file: File): Promise<SeriesListResponse> => {
    const formData = new FormData();
    formData.append('image', file);

    return requestList(
        apiClient.post<ApiEnvelope<SeriesListResponse['data']>>('/search/image', formData, {
            headers: { 'Content-Type': 'multipart/form-data' },
            timeout: 120000,
        }),
        '以图搜图失败',
    );
};

export const getConfig = async (): Promise<AppConfig> => {
    return requestData(apiClient.get<ApiEnvelope<AppConfig>>('/config'), '无法加载配置');
};

export const getAuthStatus = async (): Promise<AuthStatus> => {
    return requestData(apiClient.get<ApiEnvelope<AuthStatus>>('/auth/status'), '无法加载认证状态');
};

export const claimPairingCode = async (code: string, deviceName: string): Promise<{ token: string; device: DeviceInfo }> => {
    return requestData(
        apiClient.post<ApiEnvelope<{ token: string; device: DeviceInfo }>>('/auth/claim', { code, deviceName }),
        '设备配对失败',
    );
};

export const updateConfig = async (config: AppConfig): Promise<AppConfig> => {
    return requestData(apiClient.put<ApiEnvelope<AppConfig>>('/config', config), '保存配置失败');
};

export const startScanTask = async (path: string, mode: string = 'full'): Promise<{ taskId: string }> => {
    return requestData(apiClient.post<ApiEnvelope<{ taskId: string }>>('/tasks', { path, mode }), '启动扫描任务失败');
};

export const getTaskStatus = async (taskId: string): Promise<TaskStatusResponse> => {
    return requestData(apiClient.get<ApiEnvelope<TaskStatusResponse>>(`/tasks/${taskId}`), '获取任务状态失败');
};
