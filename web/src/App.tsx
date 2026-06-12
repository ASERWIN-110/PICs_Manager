import { BrowserRouter as Router, Routes, Route } from 'react-router-dom';
import { useEffect, useState, type FormEvent } from 'react';
import HomePage from './pages/HomePage';
import AdminPage from './pages/AdminPage';
import Navigation from './components/Navigation';
import { claimPairingCode, getAuthStatus, getDeviceToken, setDeviceToken } from './services/api';
import './App.css';

const App = () => {
    const [authRequired, setAuthRequired] = useState(false);
    const [authReady, setAuthReady] = useState(false);
    const [authError, setAuthError] = useState('');

    useEffect(() => {
        let isMounted = true;
        getAuthStatus().then(status => {
            if (!isMounted) return;
            setAuthRequired(Boolean(status.enabled && status.requireViewerForRead && !getDeviceToken()));
            setAuthReady(true);
        }).catch(err => {
            if (!isMounted) return;
            setAuthError(err instanceof Error ? err.message : '无法加载认证状态。');
            setAuthReady(true);
        });
        return () => {
            isMounted = false;
        };
    }, []);

    if (!authReady) {
        return <div className="App"><main><div className="page"><div className="inline-state">正在加载认证状态...</div></div></main></div>;
    }

    if (authRequired) {
        return <PairingPage error={authError} onPaired={() => setAuthRequired(false)} />;
    }

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

interface PairingPageProps {
    error: string;
    onPaired: () => void;
}

const PairingPage = ({ error, onPaired }: PairingPageProps) => {
    const [code, setCode] = useState('');
    const [deviceName, setDeviceName] = useState(() => navigator.userAgent.includes('Mobile') ? 'mobile-browser' : 'browser');
    const [message, setMessage] = useState(error);
    const [isSubmitting, setIsSubmitting] = useState(false);

    const handleSubmit = async (event: FormEvent) => {
        event.preventDefault();
        if (!code.trim()) {
            setMessage('请输入配对码。');
            return;
        }
        setIsSubmitting(true);
        setMessage('正在配对...');
        try {
            const result = await claimPairingCode(code.trim(), deviceName.trim());
            setDeviceToken(result.token);
            onPaired();
        } catch (err) {
            setMessage(err instanceof Error ? err.message : '配对失败。');
        } finally {
            setIsSubmitting(false);
        }
    };

    return (
        <div className="App">
            <main>
                <div className="page pairing-page">
                    <section className="tool-panel pairing-panel">
                        <div className="panel-title">
                            <h1>设备配对</h1>
                        </div>
                        {message && <div className={message === '正在配对...' ? 'inline-state' : 'error-banner'}>{message}</div>}
                        <form onSubmit={handleSubmit} className="pairing-form">
                            <label className="form-row">
                                <span>设备名</span>
                                <input value={deviceName} onChange={(event) => setDeviceName(event.target.value)} />
                            </label>
                            <label className="form-row">
                                <span>配对码</span>
                                <input value={code} onChange={(event) => setCode(event.target.value)} autoFocus />
                            </label>
                            <button className="button primary" type="submit" disabled={isSubmitting}>
                                {isSubmitting ? '配对中' : '配对'}
                            </button>
                        </form>
                    </section>
                </div>
            </main>
        </div>
    );
};

export default App;
