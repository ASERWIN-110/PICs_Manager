// src/types/config.ts

// 精确匹配 config.yaml 结构的类型定义
export interface AppConfig {
    Server: {
        port: string;
        timeout: string;
    };
    Database: {
        uri: string;
        name: string;
    };
    Logger: {
        level: string;
        format: string;
        path: string;
    };
    Scanner: {
        ScanPath: string;
        StagingPath: string;
        FinalLibraryPath: string;
        BackupPath: string;
        QuarantinePath: string;
        CorruptionLogPath: string;
        DuplicatesDir: string;
        WorkerCount: number;
        BatchSize: number;
        FilePatterns: string[];
        SeriesGroupPatterns: {
            Name: string;
            Pattern: string;
        }[];
    };
}