// src/components/SearchBar.tsx
import React, { useState, useRef } from 'react'; // 1. 引入 useRef

interface SearchBarProps {
    onSearch: (query: string) => void;
    onImageSearch: (file: File) => void;
    isSearching: boolean; // 用于控制按钮的禁用状态
}

const SearchBar = ({ onSearch, onImageSearch, isSearching }: SearchBarProps) => {
    const [query, setQuery] = useState('');

    const fileInputRef = useRef<HTMLInputElement>(null);

    const handleSearch = (event: React.FormEvent) => {
        event.preventDefault(); // 阻止表单提交导致页面刷新
        onSearch(query);
    };

    const handleUploadClick = () => {
        // 4. 当点击“上传”按钮时，以编程方式触发隐藏的文件输入框
        fileInputRef.current?.click();
    };

    const handleFileChange = (event: React.ChangeEvent<HTMLInputElement>) => {
        // 5. 当用户选择了文件后
        const file = event.target.files?.[0];
        if (file) {
            onImageSearch(file);
        }
        // 重置输入框的值，以便用户可以再次选择同一个文件
        event.target.value = '';
    };

    return (
        <form onSubmit={handleSearch} style={{ display: 'flex', gap: '10px', marginBottom: '20px' }}>
            <input
                type="text"
                value={query}
                onChange={(e) => setQuery(e.target.value)}
                placeholder="按系列名称搜索..."
                style={{ flexGrow: 1, padding: '8px', fontSize: '16px' }}
                disabled={isSearching}
            />
            <button type="submit" disabled={isSearching || !query}>
                {isSearching ? '搜索中...' : '搜索'}
            </button>

            {/* 添加一个“以图搜图”的按钮 */}
            <button type="button" onClick={handleUploadClick} disabled={isSearching}>
                上传图片搜索
            </button>

            {/* 这是隐藏的、实际的文件输入框 */}
            <input
                type="file"
                ref={fileInputRef}
                onChange={handleFileChange}
                style={{ display: 'none' }}
                accept="image/png, image/jpeg, image/webp" // 限制文件类型
            />

            {/* 如果输入框有内容，则显示一个清空按钮 */}
            {query && (
                <button type="button" onClick={() => { setQuery(''); onSearch(''); }}>
                    清空
                </button>
            )}
        </form>
    );
};

export default SearchBar;