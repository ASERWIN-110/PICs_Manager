import React from 'react';
import FolderIcon from '@mui/icons-material/Folder';
import type { Series } from '../types/entities';
import { resolveApiAssetUrl } from '../services/api';

interface SeriesItemProps {
    series: Series;
    onClick: (seriesId: string) => void;
    onContextMenu: (event: React.MouseEvent, path: string) => void;
    isExpanded: boolean;
}

const SeriesItem: React.FC<SeriesItemProps> = ({ series, onClick, onContextMenu, isExpanded }) => {
    return (
        <div
            className={isExpanded ? 'series-card expanded' : 'series-card'}
            onClick={() => onClick(series.id)}
            onContextMenu={(e) => onContextMenu(e, series.path)}
        >
            <div className="series-cover">
                {series.thumbnailUrl ? (
                    <img loading="lazy" src={resolveApiAssetUrl(series.thumbnailUrl)} alt={series.name} />
                ) : (
                    <div className="series-cover-placeholder">
                        <FolderIcon fontSize="large" />
                    </div>
                )}
            </div>
            <div className="series-info">
                <div className="series-name">{series.name}</div>
                <div className="series-meta">{series.imageCount} 个媒体</div>
            </div>
        </div>
    );
};

export default SeriesItem;
