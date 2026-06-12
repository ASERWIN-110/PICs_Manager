import React, { useState, useRef } from 'react';
import ClearIcon from '@mui/icons-material/Clear';
import ImageSearchIcon from '@mui/icons-material/ImageSearch';
import SearchIcon from '@mui/icons-material/Search';

interface SearchBarProps {
    onSearch: (query: string) => void;
    onImageSearch: (file: File) => void;
    isSearching: boolean;
}

const SearchBar = ({ onSearch, onImageSearch, isSearching }: SearchBarProps) => {
    const [query, setQuery] = useState('');
    const fileInputRef = useRef<HTMLInputElement>(null);

    const handleSearch = (event: React.FormEvent) => {
        event.preventDefault();
        onSearch(query);
    };

    const handleUploadClick = () => fileInputRef.current?.click();

    const handleFileChange = (event: React.ChangeEvent<HTMLInputElement>) => {
        const file = event.target.files?.[0];
        if (file) {
            onImageSearch(file);
        }
        event.target.value = '';
    };

    return (
        <form onSubmit={handleSearch} className="search-bar">
            <input
                type="text"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="按系列名称搜索..."
                disabled={isSearching}
                className="search-input"
            />
            <button type="submit" disabled={isSearching || !query} className="button primary">
                <SearchIcon fontSize="small" />
                {isSearching ? '搜索中' : '搜索'}
            </button>

            <button type="button" onClick={handleUploadClick} disabled={isSearching} className="button">
                <ImageSearchIcon fontSize="small" />
                以图搜图
            </button>

            <input
                type="file"
                ref={fileInputRef}
                onChange={handleFileChange}
                style={{ display: 'none' }}
                accept="image/png, image/jpeg, image/webp, image/gif"
            />

            {query && (
                <button type="button" onClick={() => { setQuery(''); onSearch(''); }} className="icon-button" title="清空搜索">
                    <ClearIcon fontSize="small" />
                    清空
                </button>
            )}
        </form>
    );
};

export default SearchBar;
