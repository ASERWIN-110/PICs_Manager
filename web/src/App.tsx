// src/App.tsx
import { BrowserRouter as Router, Routes, Route } from 'react-router-dom';
import HomePage from './pages/HomePage';
import AdminPage from './pages/AdminPage';
import Navigation from './components/Navigation';
import './App.css';

const App = () => {
    return (
        <Router>
            <div className="App">
                <Navigation />
                <main>
                    <Routes>
                        <Route path="/" element={<HomePage />} />
                        <Route path="/admin" element={<AdminPage />} />
                    </Routes>
                </main>
            </div>
        </Router>
    );
};

export default App;