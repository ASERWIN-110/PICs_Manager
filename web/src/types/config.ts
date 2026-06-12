export interface MediaTypeConfig {
    type: string;
    extensions: string[];
    filePatterns: string[];
}

export interface AppConfig {
    server: {
        port: string;
        timeout: string;
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
        filePatterns: string[];
        mediaTypes: MediaTypeConfig[];
        seriesGroupPatterns: Array<{
            name: string;
            pattern: string;
        }>;
    };
}
