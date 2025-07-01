// src/components/ImageList.tsx
import React, { useState, useEffect } from 'react';
import { fetchImagesBySeriesId } from '../services/api';
import type { Image } from '../types/entities';
interface ImageListProps {
    seriesId: string;
    onImageContextMenu: (event: React.MouseEvent, path: string) => void;
}

const ImageList: React.FC<ImageListProps> = ({ seriesId, onImageContextMenu }:ImageListProps) => {
    const [images, setImages] = useState<Image[]>([]);
    const [isLoading, setIsLoading] = useState(true);
    const [error, setError] = useState<string | null>(null);

    useEffect(() => {
        const loadImages = async () => {
            setIsLoading(true);
            setError(null);
            try {
                const imageData = await fetchImagesBySeriesId(seriesId);
                setImages(imageData);
            } catch (err) {
                console.error(err);
                setError('无法加载图片列表。');
            } finally {
                setIsLoading(false);
            }
        };
        loadImages();
    }, [seriesId]); // 当 seriesId 变化时重新获取

    if (isLoading) {
        return <div style={{ padding: '20px', color: '#666' }}>正在加载图片...</div>;
    }

    if (error) {
        return <div style={{ padding: '20px', color: 'red' }}>{error}</div>;
    }

    return (
        <div style={{
            display: 'grid',
            gridTemplateColumns: 'repeat(auto-fill, minmax(150px, 1fr))',
            gap: '10px',
            marginTop: '10px',
        }}>
            {images.map(image => (
                <div
                    key={image.ID}
                    title={image.FileName}
                    onContextMenu={(e) => onImageContextMenu(e, image.FilePath)} // 4. 绑定右键事件
                    style={{ cursor: 'context-menu' }}
                >
                    <img
                        src={image.Thumbnail} // 同样假设是Base64或可直接访问的URL
                        alt={image.FileName}
                        style={{ width: '100%', height: 'auto', borderRadius: '4px', display: 'block' }}
                    />
                </div>
            ))}
        </div>
    );
};

export default ImageList;