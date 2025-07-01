// src/components/Navigation.tsx
import React from 'react';
import { Link } from 'react-router-dom';

const Navigation = () => {
    const navStyle: React.CSSProperties = {
        background: '#333',
        padding: '10px 20px',
        display: 'flex',
        gap: '20px',
    };

    const linkStyle: React.CSSProperties = {
        color: 'white',
        textDecoration: 'none',
        fontSize: '16px',
    };

    return (
        <nav style={navStyle}>
            <Link to="/" style={linkStyle}>主页</Link>
            <Link to="/admin" style={linkStyle}>管理后台</Link>
        </nav>
    );
};

export default Navigation;