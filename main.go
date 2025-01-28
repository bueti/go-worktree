package main

import (
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

// Config holds the application configuration
type Config struct {
	verbose bool
}

// CopyStrategy defines how files should be copied
type CopyStrategy interface {
	Copy(src, dst string) error
}

// RegularCopyStrategy implements basic file copying
type RegularCopyStrategy struct {
	logger *slog.Logger
}

func (s *RegularCopyStrategy) Copy(src, dst string) error {
	source, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("opening source file: %w", err)
	}
	defer source.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0755); err != nil {
		return fmt.Errorf("creating destination directory: %w", err)
	}

	destination, err := os.Create(dst)
	if err != nil {
		return fmt.Errorf("creating destination file: %w", err)
	}
	defer destination.Close()

	if _, err = io.Copy(destination, source); err != nil {
		return fmt.Errorf("copying file contents: %w", err)
	}

	s.logger.Debug("copied file using regular copy", "src", src, "dst", dst)
	return nil
}

// CoWCopyStrategy implements copy-on-write using system cp command
type CoWCopyStrategy struct {
	logger    *slog.Logger
	cpCommand string
	cpArgs    []string
}

func (s *CoWCopyStrategy) Copy(src, dst string) error {
	args := append(append([]string{}, s.cpArgs...), src, dst)
	if err := exec.Command(s.cpCommand, args...).Run(); err != nil {
		return fmt.Errorf("copy-on-write failed: %w", err)
	}
	s.logger.Debug("copied file using copy-on-write", "src", src, "dst", dst)
	return nil
}

// NewCopyStrategy creates the appropriate copy strategy for the current OS
func NewCopyStrategy(logger *slog.Logger) CopyStrategy {
	if runtime.GOOS == "darwin" || strings.Contains(runtime.GOOS, "bsd") {
		return &CoWCopyStrategy{
			logger:    logger,
			cpCommand: "/bin/cp",
			cpArgs:    []string{"-Rc"},
		}
	}

	if runtime.GOOS == "linux" {
		// Try to detect if --reflink is supported
		cmd := exec.Command("/bin/cp", "--help")
		output, err := cmd.Output()
		if err == nil && strings.Contains(string(output), "--reflink") {
			return &CoWCopyStrategy{
				logger:    logger,
				cpCommand: "/bin/cp",
				cpArgs:    []string{"-R", "--reflink=auto"},
			}
		}
	}

	// Fallback to regular copy for unsupported systems
	return &RegularCopyStrategy{logger: logger}
}

// WorktreeManager handles git worktree operations
type WorktreeManager struct {
	logger       *slog.Logger
	cfg          Config
	copyStrategy CopyStrategy
}

// NewWorktreeManager creates a new WorktreeManager with the given configuration
func NewWorktreeManager(cfg Config) *WorktreeManager {
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}
	if cfg.verbose {
		opts.Level = slog.LevelDebug
	}
	handler = slog.NewTextHandler(os.Stderr, opts)
	logger := slog.New(handler)

	return &WorktreeManager{
		logger:       logger,
		cfg:          cfg,
		copyStrategy: NewCopyStrategy(logger),
	}
}

// copyFile attempts to copy a file using the configured strategy
func (wm *WorktreeManager) copyFile(src, dst string) error {
	return wm.copyStrategy.Copy(src, dst)
}

// branchExists checks if a branch exists either locally or remotely
func (wm *WorktreeManager) branchExists(branchName string) (bool, error) {
	// Check local branches
	localOutput, err := exec.Command("git", "for-each-ref", "--format=%(refname:lstrip=2)", "refs/heads").Output()
	if err != nil {
		return false, fmt.Errorf("checking local branches: %w", err)
	}

	if wm.branchInList(branchName, localOutput) {
		return true, nil
	}

	// Check remote branches
	remoteOutput, err := exec.Command("git", "for-each-ref", "--format=%(refname:lstrip=3)", "refs/remotes/origin").Output()
	if err != nil {
		return false, fmt.Errorf("checking remote branches: %w", err)
	}

	return wm.branchInList(branchName, remoteOutput), nil
}

func (wm *WorktreeManager) branchInList(branchName string, output []byte) bool {
	branches := strings.Split(string(output), "\n")
	for _, branch := range branches {
		if strings.TrimSpace(branch) == branchName {
			return true
		}
	}
	return false
}

func (wm *WorktreeManager) isGitDirectory() bool {
	cmd := exec.Command("git", "rev-parse", "--git-dir")
	if err := cmd.Run(); err != nil {
		return false
	}
	return true
}

// CreateWorktree creates a new git worktree
func (wm *WorktreeManager) CreateWorktree(branchName string) error {
	// Check if current directory is a git repository
	if !wm.isGitDirectory() {
		return fmt.Errorf("current directory is not a git repository")
	}

	// Replace slashes with underscores
	dirName := strings.ReplaceAll(branchName, "/", "_")

	// Try to pull the most recent version
	if err := exec.Command("git", "pull").Run(); err != nil {
		wm.logger.Warn("unable to run git pull, there may not be an upstream")
	}

	// Check if branch exists
	exists, err := wm.branchExists(branchName)
	if err != nil {
		return fmt.Errorf("checking branch existence: %w", err)
	}

	parentDir := ".."
	newWorktreePath := filepath.Join(parentDir, dirName)

	// Create worktree
	var cmd *exec.Cmd
	if exists {
		cmd = exec.Command("git", "worktree", "add", newWorktreePath, branchName)
	} else {
		cmd = exec.Command("git", "worktree", "add", "-b", branchName, newWorktreePath)
	}

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("creating git worktree: %w", err)
	}

	if err := wm.copyWorktreeFiles(newWorktreePath); err != nil {
		return fmt.Errorf("copying worktree files: %w", err)
	}

	if err := wm.setupDirenv(newWorktreePath); err != nil {
		wm.logger.Warn("failed to setup direnv", "error", err)
	}

	if err := os.Chdir(newWorktreePath); err != nil {
		return fmt.Errorf("changing to new worktree directory: %w", err)
	}

	wm.logger.Info("created worktree successfully", "path", newWorktreePath)
	return nil
}

func (wm *WorktreeManager) copyWorktreeFiles(newWorktreePath string) error {
	// Copy node_modules if it exists
	if _, err := os.Stat("node_modules"); err == nil {
		if err := wm.copyFile("node_modules", filepath.Join(newWorktreePath, "node_modules")); err != nil {
			wm.logger.Warn("failed to copy node_modules", "error", err)
		}
	}

	// Copy configuration files
	pattern := regexp.MustCompile(`(?i)\.(?:envrc|env|env\.local|tool-versions|mise\.toml)$`)
	return filepath.Walk(".", func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		if strings.Contains(path, "node_modules") {
			return filepath.SkipDir
		}

		if !info.IsDir() && pattern.MatchString(info.Name()) {
			dst := filepath.Join(newWorktreePath, path)
			if err := wm.copyFile(path, dst); err != nil {
				wm.logger.Warn("failed to copy config file",
					"src", path,
					"dst", dst,
					"error", err)
			}
		}
		return nil
	})
}

func (wm *WorktreeManager) setupDirenv(worktreePath string) error {
	if _, err := os.Stat(filepath.Join(worktreePath, ".envrc")); err == nil {
		return exec.Command("direnv", "allow", worktreePath).Run()
	}
	return nil
}

func main() {
	cfg := Config{}
	flag.BoolVar(&cfg.verbose, "v", false, "Enable verbose output")
	flag.BoolVar(&cfg.verbose, "verbose", false, "Enable verbose output")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, `Usage: worktree [-v] <branch name>

Create a git worktree with <branch name>. Will create a worktree if one isn't
found that matches the given name.

Will copy over any .env, .envrc, or .tool-versions files to the new worktree

Options:
`)
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() < 1 {
		flag.Usage()
		os.Exit(1)
	}

	branchName := flag.Arg(0)
	if branchName == "help" || branchName == "-h" || branchName == "--help" {
		flag.Usage()
		os.Exit(0)
	}

	wm := NewWorktreeManager(cfg)
	if err := wm.CreateWorktree(branchName); err != nil {
		wm.logger.Error("failed to create worktree", "error", err, "branch", branchName)
		os.Exit(1)
	}
}
