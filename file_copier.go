package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

type FileCopier struct {
	config *Config
}

func (fc *FileCopier) copyUntrackedFiles(worktreePath string) error {
	pattern := fc.getUntrackedFilesPattern()
	files, err := fc.findFiles(pattern)
	if err != nil {
		return err
	}

	for _, file := range files {
		destPath := filepath.Join(worktreePath, file)

		// Check if this is a directory that should be copied as a whole
		srcInfo, err := os.Stat(file)
		if err != nil {
			continue // Skip files that can't be accessed
		}

		if !srcInfo.IsDir() {
			// For individual files, use the existing approach
			if err := fc.copyWithCOW(file, destPath); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", yellow.Styled(fmt.Sprintf("Unable to copy file %s to %s - folder may not exist", file, destPath)))
			}
		} else {
			// For directories, copy the entire directory at once
			if err := fc.copyWithCOW(file, destPath); err != nil {
				fmt.Fprintf(os.Stderr, "%s\n", yellow.Styled(fmt.Sprintf("Unable to copy directory %s to %s", file, destPath)))
			}
		}
	}

	return nil
}

func (fc *FileCopier) getUntrackedFilesPattern() string {
	defaultPatterns := `\.env|\.envrc|\.env.local|\.mise.toml|\.tool-versions|mise.toml`

	cmd := exec.Command("git", "config", "--get-all", "worktree.untrackedfiles")
	output, err := cmd.Output()
	if err != nil {
		return fmt.Sprintf("^(%s)$", defaultPatterns)
	}

	customPatterns := strings.TrimSpace(string(output))
	if customPatterns != "" {
		patterns := strings.Split(customPatterns, "\n")
		joined := strings.Join(patterns, "|")
		return fmt.Sprintf("^(%s)$", joined)
	}

	return fmt.Sprintf("^(%s)$", defaultPatterns)
}

func (fc *FileCopier) findFiles(pattern string) ([]string, error) {
	if hasCommand("fd") {
		return fc.findFilesWithFd(pattern)
	}

	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	return fc.findFilesWithWalk(re)
}

func (fc *FileCopier) findFilesWithFd(pattern string) ([]string, error) {
	// Use fd to find files matching the pattern
	cmd := exec.Command("fd", "-u", pattern)
	if !fc.config.excludeNodeModules {
		// Add node_modules to exclusion list
		cmd = exec.Command("fd", "-u", pattern, "-E", "node_modules")
	}
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	files := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(files) == 1 && files[0] == "" {
		return []string{}, nil
	}
	return files, nil
}

func (fc *FileCopier) findFilesWithWalk(re *regexp.Regexp) ([]string, error) {
	var files []string

	err := filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		// Skip node_modules if configured to do so
		if fc.config.excludeNodeModules && strings.Contains(path, "node_modules") {
			return filepath.SkipDir
		}

		// Special handling for large directories like node_modules
		if info.IsDir() && strings.Contains(path, "node_modules") {
			// Instead of walking through all files, just add the directory itself
			// The copy function will handle copying the entire directory efficiently
			files = append(files, path)
			return filepath.SkipDir
		}

		if !info.IsDir() && re.MatchString(info.Name()) {
			files = append(files, path)
		}

		return nil
	})

	return files, err
}

func (fc *FileCopier) copyWithCOW(src, dest string) error {
	destDir := filepath.Dir(dest)
	if err := os.MkdirAll(destDir, 0755); err != nil {
		return err
	}

	// Check if this is a directory (like node_modules) that should be copied as a whole
	srcInfo, err := os.Stat(src)
	if err != nil {
		return err
	}

	if srcInfo.IsDir() {
		// For directories, try to copy the entire directory at once
		// This is much more efficient than copying individual files

		// First try rsync for optimal performance
		if hasCommand("rsync") {
			// Use rsync with hard links and archive mode for maximum efficiency
			cmd := exec.Command("rsync", "-aH", src+"/", dest)
			if err := cmd.Run(); err == nil {
				return nil
			}
		}

		// Fallback to cp with hard links
		cmd := exec.Command("cp", "-aH", src, dest)
		if err := cmd.Run(); err == nil {
			return nil
		}

		// Final fallback to existing strategies
		copyStrategies := [][]string{
			{"-Rc"},             // BSD/macOS copy-on-write
			{"-R", "--reflink"}, // GNU copy-on-write
			{"-R"},              // Regular copy
		}

		for _, strategy := range copyStrategies {
			args := append(strategy, src, dest)
			cmd := exec.Command("cp", args...)
			if err := cmd.Run(); err == nil {
				return nil
			}
		}
	} else {
		// For individual files, use the existing approach
		copyStrategies := [][]string{
			{"-c"},             // BSD/macOS copy-on-write
			{"--reflink"},     // GNU copy-on-write
			{},                // Regular copy (no flags)
		}

		for _, strategy := range copyStrategies {
			args := append(strategy, src, dest)
			cmd := exec.Command("cp", args...)
			if err := cmd.Run(); err == nil {
				return nil
			}
		}
	}

	return fmt.Errorf("failed to copy %s to %s", src, dest)
}

func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}
