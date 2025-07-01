// 文件: web/src/pages/HomePage.tsx
import React, { useEffect, useState } from 'react';
import { fetchSeriesList, searchByImage, searchSeriesByText } from '../services/api';
import type { Pagination, Series } from '../types/entities';
import SeriesItem from '../components/SeriesItem';
import ImageList from '../components/ImageList';
import SearchBar from '../components/SearchBar';

const HomePage = () => {
    // --- State Management (无变化) ---
    const [seriesList, setSeriesList] = useState<Series[]>([]);
    const [pagination, setPagination] = useState<Pagination | null>(null);
    const [currentPage, setCurrentPage] = useState<number>(1);
    const [isLoading, setIsLoading] = useState<boolean>(true);
    const [error, setError] = useState<string | null>(null);
    const [notification, setNotification] = useState<string>('');
    const [expandedSeriesId, setExpandedSeriesId] = useState<string | null>(null);
    const [isSearching, setIsSearching] = useState(false);

    // --- Data Fetching (无变化) ---
    useEffect(() => {
        const loadSeries = async () => {
            setIsLoading(true);
            setError(null);
            try {
                const response = await fetchSeriesList(currentPage);
                setSeriesList(response.data);
                setPagination(response.pagination);
            } catch (err) {
                setError('无法加载数据，请检查后端服务是否开启或联系管理员。');
                console.error(err);
            } finally {
                setIsLoading(false);
            }
        };
        loadSeries();
    }, [currentPage]);

    // --- Event Handlers (无变化) ---
    const handleSeriesClick = (seriesId: string) => {
        setExpandedSeriesId(prevId => (prevId === seriesId ? null : seriesId));
    };

    const handleSeriesContextMenu = (event: React.MouseEvent, path: string) => {
        event.preventDefault();
        navigator.clipboard.writeText(path).then(() => {
            setNotification(`路径已成功复制: ${path}`);
            setTimeout(() => setNotification(''), 3000);
        }).catch(err => {
            console.error('复制失败: ', err);
            setNotification('复制路径失败！');
            setTimeout(() => setNotification(''), 3000);
        });
    };

    const handleImageContextMenu = (event: React.MouseEvent, path: string) => {
        event.preventDefault();
        navigator.clipboard.writeText(path).then(() => {
            setNotification(`图片路径已复制: ${path}`);
            setTimeout(() => setNotification(''), 3000);
        }).catch(err => {
            console.error('复制图片路径失败: ', err);
            setNotification('复制失败！');
            setTimeout(() => setNotification(''), 3000);
        });
    };

    const handleSearch = async (query: string) => {
        if (!query) {
            setCurrentPage(1);
            return;
        }
        setIsSearching(true);
        setError(null);
        try {
            const response = await searchSeriesByText(query);
            setSeriesList(response.data);
            setPagination(response.pagination);
        } catch (err) {
            console.error(err);
            setError('搜索失败。');
        } finally {
            setIsSearching(false);
        }
    };

    const handleImageSearch = async (file: File) => {
        console.log('开始通过图片搜索:', file.name);
        setIsSearching(true);
        setError(null);
        try {
            const response = await searchByImage(file);
            setSeriesList(response.data);
            setPagination(response.pagination);
        } catch (err) {
            console.error(err);
            setError('以图搜图失败。');
        } finally {
            setIsSearching(false);
        }
    };

    // --- Render Logic ---
    if (isLoading) {
        return <div style={{ textAlign: 'center', padding: '50px' }}>正在加载中，请稍候...</div>;
    }
    if (error) {
        return <div style={{ color: 'red', textAlign: 'center', padding: '50px' }}>{error}</div>;
    }

    return (
        // [修正] 移除了 maxWidth 和 margin: '0 auto' 样式，使其占满整个页面宽度
        <div style={{ padding: '20px', fontFamily: 'sans-serif' }}>
            {notification && (
                <div style={{
                    position: 'fixed',
                    top: '20px',
                    left: '50%',
                    transform: 'translateX(-50%)',
                    backgroundColor: 'rgba(0, 0, 0, 0.8)',
                    color: 'white',
                    padding: '10px 20px',
                    borderRadius: '8px',
                    zIndex: 1000,
                    transition: 'opacity 0.5s',
                }}>
                    {notification}
                </div>
            )}

            <h1>媒体系列</h1>

            <SearchBar
                onSearch={handleSearch}
                onImageSearch={handleImageSearch}
                isSearching={isLoading || isSearching}
            />

            {/* [修正] 删除了重复的 map 循环，只保留了包含完整逻辑的这一个 */}
            <div style={{
                display: 'grid',
                gridTemplateColumns: 'repeat(auto-fill, minmax(200px, 1fr))',
                gap: '20px',
                marginTop: '20px',
            }}>
                {seriesList.map((series) => (
                    <div
                        key={series.ID}
                        style={{
                            gridColumn: expandedSeriesId === series.ID ? '1 / -1' : 'auto',
                            transition: 'grid-column 0.3s ease',
                        }}
                    >
                        <SeriesItem
                            series={series}
                            onClick={handleSeriesClick}
                            onContextMenu={handleSeriesContextMenu}
                            isExpanded={expandedSeriesId === series.ID}
                        />
                        {expandedSeriesId === series.ID && (
                            <ImageList
                                seriesId={series.ID}
                                onImageContextMenu={handleImageContextMenu}
                            />
                        )}
                    </div>
                ))}
            </div>

            <div style={{ marginTop: '40px', textAlign: 'center' }}>
                <button onClick={() => setCurrentPage(p => p - 1)} disabled={currentPage <= 1}>
                    上一页
                </button>
                <span style={{ margin: '0 15px', fontSize: '16px' }}>
                    第 {pagination?.currentPage} 页 / 共 {pagination?.totalPages} 页
                </span>
                <button onClick={() => setCurrentPage(p => p + 1)} disabled={!pagination || currentPage >= pagination.totalPages}>
                    下一页
                </button>
            </div>
        </div>
    );
};

export default HomePage;
