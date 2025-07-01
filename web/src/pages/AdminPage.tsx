// src/pages/AdminPage.tsx
import React, { useState, useEffect } from 'react';
import type { AppConfig } from '../types/config';
import { getConfig, updateConfig, startScanTask, getTaskStatus } from '../services/api';

// 创建一个可重用的输入组件，简化表单代码
interface ConfigInputProps {
    label: string;
    name: string;
    value: string | number;
    onChange: (event: React.ChangeEvent<HTMLInputElement>) => void;
    type?: string;
    width?: string;
}

const ConfigInput = ({ label, name, value, onChange, type = 'text', width = '400px' }: ConfigInputProps) => (
    <div style={{ marginBottom: '10px' }}>
        <label style={{ display: 'inline-block', width: '150px', marginRight: '10px', textAlign: 'right' }}>{label}:</label>
        <input
            type={type}
            name={name}
            value={value}
            onChange={onChange}
            style={{ width }}
        />
    </div>
);


const AdminPage = () => {
    // --- State Management ---
    const [config, setConfig] = useState<AppConfig | null>(null);
    const [configMessage, setConfigMessage] = useState('');
    const [ScanPath, setScanPath] = useState('');
    const [taskMessage, setTaskMessage] = useState('');
    const [isPolling, setIsPolling] = useState(false);

    // --- Data Loading ---
    useEffect(() => {
        getConfig().then(data => {
            setConfig(data);
            if (data?.Scanner?.ScanPath) {
                setScanPath(data.Scanner.ScanPath);
            }
        }).catch(err => {
            console.error(err); // [修正] 使用 err 变量进行日志记录
            setConfigMessage('无法加载配置。');
        });
    }, []);

    // --- Event Handlers ---
    const handleConfigChange = (event: React.ChangeEvent<HTMLInputElement | HTMLTextAreaElement>) => {
        const { name, value } = event.target;
        const keys = name.split('.');

        setConfig(prevConfig => {
            if (!prevConfig) return null;
            const newConfig = JSON.parse(JSON.stringify(prevConfig));

            // [修正] 使用 Record<string, any> 替代 any，类型更安全
            let currentLevel: Record<string, any> = newConfig;
            for (let i = 0; i < keys.length - 1; i++) {
                currentLevel = currentLevel[keys[i]];
            }

            const lastKey = keys[keys.length - 1];
            const originalValue = currentLevel[lastKey];
            currentLevel[lastKey] = typeof originalValue === 'number' ? Number(value) : value;

            return newConfig;
        });
    };

    const handleArrayChange = (event: React.ChangeEvent<HTMLTextAreaElement>) => {
        // [修正] 移除了未使用的 `keys` 变量
        const { value } = event.target;

        setConfig(prevConfig => {
            if (!prevConfig) return null;
            const newConfig = JSON.parse(JSON.stringify(prevConfig));
            newConfig.scanner.FilePatterns = value.split('\n');
            return newConfig;
        });
    };

    const handleSaveConfig = async (event: React.FormEvent) => {
        event.preventDefault();
        if (!config) return;
        setConfigMessage('保存中...');
        try {
            await updateConfig(config);
            setConfigMessage('配置已成功保存！');
        } catch (err) {
            console.error(err); // [修正] 使用 err 变量进行日志记录
            setConfigMessage('保存失败。');
        } finally {
            setTimeout(() => setConfigMessage(''), 3000);
        }
    };

    // [修正] 实现了 handleStartScan 函数的完整逻辑
    const handleStartScan = async () => {
        if (!ScanPath) {
            setTaskMessage('请输入要扫描的目录路径。');
            return;
        }
        setTaskMessage('正在提交任务...');
        setIsPolling(true);
        try {
            const { taskId } = await startScanTask(ScanPath);
            setTaskMessage(`任务已开始 (ID: ${taskId})，正在轮询状态...`);

            const intervalId = setInterval(async () => {
                try {
                    const { status, progress } = await getTaskStatus(taskId);
                    setTaskMessage(`任务状态: ${status}, 进度: ${progress.toFixed(2)}%`);
                    if (status === 'completed' || status === 'failed') {
                        clearInterval(intervalId);
                        setIsPolling(false);
                        setTaskMessage(`任务完成！最终状态: ${status}`);
                    }
                } catch (pollErr) {
                    console.error(pollErr);
                    setTaskMessage('轮询任务状态失败。');
                    clearInterval(intervalId);
                    setIsPolling(false);
                }
            }, 2000);
        } catch (startErr) {
            console.error(startErr);
            setTaskMessage('启动扫描任务失败。');
            setIsPolling(false);
        }
    };

    // --- Render Logic ---
    const renderConfigForm = () => {
        if (!config) return <p>正在加载配置...</p>;

        return (
            <form onSubmit={handleSaveConfig}>
                {/* ... 表单的 JSX 内容无变化 ... */}
                <fieldset style={{ marginBottom: '20px' }}>
                    <legend>服务器配置</legend>
                    <ConfigInput label="端口 (Port)" name="server.port" value={config.Server.port} onChange={handleConfigChange} />
                    <ConfigInput label="超时 (Timeout)" name="server.timeout" value={config.Server.timeout} onChange={handleConfigChange} />
                </fieldset>
                <fieldset style={{ marginBottom: '20px' }}>
                    <legend>数据库配置</legend>
                    <ConfigInput label="URI" name="database.uri" value={config.Database.uri} onChange={handleConfigChange} />
                    <ConfigInput label="库名 (Name)" name="database.name" value={config.Database.name} onChange={handleConfigChange} />
                </fieldset>
                <fieldset style={{ marginBottom: '20px' }}>
                    <legend>日志配置</legend>
                    <ConfigInput label="级别 (Level)" name="logger.level" value={config.Logger.level} onChange={handleConfigChange} />
                    <ConfigInput label="格式 (Format)" name="logger.format" value={config.Logger.format} onChange={handleConfigChange} />
                    <ConfigInput label="路径 (Path)" name="logger.path" value={config.Logger.path} onChange={handleConfigChange} />
                </fieldset>
                <fieldset style={{ marginBottom: '20px' }}>
                    <legend>扫描器配置</legend>
                    <ConfigInput label="扫描路径 (ScanPath)" name="scanner.ScanPath" value={config.Scanner.ScanPath} onChange={handleConfigChange} />
                    <ConfigInput label="中转站 (StagingPath)" name="scanner.StagingPath" value={config.Scanner.StagingPath} onChange={handleConfigChange} />
                    <ConfigInput label="最终库 (FinalLibraryPath)" name="scanner.FinalLibraryPath" value={config.Scanner.FinalLibraryPath} onChange={handleConfigChange} />
                    <ConfigInput label="备份路径 (BackupPath)" name="scanner.BackupPath" value={config.Scanner.BackupPath} onChange={handleConfigChange} />
                    <ConfigInput label="隔离区 (QuarantinePath)" name="scanner.QuarantinePath" value={config.Scanner.QuarantinePath} onChange={handleConfigChange} />
                    <ConfigInput label="损坏日志 (CorruptionLogPath)" name="scanner.CorruptionLogPath" value={config.Scanner.CorruptionLogPath} onChange={handleConfigChange} />
                    <ConfigInput label="重复目录 (DuplicatesDir)" name="scanner.DuplicatesDir" value={config.Scanner.DuplicatesDir} onChange={handleConfigChange} />
                    <ConfigInput label="并发数 (WorkerCount)" name="scanner.WorkerCount" value={config.Scanner.WorkerCount} onChange={handleConfigChange} type="number" />
                    <ConfigInput label="批处理大小 (BatchSize)" name="scanner.BatchSize" value={config.Scanner.BatchSize} onChange={handleConfigChange} type="number" />
                    <div style={{ marginTop: '20px' }}>
                        <label>文件匹配规则 (FilePatterns):</label>
                        <p style={{fontSize: '12px', color: '#666', margin: '5px 0'}}>每行一个正则表达式</p>
                        <textarea
                            name="scanner.FilePatterns"
                            value={config.Scanner.FilePatterns.join('\n')}
                            onChange={handleArrayChange}
                            style={{ width: '90%', height: '100px', fontFamily: 'monospace' }}
                        />
                    </div>
                    <div style={{ marginTop: '20px' }}>
                        <label>系列分组规则 (SeriesGroupPatterns): (只读)</label>
                        <pre style={{background: '#f4f4f4', padding: '10px', borderRadius: '4px'}}>
                            {JSON.stringify(config.Scanner.SeriesGroupPatterns, null, 2)}
                        </pre>
                    </div>
                </fieldset>
                <button type="submit" style={{ padding: '10px 20px', fontSize: '16px' }}>保存所有配置</button>
                {configMessage && <p style={{ marginTop: '10px', color: 'green' }}>{configMessage}</p>}
            </form>
        );
    };

    return (
        <div style={{ padding: '20px', fontFamily: 'sans-serif' }}>
            <h2>管理后台</h2>
            <div style={{ border: '1px solid #ccc', padding: '20px', marginBottom: '20px', borderRadius: '8px' }}>
                <h3>数据入库</h3>
                <p>指定一个服务器上的绝对路径，然后开始扫描。默认使用下方配置中的"扫描路径"。</p>
                <input
                    type="text"
                    value={ScanPath}
                    onChange={(e) => setScanPath(e.target.value)}
                    placeholder="/path/to/your/media/library"
                    style={{ width: '400px', marginRight: '10px', padding: '8px' }}
                    disabled={isPolling}
                />
                <button onClick={handleStartScan} disabled={isPolling} style={{ padding: '8px 16px' }}>
                    {isPolling ? '扫描中...' : '开始扫描'}
                </button>
                {taskMessage && <p style={{ marginTop: '10px' }}>{taskMessage}</p>}
            </div>
            <div style={{ border: '1px solid #ccc', padding: '20px', borderRadius: '8px' }}>
                <h3>应用配置 (config.yaml)</h3>
                {renderConfigForm()}
            </div>
        </div>
    );
};

export default AdminPage;