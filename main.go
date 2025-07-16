package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/muesli/termenv"
)

var (
	profile = termenv.ColorProfile()
	red     = termenv.String("").Foreground(profile.Color("#FF005F"))
	green   = termenv.String("").Foreground(profile.Color("#00FF00"))
	yellow  = termenv.String("").Foreground(profile.Color("#FFFF00"))
)

var (
	ErrNotInGitRepo           = errors.New("not in a git repository")
	ErrWorktreeCreationFailed = errors.New("failed to create git worktree")
)

type Config struct {
	verbose bool
	logger  *log.Logger
}

type WorktreeManager struct {
	repo   *GitRepo
	config *Config
}

func main() {
	var verbose bool
	flag.BoolVar(&verbose, "v", false, "verbose output")
	flag.BoolVar(&verbose, "verbose", false, "verbose output")
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(1)
	}

	config := &Config{
		verbose: verbose,
		logger:  log.New(os.Stderr, "", 0),
	}

	ctx := context.Background()
	manager := &WorktreeManager{config: config}

	if err := manager.CreateWorktree(ctx, args[0]); err != nil {
		die(err.Error())
	}
}

func usage() {
	fmt.Print(`worktree [-v] <branch name>

create a git worktree with <branch name>. Will create a worktree if one isn't
found that matches the given name.

Will copy over some untracked files to the new worktree. By default, this includes
.env, .envrc, .env.local, .tool-versions, and mise.toml files.

To customize the list of untracked files to copy for a particular repository:
    git config --add worktree.untrackedfiles ".env"
    git config --add worktree.untrackedfiles "mise.toml"

To set a global configuration for all repositories:
    git config --global --add worktree.untrackedfiles ".env"
    git config --global --add worktree.untrackedfiles "mise.toml"

If you have any custom configuration set, it will override the defaults
completely, so add all files you want copied.
`)
}

func die(msg string) {
	fmt.Printf("%s\n", red.Styled(msg))
	os.Exit(1)
}

func warn(msg string) {
	fmt.Printf("%s\n", yellow.Styled(msg))
}

func (wm *WorktreeManager) CreateWorktree(ctx context.Context, branchname string) error {
	repo, err := wm.initGitRepo()
	if err != nil {
		return err
	}
	wm.repo = repo

	dirname := strings.ReplaceAll(branchname, "/", "_")
	worktreePath := filepath.Join("..", dirname)

	if err := repo.pull(ctx); err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "no upstream") {
			// Silent for no upstream - this is common and expected
		} else if wm.config.verbose {
			warn(fmt.Sprintf("Unable to pull: %v", err))
		}
	}

	if err := repo.createWorktree(ctx, branchname, worktreePath); err != nil {
		return fmt.Errorf("%w: %s", ErrWorktreeCreationFailed, err)
	}

	fileCopier := &FileCopier{config: wm.config}

	if err := fileCopier.copyNodeModulesAsync(worktreePath); err != nil {
		wm.config.logger.Printf("Error copying node_modules: %v", err)
	}

	if err := fileCopier.copyUntrackedFiles(worktreePath); err != nil {
		warn(fmt.Sprintf("Error copying untracked files: %v", err))
	}

	if err := wm.setupDirenv(worktreePath); err != nil {
		wm.config.logger.Printf("Error setting up direnv: %v", err)
	}

	if err := os.Chdir(worktreePath); err != nil {
		return fmt.Errorf("failed to change to worktree directory: %w", err)
	}

	fmt.Printf("%s\n", green.Styled("created worktree "+worktreePath))
	return nil
}

func (wm *WorktreeManager) setupDirenv(worktreePath string) error {
	envrcPath := filepath.Join(worktreePath, ".envrc")
	if _, err := os.Stat(envrcPath); err == nil {
		cmd := exec.Command("direnv", "allow", worktreePath)
		return cmd.Run()
	}
	return nil
}
