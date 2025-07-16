package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/go-git/go-git/v5/plumbing/transport/ssh"
)

func (r *GitRepo) getAuth() (transport.AuthMethod, error) {
	remote, err := r.repository.Remote("origin")
	if err != nil {
		return nil, fmt.Errorf("failed to get origin remote: %w", err)
	}

	if len(remote.Config().URLs) == 0 {
		return nil, fmt.Errorf("no URLs configured for origin remote")
	}

	remoteURL := remote.Config().URLs[0]

	if strings.HasPrefix(remoteURL, "git@") || strings.HasPrefix(remoteURL, "ssh://") {
		return r.getSSHAuth()
	}

	// For HTTPS, try to get token from git credential helper or gh CLI
	if strings.HasPrefix(remoteURL, "https://github.com") {
		return r.getHTTPSAuth(remoteURL)
	}

	// No auth method found, return nil (will use default)
	return nil, nil
}

func (r *GitRepo) getSSHAuth() (transport.AuthMethod, error) {
	auth, err := ssh.NewSSHAgentAuth("git")
	if err == nil {
		return auth, nil
	}

	// Fallback to default SSH keys if agent fails
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	// Try common SSH key names
	keyNames := []string{"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa"}
	for _, keyName := range keyNames {
		sshKey := filepath.Join(homeDir, ".ssh", keyName)
		if _, err := os.Stat(sshKey); err == nil {
			auth, err := ssh.NewPublicKeysFromFile("git", sshKey, "")
			if err == nil {
				return auth, nil
			}
		}
	}

	return nil, fmt.Errorf("no SSH keys found or SSH agent not available")
}

func (r *GitRepo) getHTTPSAuth(remoteURL string) (transport.AuthMethod, error) {
	// Try gh CLI first
	if token, err := r.getGitHubToken(); err == nil {
		return &http.BasicAuth{
			Username: "token",
			Password: token,
		}, nil
	}

	// Try git credential helper
	if token, err := r.getGitCredentials(remoteURL); err == nil {
		return &http.BasicAuth{
			Username: "token",
			Password: token,
		}, nil
	}

	return nil, fmt.Errorf("no HTTPS authentication method found")
}

func (r *GitRepo) getGitHubToken() (string, error) {
	cmd := exec.Command("gh", "auth", "token")
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func (r *GitRepo) getGitCredentials(url string) (string, error) {
	cmd := exec.Command("git", "credential", "fill")
	cmd.Stdin = strings.NewReader(fmt.Sprintf("url=%s\n", url))
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(output), "\n")
	for _, line := range lines {
		if strings.HasPrefix(line, "password=") {
			return strings.TrimPrefix(line, "password="), nil
		}
	}

	return "", fmt.Errorf("no password found in git credentials")
}

