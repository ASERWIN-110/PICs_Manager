package scanner

import (
	"PICs_Manager/config"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func defaultWorkerCount(workerCount int) int {
	if workerCount > 0 {
		return workerCount
	}
	cpus := runtime.NumCPU()
	if cpus < 1 {
		return 1
	}
	if cpus > 4 {
		return 4
	}
	return cpus
}

func ensureMaintenanceWindow(window string, now func() time.Time) error {
	window = strings.TrimSpace(window)
	if window == "" {
		return nil
	}
	parts := strings.Split(window, "-")
	if len(parts) != 2 {
		return fmt.Errorf("无效维护窗口 %q，格式应为 HH:MM-HH:MM", window)
	}
	start, err := time.Parse("15:04", strings.TrimSpace(parts[0]))
	if err != nil {
		return fmt.Errorf("无效维护窗口 %q: %w", window, err)
	}
	end, err := time.Parse("15:04", strings.TrimSpace(parts[1]))
	if err != nil {
		return fmt.Errorf("无效维护窗口 %q: %w", window, err)
	}

	current := now()
	minuteOfDay := current.Hour()*60 + current.Minute()
	startMinute := start.Hour()*60 + start.Minute()
	endMinute := end.Hour()*60 + end.Minute()
	inWindow := false
	if startMinute == endMinute {
		inWindow = true
	} else if startMinute < endMinute {
		inWindow = minuteOfDay >= startMinute && minuteOfDay < endMinute
	} else {
		inWindow = minuteOfDay >= startMinute || minuteOfDay < endMinute
	}
	if !inWindow {
		return fmt.Errorf("当前时间不在维护窗口 %s 内", window)
	}
	return nil
}

func unsupportedFiles(all, supported []string) []string {
	supportedSet := make(map[string]struct{}, len(supported))
	for _, path := range supported {
		supportedSet[path] = struct{}{}
	}
	result := make([]string, 0)
	for _, path := range all {
		if _, ok := supportedSet[path]; !ok {
			result = append(result, path)
		}
	}
	return result
}

func CountUnsupportedFiles(ctx context.Context, root string, scannerCfg config.ScannerConfig) (int, error) {
	if strings.TrimSpace(root) == "" {
		return 0, nil
	}
	mediaTypes, err := compileMediaTypes(scannerCfg)
	if err != nil {
		return 0, err
	}
	extensions := make(map[string]struct{})
	for _, mediaType := range mediaTypes {
		for ext := range mediaType.Extensions {
			extensions[ext] = struct{}{}
		}
	}

	count := 0
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			if path != root && isIgnoredFilesystemEntry(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if isIgnoredFilesystemEntry(d.Name()) {
			return nil
		}
		if _, ok := extensions[strings.ToLower(filepath.Ext(path))]; !ok {
			count++
		}
		return nil
	})
	if err != nil && os.IsNotExist(err) {
		return 0, nil
	}
	return count, err
}
