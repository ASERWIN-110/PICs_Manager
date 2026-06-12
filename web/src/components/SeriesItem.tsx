import React from 'react';
import DownloadIcon from '@mui/icons-material/Download';
import FolderIcon from '@mui/icons-material/Folder';
import type { Series } from '../types/entities';
import { downloadApiFile, resolveApiAssetUrl } from '../services/api';

interface SeriesItemProps {
    series: Series;
    onClick: (seriesId: string) => void;
    onContextMenu: (event: React.MouseEvent, path: string) => void;
    isExpanded: boolean;
}

const SeriesItem: React.FC<SeriesItemProps> = ({ series, onClick, onContextMenu, isExpanded }) => {
    const handleDownload = async (event: React.MouseEvent) => {
        event.stopPropagation();
        await downloadApiFile(`/series/${series.id}/download`, `${series.name}.zip`);
    };

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
                <div className="series-meta">
                    <span>{series.imageCount} 个媒体</span>
                    <button className="tile-action" type="button" onClick={handleDownload} title="下载系列">
                        <DownloadIcon fontSize="small" />
                    </button>
                </div>
            </div>
        </div>
    );
};

export default SeriesItem;
