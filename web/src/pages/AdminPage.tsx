import React, { useEffect, useMemo, useRef, useState } from 'react';
import AddIcon from '@mui/icons-material/Add';
import DeleteIcon from '@mui/icons-material/Delete';
import PlayArrowIcon from '@mui/icons-material/PlayArrow';
import SaveIcon from '@mui/icons-material/Save';
import type { AppConfig, MediaTypeConfig } from '../types/config';
import { getConfig, getTaskStatus, startScanTask, updateConfig } from '../services/api';

interface ConfigInputProps {
    label: string;
    name: string;
    value: string | number;
    onChange: (event: React.ChangeEvent<HTMLInputElement>) => void;
    type?: string;
    disabled?: boolean;
}

interface ConfigCheckboxProps {
    label: string;
    name: string;
    checked: boolean;
    onChange: (event: React.ChangeEvent<HTMLInputElement>) => void;
}

type MutableConfigNode = Record<string, unknown>;

const cloneConfig = (config: AppConfig): AppConfig => JSON.parse(JSON.stringify(config)) as AppConfig;

const splitExtensions = (value: string): string[] => value
    .split(/[\n,]+/)
    .map(item => item.trim())
    .filter(Boolean);

const splitPatterns = (value: string): string[] => value
    .split('\n')
    .filter(item => item.trim() !== '');

const joinLines = (value: string[]): string => value.join('\n');

const validateMediaTypes = (mediaTypes: MediaTypeConfig[]): string => {
    const seen = new Set<string>();
    for (let index = 0; index < mediaTypes.length; index += 1) {
        const item = mediaTypes[index];
        const type = item.type.trim();
        if (!type) {
            return `第 ${index + 1} 个媒体类型缺少 type`;
        }
        if (seen.has(type)) {
            return `媒体类型 ${type} 重复`;
        }
        seen.add(type);
        if (item.extensions.length === 0) {
            return `${type} 至少需要一个扩展名`;
        }
        if (item.filePatterns.length === 0) {
            return `${type} 至少需要一个分类正则`;
        }
    }
    return '';
};

const nextCustomType = (mediaTypes: MediaTypeConfig[]): string => {
    const existing = new Set(mediaTypes.map(item => item.type));
    if (!existing.has('custom')) {
        return 'custom';
    }
    for (let index = 2; ; index += 1) {
        const candidate = `custom_${index}`;
        if (!existing.has(candidate)) {
            return candidate;
        }
    }
};

const ConfigInput = ({ label, name, value, onChange, type = 'text', disabled = false }: ConfigInputProps) => (
    <label className="form-row">
        <span>{label}</span>
        <input type={type} name={name} value={value} onChange={onChange} disabled={disabled} />
    </label>
);

const ConfigCheckbox = ({ label, name, checked, onChange }: ConfigCheckboxProps) => (
    <label className="form-row checkbox-row">
        <span>{label}</span>
        <input type="checkbox" name={name} checked={checked} onChange={onChange} />
    </label>
);

const AdminPage = () => {
    const [config, setConfig] = useState<AppConfig | null>(null);
    const [configMessage, setConfigMessage] = useState('');
    const [configError, setConfigError] = useState('');
    const [scanPath, setScanPath] = useState('');
    const [scanMode, setScanMode] = useState('full');
    const [taskMessage, setTaskMessage] = useState('');
    const [isPolling, setIsPolling] = useState(false);
    const pollTimerRef = useRef<number | null>(null);
    const isMountedRef = useRef(false);
    const scanInFlightRef = useRef(false);

    useEffect(() => {
        isMountedRef.current = true;
        getConfig().then(data => {
            if (!isMountedRef.current) {
                return;
            }
            setConfig(data);
            setScanPath(data.scanner.scanPath ?? '');
            setScanMode(data.scanner.mode || 'full');
        }).catch(err => {
            console.error(err);
            if (isMountedRef.current) {
                setConfigError(err instanceof Error ? err.message : '无法加载配置。');
            }
        });

        return () => {
            isMountedRef.current = false;
            scanInFlightRef.current = false;
            if (pollTimerRef.current !== null) {
                window.clearTimeout(pollTimerRef.current);
                pollTimerRef.current = null;
            }
        };
    }, []);

    const mediaTypeSummary = useMemo(() => {
        if (!config?.scanner.mediaTypes?.length) {
            return '未配置媒体类型';
        }
        return config.scanner.mediaTypes
            .map(item => `${item.type}: ${item.extensions.join(', ')}`)
            .join(' | ');
    }, [config]);

    const mediaTypesError = useMemo(() => {
        if (!config) {
            return '';
        }
        return validateMediaTypes(config.scanner.mediaTypes ?? []);
    }, [config]);

    const handleConfigChange = (event: React.ChangeEvent<HTMLInputElement>) => {
        const { name, value } = event.target;
        const keys = name.split('.');

        setConfig(prevConfig => {
            if (!prevConfig) return null;
            const nextConfig = cloneConfig(prevConfig);
            let currentLevel: MutableConfigNode = nextConfig as unknown as MutableConfigNode;
            for (let i = 0; i < keys.length - 1; i++) {
                currentLevel = currentLevel[keys[i]] as MutableConfigNode;
            }

            const lastKey = keys[keys.length - 1];
            const originalValue = currentLevel[lastKey];
            currentLevel[lastKey] = typeof originalValue === 'number' ? Number(value) : value;
            return nextConfig;
        });
    };

    const handleCheckboxChange = (event: React.ChangeEvent<HTMLInputElement>) => {
        const { name, checked } = event.target;
        const keys = name.split('.');

        setConfig(prevConfig => {
            if (!prevConfig) return null;
            const nextConfig = cloneConfig(prevConfig);
            let currentLevel: MutableConfigNode = nextConfig as unknown as MutableConfigNode;
            for (let i = 0; i < keys.length - 1; i++) {
                currentLevel = currentLevel[keys[i]] as MutableConfigNode;
            }
            currentLevel[keys[keys.length - 1]] = checked;
            return nextConfig;
        });
    };

    const handleModeChange = (event: React.ChangeEvent<HTMLSelectElement>) => {
        const mode = event.target.value;
        setScanMode(mode);
        setConfig(prev => {
            if (!prev) return null;
            const next = cloneConfig(prev);
            next.scanner.mode = mode;
            return next;
        });
    };

    const handleFilePatternsChange = (event: React.ChangeEvent<HTMLTextAreaElement>) => {
        const { value } = event.target;
        setConfig(prevConfig => {
            if (!prevConfig) return null;
            const nextConfig = cloneConfig(prevConfig);
            nextConfig.scanner.filePatterns = splitPatterns(value);
            return nextConfig;
        });
    };

    const updateMediaType = (index: number, updater: (item: MediaTypeConfig) => MediaTypeConfig) => {
        setConfig(prevConfig => {
            if (!prevConfig) return null;
            const nextConfig = cloneConfig(prevConfig);
            nextConfig.scanner.mediaTypes = nextConfig.scanner.mediaTypes.map((item, itemIndex) => (
                itemIndex === index ? updater(item) : item
            ));
            return nextConfig;
        });
    };

    const handleMediaTypeNameChange = (index: number, value: string) => {
        updateMediaType(index, item => ({ ...item, type: value.trim() }));
    };

    const handleMediaTypeExtensionsChange = (index: number, value: string) => {
        updateMediaType(index, item => ({ ...item, extensions: splitExtensions(value) }));
    };

    const handleMediaTypePatternsChange = (index: number, value: string) => {
        updateMediaType(index, item => ({ ...item, filePatterns: splitPatterns(value) }));
    };

    const handleAddMediaType = () => {
        setConfig(prevConfig => {
            if (!prevConfig) return null;
            const nextConfig = cloneConfig(prevConfig);
            nextConfig.scanner.mediaTypes = [
                ...nextConfig.scanner.mediaTypes,
                {
                    type: nextCustomType(nextConfig.scanner.mediaTypes),
                    extensions: ['.ext'],
                    filePatterns: ['^(.*?)_(\\d+)(\\.[a-zA-Z0-9_]+)?$'],
                },
            ];
            return nextConfig;
        });
    };

    const handleRemoveMediaType = (index: number) => {
        setConfig(prevConfig => {
            if (!prevConfig) return null;
            const nextConfig = cloneConfig(prevConfig);
            nextConfig.scanner.mediaTypes = nextConfig.scanner.mediaTypes.filter((_, itemIndex) => itemIndex !== index);
            return nextConfig;
        });
    };

    const handleSaveConfig = async (event: React.FormEvent) => {
        event.preventDefault();
        if (!config) return;
        if (mediaTypesError) {
            setConfigError(`媒体类型配置无效: ${mediaTypesError}`);
            return;
        }
        setConfigMessage('保存中...');
        setConfigError('');
        try {
            const saved = await updateConfig(config);
            if (!isMountedRef.current) {
                return;
            }
            setConfig(saved);
            setConfigMessage('配置已保存。');
        } catch (err) {
            console.error(err);
            if (isMountedRef.current) {
                setConfigError(err instanceof Error ? err.message : '保存失败。');
                setConfigMessage('');
            }
        }
    };

    const stopPolling = () => {
        if (pollTimerRef.current !== null) {
            window.clearTimeout(pollTimerRef.current);
            pollTimerRef.current = null;
        }
        scanInFlightRef.current = false;
        if (isMountedRef.current) {
            setIsPolling(false);
        }
    };

    const handleStartScan = async () => {
        if (scanInFlightRef.current || isPolling) {
            return;
        }
        if (!scanPath.trim()) {
            setTaskMessage('请输入要扫描的目录路径。');
            return;
        }
        scanInFlightRef.current = true;
        setTaskMessage('正在提交任务...');
        setIsPolling(true);
        try {
            const { taskId } = await startScanTask(scanPath.trim(), scanMode);
            if (!isMountedRef.current) {
                return;
            }
            setTaskMessage(`任务已开始: ${taskId}`);

            const pollTask = async () => {
                try {
                    const task = await getTaskStatus(taskId);
                    if (!isMountedRef.current) {
                        return;
                    }
                    setTaskMessage(`任务状态: ${task.status}, 进度: ${task.progress.toFixed(2)}%${task.error ? `, 错误: ${task.error}` : ''}`);
                    if (task.status === 'completed' || task.status === 'failed') {
                        stopPolling();
                        return;
                    }
                } catch (err) {
                    console.error(err);
                    if (isMountedRef.current) {
                        setTaskMessage(err instanceof Error ? err.message : '轮询任务状态失败。');
                    }
                    stopPolling();
                    return;
                }
                pollTimerRef.current = window.setTimeout(pollTask, 2000);
            };
            pollTask();
        } catch (err) {
            console.error(err);
            scanInFlightRef.current = false;
            if (isMountedRef.current) {
                setTaskMessage(err instanceof Error ? err.message : '启动扫描任务失败。');
                setIsPolling(false);
            }
        }
    };

    if (configError && !config) {
        return <div className="page"><div className="error-banner">{configError}</div></div>;
    }

    if (!config) {
        return <div className="page"><div className="inline-state">正在加载配置...</div></div>;
    }

    return (
        <div className="page admin-page">
            <header className="page-header">
                <div>
                    <h1>管理</h1>
                    <p>控制扫描任务、媒体类型规则和后端配置。</p>
                </div>
                <div className="metric-strip wide">
                    <div>
                        <strong>{config.scanner.workerCount || 'auto'}</strong>
                        <span>worker</span>
                    </div>
                    <div>
                        <strong>{config.scanner.batchSize || 100}</strong>
                        <span>batch</span>
                    </div>
                </div>
            </header>

            <section className="tool-panel">
                <div className="panel-title">
                    <h2>扫描任务</h2>
                    <span>{scanMode === 'classifyOnly' ? '只分类，不入库' : '分类并同步数据库'}</span>
                </div>
                <div className="scan-row">
                    <input
                        type="text"
                        value={scanPath}
                        onChange={(e) => setScanPath(e.target.value)}
                        placeholder="/path/to/media"
                        disabled={isPolling}
                    />
                    <select value={scanMode} onChange={handleModeChange} disabled={isPolling}>
                        <option value="full">分类并入库</option>
                        <option value="classifyOnly">只分类不入库</option>
                    </select>
                    <button className="button primary" onClick={handleStartScan} disabled={isPolling}>
                        <PlayArrowIcon fontSize="small" />
                        {isPolling ? '扫描中' : '开始扫描'}
                    </button>
                </div>
                {taskMessage && <div className="task-message">{taskMessage}</div>}
            </section>

            <form className="config-layout" onSubmit={handleSaveConfig}>
                <section className="tool-panel">
                    <div className="panel-title">
                        <h2>运行配置</h2>
                        <button className="button primary" type="submit" disabled={Boolean(mediaTypesError)}>
                            <SaveIcon fontSize="small" />
                            保存
                        </button>
                    </div>
                    {configMessage && <div className="success-banner">{configMessage}</div>}
                    {configError && <div className="error-banner">{configError}</div>}

                    <div className="form-grid">
                        <ConfigInput label="服务端口" name="server.port" value={config.server.port} onChange={handleConfigChange} />
                        <ConfigInput label="请求超时" name="server.timeout" value={config.server.timeout} onChange={handleConfigChange} />
                        <ConfigInput label="数据库 URI" name="database.uri" value={config.database.uri} onChange={handleConfigChange} disabled />
                        <ConfigInput label="数据库名" name="database.name" value={config.database.name} onChange={handleConfigChange} />
                        <ConfigInput label="日志路径" name="logger.path" value={config.logger.path} onChange={handleConfigChange} />
                        <ConfigInput label="日志级别" name="logger.level" value={config.logger.level} onChange={handleConfigChange} />
                        <ConfigInput label="扫描路径" name="scanner.scanPath" value={config.scanner.scanPath} onChange={handleConfigChange} />
                        <ConfigInput label="中转站" name="scanner.stagingPath" value={config.scanner.stagingPath} onChange={handleConfigChange} />
                        <ConfigInput label="最终库" name="scanner.finalLibraryPath" value={config.scanner.finalLibraryPath} onChange={handleConfigChange} />
                        <ConfigInput label="备份路径" name="scanner.backupPath" value={config.scanner.backupPath} onChange={handleConfigChange} />
                        <ConfigInput label="隔离区" name="scanner.quarantinePath" value={config.scanner.quarantinePath} onChange={handleConfigChange} />
                        <ConfigInput label="损坏日志" name="scanner.corruptionLogPath" value={config.scanner.corruptionLogPath} onChange={handleConfigChange} />
                        <ConfigInput label="重复目录" name="scanner.duplicatesDir" value={config.scanner.duplicatesDir} onChange={handleConfigChange} />
                        <ConfigInput label="并发数" name="scanner.workerCount" value={config.scanner.workerCount} onChange={handleConfigChange} type="number" />
                        <ConfigInput label="批处理大小" name="scanner.batchSize" value={config.scanner.batchSize} onChange={handleConfigChange} type="number" />
                        <ConfigInput label="IO 节流 ms" name="scanner.ioThrottleMs" value={config.scanner.ioThrottleMs} onChange={handleConfigChange} type="number" />
                        <ConfigInput label="维护窗口" name="scanner.maintenanceWindow" value={config.scanner.maintenanceWindow} onChange={handleConfigChange} />
                        <ConfigInput label="目录阈值" name="scanner.maxFilesPerDir" value={config.scanner.maxFilesPerDir} onChange={handleConfigChange} type="number" />
                        <ConfigCheckbox label="允许 symlink" name="scanner.followSymlinks" checked={config.scanner.followSymlinks} onChange={handleCheckboxChange} />
                        <ConfigCheckbox label="启用绑定" name="security.enabled" checked={config.security.enabled} onChange={handleCheckboxChange} />
                        <ConfigCheckbox label="查看需配对" name="security.requireViewerForRead" checked={config.security.requireViewerForRead} onChange={handleCheckboxChange} />
                        <ConfigCheckbox label="本机 admin" name="security.allowLocalAdmin" checked={config.security.allowLocalAdmin} onChange={handleCheckboxChange} />
                        <ConfigInput label="配对有效期" name="security.defaultPairingTTL" value={config.security.defaultPairingTTL} onChange={handleConfigChange} />
                        <ConfigCheckbox label="启用调度" name="scheduler.enabled" checked={config.scheduler.enabled} onChange={handleCheckboxChange} />
                        <ConfigInput label="调度间隔" name="scheduler.interval" value={config.scheduler.interval} onChange={handleConfigChange} />
                        <ConfigInput label="调度模式" name="scheduler.mode" value={config.scheduler.mode} onChange={handleConfigChange} />
                        <ConfigCheckbox label="启动即扫" name="scheduler.runOnStartup" checked={config.scheduler.runOnStartup} onChange={handleCheckboxChange} />
                        <ConfigInput label="保留运行数" name="runRetention.maxRuns" value={config.runRetention.maxRuns} onChange={handleConfigChange} type="number" />
                        <ConfigInput label="保留天数" name="runRetention.maxAgeDays" value={config.runRetention.maxAgeDays} onChange={handleConfigChange} type="number" />
                    </div>
                </section>

                <section className="tool-panel">
                    <div className="panel-title">
                        <h2>分类规则</h2>
                        <span>{mediaTypeSummary}</span>
                    </div>

                    <label className="text-area-field">
                        <span>图片兼容正则</span>
                        <textarea
                            value={config.scanner.filePatterns.join('\n')}
                            onChange={handleFilePatternsChange}
                            spellCheck={false}
                        />
                    </label>

                    <div className="media-rule-header">
                        <h3>媒体类型规则</h3>
                        <button className="button secondary" type="button" onClick={handleAddMediaType}>
                            <AddIcon fontSize="small" />
                            新增类型
                        </button>
                    </div>
                    {mediaTypesError && <div className="error-banner">媒体类型配置无效: {mediaTypesError}</div>}

                    <div className="media-rule-list">
                        {config.scanner.mediaTypes.map((item, index) => (
                            <section key={`${item.type}-${index}`} className="media-rule-card">
                                <div className="media-rule-title">
                                    <label className="form-row compact">
                                        <span>类型</span>
                                        <input
                                            type="text"
                                            value={item.type}
                                            onChange={(event) => handleMediaTypeNameChange(index, event.target.value)}
                                        />
                                    </label>
                                    <button
                                        className="icon-button danger"
                                        type="button"
                                        onClick={() => handleRemoveMediaType(index)}
                                        aria-label={`删除 ${item.type || '媒体类型'}`}
                                    >
                                        <DeleteIcon fontSize="small" />
                                    </button>
                                </div>

                                <label className="text-area-field">
                                    <span>扩展名</span>
                                    <textarea
                                        value={item.extensions.join(', ')}
                                        onChange={(event) => handleMediaTypeExtensionsChange(index, event.target.value)}
                                        spellCheck={false}
                                    />
                                </label>

                                <label className="text-area-field">
                                    <span>分类正则</span>
                                    <textarea
                                        value={joinLines(item.filePatterns)}
                                        onChange={(event) => handleMediaTypePatternsChange(index, event.target.value)}
                                        spellCheck={false}
                                    />
                                </label>
                            </section>
                        ))}
                    </div>

                    <label className="text-area-field">
                        <span>系列聚合规则</span>
                        <textarea
                            value={JSON.stringify(config.scanner.seriesGroupPatterns, null, 2)}
                            readOnly
                            spellCheck={false}
                        />
                    </label>
                </section>
            </form>
        </div>
    );
};

export default AdminPage;
