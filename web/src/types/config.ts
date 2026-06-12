export interface MediaTypeConfig {
    type: string;
    extensions: string[];
    filePatterns: string[];
}

export interface AppConfig {
    server: {
        port: string;
        timeout: string;
        maintenanceToken?: string;
    };
    security: {
        enabled: boolean;
        storePath: string;
        defaultPairingTTL: string;
        defaultDeviceTTL: string;
        allowLocalAdmin: boolean;
        corsAllowedOrigins: string[];
        requireViewerForRead: boolean;
    };
    scheduler: {
        enabled: boolean;
        interval: string;
        mode: string;
        runOnStartup: boolean;
    };
    runRetention: {
        maxRuns: number;
        maxAgeDays: number;
    };
    database: {
        uri: string;
        name: string;
    };
    logger: {
        level: string;
        format: string;
        path: string;
    };
    scanner: {
        mode: string;
        scanPath: string;
        stagingPath: string;
        finalLibraryPath: string;
        backupPath: string;
        quarantinePath: string;
        corruptionLogPath: string;
        duplicatesDir: string;
        workerCount: number;
        batchSize: number;
        ioThrottleMs: number;
        maintenanceWindow: string;
        maxFilesPerDir: number;
        followSymlinks: boolean;
        filePatterns: string[];
        mediaTypes: MediaTypeConfig[];
        seriesGroupPatterns: Array<{
            name: string;
            pattern: string;
        }>;
    };
}
