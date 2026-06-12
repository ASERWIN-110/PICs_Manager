import React, { useRef, useState, useEffect } from 'react';
import ArticleIcon from '@mui/icons-material/Article';
import AudiotrackIcon from '@mui/icons-material/Audiotrack';
import ImageIcon from '@mui/icons-material/Image';
import InsertDriveFileIcon from '@mui/icons-material/InsertDriveFile';
import MovieIcon from '@mui/icons-material/Movie';
import { fetchMediaBySeriesId, getConfig, resolveApiAssetUrl } from '../services/api';
import type { MediaItem, Pagination } from '../types/entities';

const MEDIA_PAGE_SIZE = 60;
const FALLBACK_MEDIA_TYPES = ['image', 'video', 'audio', 'text'];

interface MediaListProps {
    seriesId: string;
    onMediaContextMenu: (event: React.MouseEvent, path: string) => void;
}

const mediaIcon = (mediaType: string) => {
    switch (mediaType) {
        case 'image':
            return <ImageIcon fontSize="large" />;
        case 'video':
            return <MovieIcon fontSize="large" />;
        case 'audio':
            return <AudiotrackIcon fontSize="large" />;
        case 'text':
            return <ArticleIcon fontSize="large" />;
        default:
            return <InsertDriveFileIcon fontSize="large" />;
    }
};

const mediaTypeLabel = (mediaType: string) => {
    switch (mediaType) {
        case 'image':
            return '图片';
        case 'video':
            return '视频';
        case 'audio':
            return '音频';
        case 'text':
            return '文本';
        default:
            return mediaType;
    }
};

const normalizeMediaTypes = (types: string[]) => {
    const seen = new Set<string>();
    const result: string[] = [];
    for (const rawType of types) {
        const mediaType = rawType.trim().toLowerCase().replace(/[^a-z0-9_]+/g, '_').replace(/^_+|_+$/g, '');
        if (!mediaType || seen.has(mediaType)) {
            continue;
        }
        seen.add(mediaType);
        result.push(mediaType);
    }
    return result;
};

const MediaList: React.FC<MediaListProps> = ({ seriesId, onMediaContextMenu }: MediaListProps) => {
    const [mediaTypes, setMediaTypes] = useState<string[]>(FALLBACK_MEDIA_TYPES);
    const [activeMediaType, setActiveMediaType] = useState('image');
    const [mediaItems, setMediaItems] = useState<MediaItem[]>([]);
    const [pagination, setPagination] = useState<Pagination | null>(null);
    const [pageByType, setPageByType] = useState<Record<string, number>>({ image: 1 });
    const [cursorByType, setCursorByType] = useState<Record<string, Record<number, string>>>({ image: { 1: '' } });
    const cursorByTypeRef = useRef<Record<string, Record<number, string>>>({ image: { 1: '' } });
    const [isLoading, setIsLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);
    const currentPage = pageByType[activeMediaType] ?? 1;

    const resetPaging = () => {
        const initialCursor = { image: { 1: '' } };
        cursorByTypeRef.current = initialCursor;
        setCursorByType(initialCursor);
        setPageByType({ image: 1 });
    };

    const rememberCursor = (mediaType: string, page: number, cursor: string) => {
        if (!cursor || cursorByTypeRef.current[mediaType]?.[page] === cursor) {
            return;
        }
        const nextTypeCursor = { ...(cursorByTypeRef.current[mediaType] ?? { 1: '' }), [page]: cursor };
        const next = { ...cursorByTypeRef.current, [mediaType]: nextTypeCursor };
        cursorByTypeRef.current = next;
        setCursorByType(next);
    };

    const setCurrentPageForActive = (update: (page: number) => number) => {
        setPageByType(prev => {
            const prevPage = prev[activeMediaType] ?? 1;
            return { ...prev, [activeMediaType]: update(prevPage) };
        });
    };

    useEffect(() => {
        let isMounted = true;
        const loadMediaTypes = async () => {
            try {
                const config = await getConfig();
                if (!isMounted) {
                    return;
                }
                const configured = config.scanner.mediaTypes?.map(item => item.type) ?? [];
                const nextTypes = normalizeMediaTypes(['image', ...configured, ...FALLBACK_MEDIA_TYPES]);
                setMediaTypes(nextTypes.length > 0 ? nextTypes : FALLBACK_MEDIA_TYPES);
            } catch (err) {
                console.error(err);
                if (isMounted) {
                    setMediaTypes(FALLBACK_MEDIA_TYPES);
                }
            }
        };
        loadMediaTypes();
        return () => {
            isMounted = false;
        };
    }, []);

    useEffect(() => {
        resetPaging();
        setActiveMediaType('image');
    }, [seriesId]);

    useEffect(() => {
        let isMounted = true;

        const loadMedia = async () => {
            setIsLoading(true);
            setError(null);
            try {
                const cursor = cursorByTypeRef.current[activeMediaType]?.[currentPage] ?? '';
                const response = await fetchMediaBySeriesId(seriesId, activeMediaType, currentPage, MEDIA_PAGE_SIZE, cursor);
                if (!isMounted) {
                    return;
                }
                setMediaItems(response.data);
                setPagination(response.pagination);
                if (response.pagination.nextCursor) {
                    rememberCursor(activeMediaType, currentPage + 1, response.pagination.nextCursor);
                }
            } catch (err) {
                console.error(err);
                if (isMounted) {
                    setError(err instanceof Error ? err.message : `无法加载${mediaTypeLabel(activeMediaType)}列表。`);
                }
            } finally {
                if (isMounted) {
                    setIsLoading(false);
                }
            }
        };
        loadMedia();
        return () => {
            isMounted = false;
        };
    }, [seriesId, activeMediaType, currentPage]);

    if (isLoading) {
        return (
            <div className="media-section">
                <MediaTypeTabs mediaTypes={mediaTypes} activeMediaType={activeMediaType} onChange={setActiveMediaType} />
                <div className="inline-state">正在加载{mediaTypeLabel(activeMediaType)}...</div>
            </div>
        );
    }

    if (error) {
        return (
            <div className="media-section">
                <MediaTypeTabs mediaTypes={mediaTypes} activeMediaType={activeMediaType} onChange={setActiveMediaType} />
                <div className="inline-state error">{error}</div>
            </div>
        );
    }

    return (
        <div className="media-section">
            <MediaTypeTabs mediaTypes={mediaTypes} activeMediaType={activeMediaType} onChange={setActiveMediaType} />
            <div className="media-summary">
                <span>{pagination?.totalItems ?? 0} 个{mediaTypeLabel(activeMediaType)}</span>
                <span>第 {pagination?.currentPage ?? currentPage} 页 / 共 {pagination?.totalPages ?? 0} 页</span>
            </div>
            {mediaItems.length === 0 ? (
                <div className="inline-state">这个系列还没有{mediaTypeLabel(activeMediaType)}记录。</div>
            ) : (
                <div className="media-grid">
                    {mediaItems.map(item => (
                        <div
                            key={`${item.id}-${item.fileName}`}
                            className="media-tile"
                            title={item.fileName}
                            onContextMenu={(e) => onMediaContextMenu(e, item.filePath)}
                        >
                            {item.thumbnailUrl ? (
                                <img
                                    loading="lazy"
                                    src={resolveApiAssetUrl(item.thumbnailUrl)}
                                    alt={item.fileName}
                                    className="media-thumb"
                                />
                            ) : (
                                <div className={`media-placeholder ${item.mediaType || activeMediaType || 'unknown'}`}>
                                    {mediaIcon(item.mediaType || activeMediaType)}
                                </div>
                            )}
                            <div className="media-caption">
                                <span className="media-type">{item.mediaType || activeMediaType}</span>
                                <span className="media-name">{item.fileName}</span>
                            </div>
                        </div>
                    ))}
                </div>
            )}
            {pagination && pagination.totalPages > 1 && (
                <div className="media-pagination">
                    <button
                        className="button secondary"
                        onClick={() => setCurrentPageForActive(page => page - 1)}
                        disabled={currentPage <= 1 || isLoading}
                    >
                        上一页
                    </button>
                    <button
                        className="button secondary"
                        onClick={() => setCurrentPageForActive(page => page + 1)}
                        disabled={currentPage >= pagination.totalPages || isLoading || !cursorByType[activeMediaType]?.[currentPage + 1]}
                    >
                        下一页
                    </button>
                </div>
            )}
        </div>
    );
};

interface MediaTypeTabsProps {
    mediaTypes: string[];
    activeMediaType: string;
    onChange: (mediaType: string) => void;
}

const MediaTypeTabs: React.FC<MediaTypeTabsProps> = ({ mediaTypes, activeMediaType, onChange }) => (
    <div className="media-tabs" role="tablist" aria-label="媒体类型">
        {mediaTypes.map(mediaType => (
            <button
                key={mediaType}
                type="button"
                role="tab"
                aria-selected={mediaType === activeMediaType}
                className={`media-tab${mediaType === activeMediaType ? ' active' : ''}`}
                onClick={() => onChange(mediaType)}
            >
                {mediaTypeLabel(mediaType)}
            </button>
        ))}
    </div>
);

export default MediaList;
