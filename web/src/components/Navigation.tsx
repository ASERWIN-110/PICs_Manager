import AdminPanelSettingsIcon from '@mui/icons-material/AdminPanelSettings';
import CollectionsIcon from '@mui/icons-material/Collections';
import { NavLink } from 'react-router-dom';

const Navigation = () => {
    return (
        <nav className="top-nav">
            <div className="brand">PICs Manager</div>
            <div className="nav-links">
                <NavLink to="/" className={({ isActive }) => isActive ? 'nav-link active' : 'nav-link'}>
                    <CollectionsIcon fontSize="small" />
                    媒体库
                </NavLink>
                <NavLink to="/admin" className={({ isActive }) => isActive ? 'nav-link active' : 'nav-link'}>
                    <AdminPanelSettingsIcon fontSize="small" />
                    管理
                </NavLink>
            </div>
        </nav>
    );
};

export default Navigation;
