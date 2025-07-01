// src/components/SeriesItem.tsx
import React from 'react';
import type { Series } from '../types/entities';

// 定义组件接收的 props 类型
interface SeriesItemProps {
    series: Series;
    // 我们预先定义好点击事件的回调函数类型，下一步会用到
    onClick: (seriesId: string) => void;
    onContextMenu: (event: React.MouseEvent, path: string) => void;
    isExpanded: boolean;
}

const SeriesItem: React.FC<SeriesItemProps> = ({ series, onClick, onContextMenu, isExpanded }) => {
    const cardStyles: React.CSSProperties = {
        border: '1px solid #ddd',
        borderRadius: '8px',
        overflow: 'hidden',
        cursor: 'pointer',
        position: 'relative', // 用于定位信息层
        boxShadow: '0 2px 5px rgba(0,0,0,0.1)',
        transition: 'transform 0.2s ease-in-out',
        maxWidth: isExpanded ? '300px' : 'none',
    };

    const imageContainerStyles: React.CSSProperties = {
        width: '100%',
        paddingTop: '100%', // 保持1:1的宽高比
        position: 'relative',
        backgroundColor: '#f0f0f0', // 图片加载前的占位背景
    };

    const imageStyles: React.CSSProperties = {
        position: 'absolute',
        top: 0,
        left: 0,
        width: '100%',
        height: '100%',
        objectFit: 'cover', // 确保图片不变形地填满容器
    };

    const infoStyles: React.CSSProperties = {
        position: 'absolute',
        bottom: 0,
        left: 0,
        right: 0,
        backgroundColor: 'rgba(0, 0, 0, 0.6)',
        color: 'white',
        padding: '8px',
        textAlign: 'left',
        fontSize: '14px',
    };

    return (
        <div
            style={cardStyles}
            onClick={() => onClick(series.ID)}
            onContextMenu={(e) => onContextMenu(e, series.Path)}
            onMouseEnter={(e) => e.currentTarget.style.transform = 'scale(1.03)'}
            onMouseLeave={(e) => e.currentTarget.style.transform = 'scale(1)'}
        >
            <div style={imageContainerStyles}>
                {/*
          这里我们假设 series.Thumbnail 是一个 Base64 编码的 Data URL。
          如果它是一个相对路径，你可能需要拼接成完整的 URL，
          例如: `http://localhost:8080${series.Thumbnail}`
        */}
                <img src={series.Thumbnail} alt={series.Name} style={imageStyles} />
            </div>
            <div style={infoStyles}>
                <div style={{ fontWeight: 'bold', whiteSpace: 'nowrap', overflow: 'hidden', textOverflow: 'ellipsis' }}>
                    {series.Name}
                </div>
                <div>{series.ImageCount} items</div>
            </div>
        </div>
    );
};

export default SeriesItem;