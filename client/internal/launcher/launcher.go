package launcher

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

func ResolveConfigPath(explicitPath string, defaultCandidates []string) (string, error) {
	explicitPath = strings.TrimSpace(explicitPath)
	if explicitPath != "" {
		for _, candidate := range explicitConfigCandidates(explicitPath) {
			if fileExists(candidate) {
				return candidate, nil
			}
		}
		return "", fmt.Errorf("config file not found: %s", explicitPath)
	}

	searchDirs := defaultSearchDirs()
	triedPaths := make([]string, 0, len(searchDirs)*len(defaultCandidates))

	for _, dir := range searchDirs {
		for _, name := range defaultCandidates {
			fullPath := filepath.Join(dir, name)
			triedPaths = append(triedPaths, fullPath)
			if fileExists(fullPath) {
				return fullPath, nil
			}
		}
	}

	return "", fmt.Errorf("config file not found, tried: %s", strings.Join(triedPaths, ", "))
}

func SetupLogging(defaultFileName string) (func(), string, error) {
	exeDir, exeErr := executableDir()
	cacheRoot, cacheErr := os.UserCacheDir()
	fallbackDir := ""
	if cacheErr == nil {
		fallbackDir = filepath.Join(cacheRoot, "1cat-tunnel", "logs")
	}

	cleanup, path, err := setupLoggingWithFallback(defaultFileName, exeDir, fallbackDir)
	if err == nil {
		return cleanup, path, nil
	}
	return func() {}, "", errors.Join(exeErr, cacheErr, err)
}

func setupLoggingWithFallback(defaultFileName, primaryDir, fallbackDir string) (func(), string, error) {
	defaultFileName = strings.TrimSpace(defaultFileName)
	if defaultFileName == "" || filepath.Base(defaultFileName) != defaultFileName || defaultFileName == "." {
		return func() {}, "", errors.New("log file name must be a plain file name")
	}

	var primaryErr error
	if strings.TrimSpace(primaryDir) != "" {
		cleanup, path, err := openLogFile(primaryDir, defaultFileName, false)
		if err == nil {
			return cleanup, path, nil
		}
		primaryErr = fmt.Errorf("open log beside executable: %w", err)
	}

	if strings.TrimSpace(fallbackDir) == "" || samePath(primaryDir, fallbackDir) {
		return func() {}, "", errors.Join(primaryErr, errors.New("writable fallback log directory is unavailable"))
	}
	cleanup, path, fallbackErr := openLogFile(fallbackDir, defaultFileName, true)
	if fallbackErr == nil {
		return cleanup, path, nil
	}
	return func() {}, "", errors.Join(primaryErr, fmt.Errorf("open fallback log: %w", fallbackErr))
}

func openLogFile(directory, defaultFileName string, createDirectory bool) (func(), string, error) {
	if createDirectory {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return func() {}, "", err
		}
		if err := os.Chmod(directory, 0o700); err != nil {
			return func() {}, "", err
		}
	}

	logPath := filepath.Join(directory, defaultFileName)
	file, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return func() {}, "", err
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return func() {}, "", err
	}

	log.SetOutput(io.MultiWriter(os.Stdout, file))
	cleanup := func() {
		_ = file.Close()
	}
	return cleanup, logPath, nil
}

func PauseBeforeExitIfWindows(enabled bool) {
	if !enabled || runtime.GOOS != "windows" {
		return
	}

	fmt.Println()
	fmt.Print("Press Enter to exit...")
	_, _ = fmt.Fscanln(os.Stdin)
}

func executableDir() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.Dir(exePath), nil
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func samePath(a, b string) bool {
	aa := filepath.Clean(a)
	bb := filepath.Clean(b)
	if runtime.GOOS == "windows" {
		return strings.EqualFold(aa, bb)
	}
	return aa == bb
}

func FatalfWithPause(pauseOnExit bool, format string, args ...any) {
	log.Printf(format, args...)
	PauseBeforeExitIfWindows(pauseOnExit)
	os.Exit(1)
}

func WarnIfLoggingSetupFails(err error) {
	if err == nil {
		return
	}
	if errors.Is(err, os.ErrPermission) {
		log.Printf("log file setup skipped: %v", err)
		return
	}
	log.Printf("log file setup skipped: %v", err)
}

func defaultSearchDirs() []string {
	dirs := make([]string, 0, 6)

	if cwd, err := os.Getwd(); err == nil && cwd != "" {
		dirs = appendUniquePath(dirs, cwd)
		dirs = appendUniquePath(dirs, filepath.Join(cwd, "examples"))
	}

	if exeDir, err := executableDir(); err == nil && exeDir != "" {
		dirs = appendUniquePath(dirs, exeDir)
		dirs = appendUniquePath(dirs, filepath.Join(exeDir, "examples"))

		parentDir := filepath.Dir(exeDir)
		if parentDir != "" && !samePath(parentDir, exeDir) {
			dirs = appendUniquePath(dirs, parentDir)
			dirs = appendUniquePath(dirs, filepath.Join(parentDir, "examples"))
		}
	}

	return dirs
}

func explicitConfigCandidates(explicitPath string) []string {
	if filepath.IsAbs(explicitPath) {
		return []string{explicitPath}
	}

	candidates := make([]string, 0, 4)
	candidates = append(candidates, explicitPath)

	if exeDir, err := executableDir(); err == nil && exeDir != "" {
		candidates = appendUniquePath(candidates, filepath.Join(exeDir, explicitPath))

		parentDir := filepath.Dir(exeDir)
		if parentDir != "" && !samePath(parentDir, exeDir) {
			candidates = appendUniquePath(candidates, filepath.Join(parentDir, explicitPath))
		}
	}

	return candidates
}

func appendUniquePath(paths []string, candidate string) []string {
	for _, path := range paths {
		if samePath(path, candidate) {
			return paths
		}
	}
	return append(paths, candidate)
}
