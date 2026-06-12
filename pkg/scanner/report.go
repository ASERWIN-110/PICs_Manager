package scanner

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type DirectoryHealthReport struct {
	RunID            string    `json:"runId,omitempty"`
	GeneratedAt      time.Time `json:"generatedAt"`
	LibraryPath      string    `json:"libraryPath"`
	QuarantinePath   string    `json:"quarantinePath"`
	Directories      int       `json:"directories"`
	Files            int       `json:"files"`
	SameNameFiles    int       `json:"sameNameFiles"`
	QuarantineFiles  int       `json:"quarantineFiles"`
	UnsupportedFiles int       `json:"unsupportedFiles"`
	MaxFilesInDir    int       `json:"maxFilesInDir"`
	MaxFilesDir      string    `json:"maxFilesDir,omitempty"`
	Warnings         []string  `json:"warnings,omitempty"`
}

func WriteDirectoryHealthReport(ctx context.Context, libraryPath, quarantinePath, backupPath string, maxFilesPerDir int, runID string, unsupportedFiles ...int) (DirectoryHealthReport, error) {
	report, err := AnalyzeDirectoryHealth(ctx, libraryPath, quarantinePath, maxFilesPerDir)
	if err != nil {
		return report, err
	}
	report.RunID = strings.TrimSpace(runID)
	report.GeneratedAt = time.Now()
	if len(unsupportedFiles) > 0 {
		report.UnsupportedFiles = unsupportedFiles[0]
	}

	reportsDir := filepath.Join(backupPath, "reports")
	if err := os.MkdirAll(reportsDir, 0755); err != nil {
		return report, err
	}
	name := report.RunID
	if name == "" {
		name = fmt.Sprintf("run-%d", report.GeneratedAt.UnixNano())
	}
	path := filepath.Join(reportsDir, safeReportName(name)+".health.json")
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return report, err
	}
	if err := os.WriteFile(path, append(data, '\n'), 0644); err != nil {
		return report, err
	}
	return report, nil
}

func AnalyzeDirectoryHealth(ctx context.Context, libraryPath, quarantinePath string, maxFilesPerDir int) (DirectoryHealthReport, error) {
	report := DirectoryHealthReport{
		LibraryPath:    libraryPath,
		QuarantinePath: quarantinePath,
	}
	if err := filepath.WalkDir(libraryPath, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			report.Directories++
			entries, err := os.ReadDir(path)
			if err != nil {
				return err
			}
			fileCount := 0
			for _, entry := range entries {
				if !entry.IsDir() {
					fileCount++
				}
			}
			if fileCount > report.MaxFilesInDir {
				report.MaxFilesInDir = fileCount
				report.MaxFilesDir = path
			}
			if maxFilesPerDir > 0 && fileCount > maxFilesPerDir {
				report.Warnings = append(report.Warnings, fmt.Sprintf("目录文件数超过阈值: %s files=%d threshold=%d", path, fileCount, maxFilesPerDir))
			}
			if d.Name() == sameNameDirName {
				count, err := countFiles(ctx, path)
				if err != nil {
					return err
				}
				report.SameNameFiles += count
			}
			return nil
		}
		report.Files++
		return nil
	}); err != nil {
		return report, err
	}

	if strings.TrimSpace(quarantinePath) != "" {
		count, err := countFiles(ctx, quarantinePath)
		if err != nil && !os.IsNotExist(err) {
			return report, err
		}
		report.QuarantineFiles = count
	}
	return report, nil
}

func countFiles(ctx context.Context, root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !d.IsDir() {
			count++
		}
		return nil
	})
	return count, err
}

func safeReportName(value string) string {
	replacer := strings.NewReplacer("/", "_", "\\", "_", ":", "_")
	value = strings.TrimSpace(value)
	if value == "" {
		return "run"
	}
	return replacer.Replace(value)
}
