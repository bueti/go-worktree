package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
)

type GitRepo struct {
	root       string
	repository *git.Repository
	config     *Config
}

func (wm *WorktreeManager) initGitRepo() (*GitRepo, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("failed to get current directory: %w", err)
	}

	repo, err := git.PlainOpenWithOptions(cwd, &git.PlainOpenOptions{
		DetectDotGit: true,
	})
	if err != nil {
		return nil, ErrNotInGitRepo
	}

	workTree, err := repo.Worktree()
	if err != nil {
		return nil, fmt.Errorf("failed to get worktree: %w", err)
	}

	root := workTree.Filesystem.Root()
	if err := os.Chdir(root); err != nil {
		return nil, fmt.Errorf("failed to change to git root directory: %w", err)
	}

	return &GitRepo{
		root:       root,
		repository: repo,
		config:     wm.config,
	}, nil
}

func (r *GitRepo) pull(ctx context.Context) error {
	w, err := r.repository.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get worktree: %w", err)
	}

	auth, err := r.getAuth()
	if err != nil {
		return fmt.Errorf("failed to get authentication: %w", err)
	}

	err = w.PullContext(ctx, &git.PullOptions{
		RemoteName: "origin",
		Progress:   r.getProgressWriter(),
		Auth:       auth,
	})
	if err != nil && err != git.NoErrAlreadyUpToDate {
		// Check for specific error types and handle them appropriately
		errStr := err.Error()
		if strings.Contains(errStr, "no upstream") || strings.Contains(errStr, "no tracking information") {
			return fmt.Errorf("no upstream configured for current branch")
		}
		if strings.Contains(errStr, "authentication required") || strings.Contains(errStr, "Repository not found") {
			return fmt.Errorf("authentication failed or repository not accessible")
		}
		return fmt.Errorf("failed to pull: %w", err)
	}

	return nil
}

func (r *GitRepo) createWorktree(ctx context.Context, branchname, worktreePath string) error {
	var ref plumbing.ReferenceName
	var hash plumbing.Hash

	if r.branchExistsLocally(branchname) {
		ref = plumbing.NewBranchReferenceName(branchname)
		branchRef, err := r.repository.Reference(ref, true)
		if err != nil {
			return fmt.Errorf("failed to get local branch reference: %w", err)
		}
		hash = branchRef.Hash()
	} else if r.branchExistsOnRemote(branchname) {
		remoteRef := plumbing.NewRemoteReferenceName("origin", branchname)
		branchRef, err := r.repository.Reference(remoteRef, true)
		if err != nil {
			return fmt.Errorf("failed to get remote branch reference: %w", err)
		}
		hash = branchRef.Hash()
		// Create local branch from remote
		ref = plumbing.NewBranchReferenceName(branchname)
		localRef := plumbing.NewHashReference(ref, hash)
		if err := r.repository.Storer.SetReference(localRef); err != nil {
			return fmt.Errorf("failed to create local branch: %w", err)
		}
	} else {
		// Create new branch from HEAD
		head, err := r.repository.Head()
		if err != nil {
			return fmt.Errorf("failed to get HEAD: %w", err)
		}
		hash = head.Hash()
		ref = plumbing.NewBranchReferenceName(branchname)
		newRef := plumbing.NewHashReference(ref, hash)
		if err := r.repository.Storer.SetReference(newRef); err != nil {
			return fmt.Errorf("failed to create new branch: %w", err)
		}
	}

	_, err := r.repository.Worktree()
	if err != nil {
		return fmt.Errorf("failed to get main worktree: %w", err)
	}

	// Create worktree using git command as go-git worktree support is limited
	cmd := exec.CommandContext(ctx, "git", "worktree", "add", worktreePath, branchname)
	if r.config.verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func (r *GitRepo) createBranch(branchname string) error {
	head, err := r.repository.Head()
	if err != nil {
		return fmt.Errorf("failed to get HEAD: %w", err)
	}

	ref := plumbing.NewHashReference(plumbing.NewBranchReferenceName(branchname), head.Hash())
	return r.repository.Storer.SetReference(ref)
}

func (r *GitRepo) getProgressWriter() *os.File {
	if r.config.verbose {
		return os.Stdout
	}
	return nil
}

func (r *GitRepo) branchExistsLocally(branchname string) bool {
	branchRef := plumbing.NewBranchReferenceName(branchname)
	_, err := r.repository.Reference(branchRef, true)
	return err == nil
}

func (r *GitRepo) branchExistsOnRemote(branchname string) bool {
	remoteRef := plumbing.NewRemoteReferenceName("origin", branchname)
	_, err := r.repository.Reference(remoteRef, true)
	return err == nil
}

