import React, { useRef, useState, useEffect } from 'react';
import ArticleIcon from '@mui/icons-material/Article';
import AudiotrackIcon from '@mui/icons-material/Audiotrack';
import ImageIcon from '@mui/icons-material/Image';
import InsertDriveFileIcon from '@mui/icons-material/InsertDriveFile';
import MovieIcon from '@mui/icons-material/Movie';
import { fetchMediaBySeriesId, resolveApiAssetUrl } from '../services/api';
import type { MediaItem, Pagination } from '../types/entities';

const MEDIA_PAGE_SIZE = 60;

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

const MediaList: React.FC<MediaListProps> = ({ seriesId, onMediaContextMenu }: MediaListProps) => {
    const [mediaItems, setMediaItems] = useState<MediaItem[]>([]);
    const [pagination, setPagination] = useState<Pagination | null>(null);
    const [currentPage, setCurrentPage] = useState(1);
    const [cursorByPage, setCursorByPage] = useState<Record<number, string>>({ 1: '' });
    const cursorByPageRef = useRef<Record<number, string>>({ 1: '' });
    const [isLoading, setIsLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    const resetCursors = () => {
        const initial = { 1: '' };
        cursorByPageRef.current = initial;
        setCursorByPage(initial);
    };

    const rememberCursor = (page: number, cursor: string) => {
        if (!cursor || cursorByPageRef.current[page] === cursor) {
            return;
        }
        const next = { ...cursorByPageRef.current, [page]: cursor };
        cursorByPageRef.current = next;
        setCursorByPage(next);
    };

    useEffect(() => {
        setCurrentPage(1);
        resetCursors();
    }, [seriesId]);

    useEffect(() => {
        let isMounted = true;

        const loadMedia = async () => {
            setIsLoading(true);
            setError(null);
            try {
                const cursor = cursorByPageRef.current[currentPage] ?? '';
                const response = await fetchMediaBySeriesId(seriesId, currentPage, MEDIA_PAGE_SIZE, cursor);
                if (!isMounted) {
                    return;
                }
                setMediaItems(response.data);
                setPagination(response.pagination);
                if (response.pagination.nextCursor) {
                    rememberCursor(currentPage + 1, response.pagination.nextCursor);
                }
            } catch (err) {
                console.error(err);
                if (isMounted) {
                    setError(err instanceof Error ? err.message : '无法加载媒体列表。');
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
    }, [seriesId, currentPage]);

    if (isLoading) {
        return <div className="inline-state">正在加载媒体...</div>;
    }

    if (error) {
        return <div className="inline-state error">{error}</div>;
    }

    if (mediaItems.length === 0) {
        return <div className="inline-state">这个系列还没有媒体记录。</div>;
    }

    return (
        <div className="media-section">
            <div className="media-summary">
                <span>{pagination?.totalItems ?? 0} 个媒体</span>
                <span>第 {pagination?.currentPage ?? currentPage} 页 / 共 {pagination?.totalPages ?? 0} 页</span>
            </div>
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
                            <div className={`media-placeholder ${item.mediaType || 'unknown'}`}>
                                {mediaIcon(item.mediaType)}
                            </div>
                        )}
                        <div className="media-caption">
                            <span className="media-type">{item.mediaType || 'image'}</span>
                            <span className="media-name">{item.fileName}</span>
                        </div>
                    </div>
                ))}
            </div>
            {pagination && pagination.totalPages > 1 && (
                <div className="media-pagination">
                    <button
                        className="button secondary"
                        onClick={() => setCurrentPage(page => page - 1)}
                        disabled={currentPage <= 1 || isLoading}
                    >
                        上一页
                    </button>
                    <button
                        className="button secondary"
                        onClick={() => setCurrentPage(page => page + 1)}
                        disabled={currentPage >= pagination.totalPages || isLoading || !cursorByPage[currentPage + 1]}
                    >
                        下一页
                    </button>
                </div>
            )}
        </div>
    );
};

export default MediaList;
