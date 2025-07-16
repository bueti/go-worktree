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

func (fc *FileCopier) copyNodeModulesAsync(worktreePath string) error {
	if _, err := os.Stat("node_modules"); os.IsNotExist(err) {
		return nil
	}

	go func() {
		destPath := filepath.Join(worktreePath, "node_modules")
		warn("copying node_modules in the background")

		if err := fc.copyWithCOW("node_modules", destPath); err != nil {
			warn(fmt.Sprintf("Failed to copy node_modules: %v", err))
		}
	}()

	return nil
}

func (fc *FileCopier) copyUntrackedFiles(worktreePath string) error {
	pattern := fc.getUntrackedFilesPattern()
	files, err := fc.findFiles(pattern)
	if err != nil {
		return err
	}

	for _, file := range files {
		destPath := filepath.Join(worktreePath, file)
		if err := fc.copyWithCOW(file, destPath); err != nil {
			warn(fmt.Sprintf("Unable to copy file %s to %s - folder may not exist", file, destPath))
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
	cmd := exec.Command("fd", "-u", pattern, "-E", "node_modules")
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

		if strings.Contains(path, "node_modules") {
			return nil
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

	return fmt.Errorf("failed to copy %s to %s", src, dest)
}

func hasCommand(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

