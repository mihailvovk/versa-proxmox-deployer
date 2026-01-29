package ssh

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/crypto/ssh"
)

// KeyAuth creates an SSH auth method from a private key file
func KeyAuth(keyPath string, passphrase string) (ssh.AuthMethod, error) {
	// Expand ~ to home directory
	if strings.HasPrefix(keyPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("getting home directory: %w", err)
		}
		keyPath = filepath.Join(home, keyPath[2:])
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, fmt.Errorf("reading key file: %w", err)
	}

	var signer ssh.Signer
	if passphrase != "" {
		signer, err = ssh.ParsePrivateKeyWithPassphrase(keyData, []byte(passphrase))
	} else {
		signer, err = ssh.ParsePrivateKey(keyData)
	}

	if err != nil {
		// Check if it's a passphrase-protected key
		if _, ok := err.(*ssh.PassphraseMissingError); ok {
			return nil, fmt.Errorf("key requires passphrase")
		}
		return nil, fmt.Errorf("parsing key: %w", err)
	}

	return ssh.PublicKeys(signer), nil
}

// PasswordAuth creates an SSH auth method from a password
func PasswordAuth(password string) ssh.AuthMethod {
	return ssh.Password(password)
}

// KeyboardInteractiveAuth creates an SSH auth method for keyboard-interactive authentication
func KeyboardInteractiveAuth(password string) ssh.AuthMethod {
	return ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		for i := range questions {
			answers[i] = password
		}
		return answers, nil
	})
}

// FindDefaultKey looks for common SSH key locations
func FindDefaultKey() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// Common key locations in order of preference
	keyPaths := []string{
		filepath.Join(home, ".ssh", "id_ed25519"),
		filepath.Join(home, ".ssh", "id_rsa"),
		filepath.Join(home, ".ssh", "id_ecdsa"),
		filepath.Join(home, ".ssh", "id_dsa"),
	}

	for _, path := range keyPaths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// IsKeyEncrypted checks if an SSH private key is encrypted
func IsKeyEncrypted(keyPath string) (bool, error) {
	// Expand ~ to home directory
	if strings.HasPrefix(keyPath, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return false, fmt.Errorf("getting home directory: %w", err)
		}
		keyPath = filepath.Join(home, keyPath[2:])
	}

	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return false, fmt.Errorf("reading key file: %w", err)
	}

	_, err = ssh.ParsePrivateKey(keyData)
	if err != nil {
		if _, ok := err.(*ssh.PassphraseMissingError); ok {
			return true, nil
		}
		return false, fmt.Errorf("parsing key: %w", err)
	}

	return false, nil
}
