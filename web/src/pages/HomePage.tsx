import React, { useEffect, useRef, useState } from 'react';
import { fetchSeriesList, searchByImage, searchSeriesByText } from '../services/api';
import type { Pagination, Series } from '../types/entities';
import SeriesItem from '../components/SeriesItem';
import MediaList from '../components/MediaList';
import SearchBar from '../components/SearchBar';

type SearchState =
    | { type: 'text'; query: string; label: string }
    | { type: 'image'; label: string };

const HomePage = () => {
    const [seriesList, setSeriesList] = useState<Series[]>([]);
    const [pagination, setPagination] = useState<Pagination | null>(null);
    const [currentPage, setCurrentPage] = useState<number>(1);
    const [isLoading, setIsLoading] = useState<boolean>(true);
    const [error, setError] = useState<string | null>(null);
    const [notification, setNotification] = useState<string>('');
    const [expandedSeriesId, setExpandedSeriesId] = useState<string | null>(null);
    const [isSearching, setIsSearching] = useState(false);
    const [activeSearch, setActiveSearch] = useState<SearchState | null>(null);
    const [cursorByPage, setCursorByPage] = useState<Record<number, string>>({ 1: '' });
    const cursorByPageRef = useRef<Record<number, string>>({ 1: '' });
    const requestSeqRef = useRef(0);
    const notificationTimerRef = useRef<number | null>(null);

    const resetCursors = (next: Record<number, string> = { 1: '' }) => {
        cursorByPageRef.current = next;
        setCursorByPage(next);
    };

    const rememberCursor = (page: number, cursor: string) => {
        if (!cursor || cursorByPageRef.current[page] === cursor) {
            return;
        }
        const next = { ...cursorByPageRef.current, [page]: cursor };
        cursorByPageRef.current = next;
        setCursorByPage(next);
    };

    const showNotification = (message: string) => {
        setNotification(message);
        if (notificationTimerRef.current !== null) {
            window.clearTimeout(notificationTimerRef.current);
        }
        notificationTimerRef.current = window.setTimeout(() => {
            setNotification('');
            notificationTimerRef.current = null;
        }, 3000);
    };

    useEffect(() => {
        return () => {
            if (notificationTimerRef.current !== null) {
                window.clearTimeout(notificationTimerRef.current);
            }
        };
    }, []);

    useEffect(() => {
        const requestSeq = ++requestSeqRef.current;
        let isActive = true;

        const loadSeries = async () => {
            if (activeSearch?.type === 'image') {
                return;
            }
            setIsLoading(true);
            setError(null);
            try {
                const cursor = cursorByPageRef.current[currentPage] ?? '';
                const response = activeSearch?.type === 'text'
                    ? await searchSeriesByText(activeSearch.query, currentPage, 20, cursor)
                    : await fetchSeriesList(currentPage, 20, cursor);
                if (!isActive || requestSeqRef.current !== requestSeq) {
                    return;
                }
                setSeriesList(response.data);
                setPagination(response.pagination);
                if (response.pagination.nextCursor) {
                    rememberCursor(currentPage + 1, response.pagination.nextCursor);
                }
            } catch (err) {
                console.error(err);
                if (isActive && requestSeqRef.current === requestSeq) {
                    setError(err instanceof Error ? err.message : '无法加载数据，请检查后端服务是否开启或联系管理员。');
                }
            } finally {
                if (isActive && requestSeqRef.current === requestSeq) {
                    setIsLoading(false);
                }
            }
        };
        loadSeries();
        return () => {
            isActive = false;
        };
    }, [currentPage, activeSearch]);

    const handleSeriesClick = (seriesId: string) => {
        setExpandedSeriesId(prevId => (prevId === seriesId ? null : seriesId));
    };

    const handleSeriesContextMenu = (event: React.MouseEvent, path: string) => {
        event.preventDefault();
        navigator.clipboard.writeText(path).then(() => {
            showNotification(`路径已成功复制: ${path}`);
        }).catch(err => {
            console.error('复制失败: ', err);
            showNotification('复制路径失败！');
        });
    };

    const handleMediaContextMenu = (event: React.MouseEvent, path: string) => {
        event.preventDefault();
        navigator.clipboard.writeText(path).then(() => {
            showNotification(`媒体路径已复制: ${path}`);
        }).catch(err => {
            console.error('复制媒体路径失败: ', err);
            showNotification('复制失败！');
        });
    };

    const handleSearch = (query: string) => {
        requestSeqRef.current++;
        if (!query) {
            setActiveSearch(null);
            setExpandedSeriesId(null);
            setCurrentPage(1);
            resetCursors();
            return;
        }
        setError(null);
        setActiveSearch({ type: 'text', query, label: query });
        setExpandedSeriesId(null);
        setCurrentPage(1);
        resetCursors();
    };

    const handleImageSearch = async (file: File) => {
        const requestSeq = ++requestSeqRef.current;
        setIsSearching(true);
        setError(null);
        try {
            const response = await searchByImage(file);
            if (requestSeqRef.current !== requestSeq) {
                return;
            }
            setSeriesList(response.data);
            setPagination(response.pagination);
            setActiveSearch({ type: 'image', label: `以图搜图: ${file.name}` });
            setExpandedSeriesId(null);
            setCurrentPage(1);
            resetCursors();
        } catch (err) {
            console.error(err);
            if (requestSeqRef.current === requestSeq) {
                setError(err instanceof Error ? err.message : '以图搜图失败。');
            }
        } finally {
            if (requestSeqRef.current === requestSeq) {
                setIsLoading(false);
                setIsSearching(false);
            }
        }
    };

    return (
        <div className="page">
            {notification && (
                <div className="toast">
                    {notification}
                </div>
            )}

            <header className="page-header">
                <div>
                    <h1>媒体库</h1>
                    <p>按系列查看图片、视频、音频和文本媒体。</p>
                </div>
                <div className="metric-strip">
                    <div>
                        <strong>{pagination?.totalItems ?? 0}</strong>
                        <span>系列</span>
                    </div>
                    <div>
                        <strong>{pagination?.currentPage ?? currentPage}</strong>
                        <span>当前页</span>
                    </div>
                </div>
            </header>

            <SearchBar
                onSearch={handleSearch}
                onImageSearch={handleImageSearch}
                isSearching={isLoading || isSearching}
            />

            {activeSearch && (
                <div className="query-banner">
                    当前筛选：{activeSearch.label}
                    <button className="link-button" onClick={() => handleSearch('')}>返回全部</button>
                </div>
            )}

            {error && <div className="error-banner">{error}</div>}
            {isLoading ? (
                <div className="inline-state">正在加载媒体库...</div>
            ) : (
                <>
                    <div className="series-grid">
                        {seriesList.map((series) => (
                            <div
                                key={series.id}
                                className={expandedSeriesId === series.id ? 'series-shell expanded' : 'series-shell'}
                            >
                                <SeriesItem
                                    series={series}
                                    onClick={handleSeriesClick}
                                    onContextMenu={handleSeriesContextMenu}
                                    isExpanded={expandedSeriesId === series.id}
                                />
                                {expandedSeriesId === series.id && (
                                    <MediaList
                                        seriesId={series.id}
                                        onMediaContextMenu={handleMediaContextMenu}
                                    />
                                )}
                            </div>
                        ))}
                    </div>

                    {seriesList.length === 0 && <div className="inline-state">没有匹配的系列。</div>}

                    <div className="pagination">
                        <button
                            className="button"
                            onClick={() => setCurrentPage(p => p - 1)}
                            disabled={currentPage <= 1 || activeSearch?.type === 'image'}
                        >
                            上一页
                        </button>
                        <span>
                            第 {pagination?.currentPage ?? 1} 页 / 共 {pagination?.totalPages ?? 0} 页
                        </span>
                        <button
                            className="button"
                            onClick={() => setCurrentPage(p => p + 1)}
                            disabled={activeSearch?.type === 'image' || !pagination || currentPage >= pagination.totalPages || !cursorByPage[currentPage + 1]}
                        >
                            下一页
                        </button>
                    </div>
                </>
            )}
        </div>
    );
};

export default HomePage;
