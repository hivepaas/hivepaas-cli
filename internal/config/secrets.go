package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/zalando/go-keyring"
	"gopkg.in/yaml.v3"
)

const keyringService = "hivepaas"

// ErrNoKeychain is a keychain that cannot be used - a Linux server without a
// secret service - when the secret was not allowed to go to a file instead.
var ErrNoKeychain = errors.New("the OS keychain cannot be used here; " +
	"log in with --insecure-storage to keep the secret in a file readable only by you")

// Secrets keeps the API keys' secrets: in the OS keychain, or - only when that
// cannot be used and the person said so - in credentials.yaml beside the
// config, mode 0600.
type Secrets struct {
	file string
}

func NewSecrets(dir string) *Secrets {
	return &Secrets{file: filepath.Join(dir, "credentials.yaml")}
}

func account(url, keyID string) string { return url + " " + keyID }

// Get is a key's secret, from the keychain or the file; "" when neither has it.
func (s *Secrets) Get(url, keyID string) (string, error) {
	secret, err := keyring.Get(keyringService, account(url, keyID))
	if err == nil {
		return secret, nil
	}
	stored, fileErr := s.readFile()
	if fileErr != nil {
		return "", fileErr
	}
	return stored[account(url, keyID)], nil
}

// Set keeps a key's secret in the keychain, or in the file when the keychain
// cannot be used and insecure allows it.
func (s *Secrets) Set(url, keyID, secret string, insecure bool) error {
	err := keyring.Set(keyringService, account(url, keyID), secret)
	if err == nil {
		return nil
	}
	if !insecure {
		return fmt.Errorf("%w (%w)", ErrNoKeychain, err)
	}
	stored, err := s.readFile()
	if err != nil {
		return err
	}
	stored[account(url, keyID)] = secret
	return s.writeFile(stored)
}

// Delete forgets a key's secret, wherever it was.
func (s *Secrets) Delete(url, keyID string) error {
	_ = keyring.Delete(keyringService, account(url, keyID))
	stored, err := s.readFile()
	if err != nil {
		return err
	}
	if _, found := stored[account(url, keyID)]; !found {
		return nil
	}
	delete(stored, account(url, keyID))
	return s.writeFile(stored)
}

func (s *Secrets) readFile() (map[string]string, error) {
	stored := map[string]string{}
	data, err := os.ReadFile(s.file)
	if errors.Is(err, fs.ErrNotExist) {
		return stored, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.file, err)
	}
	if err = yaml.Unmarshal(data, &stored); err != nil {
		return nil, fmt.Errorf("reading %s: %w", s.file, err)
	}
	return stored, nil
}

func (s *Secrets) writeFile(stored map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(s.file), dirMode); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(s.file), err)
	}
	data, err := yaml.Marshal(stored)
	if err != nil {
		return fmt.Errorf("writing %s: %w", s.file, err)
	}
	if err = os.WriteFile(s.file, data, fileMode); err != nil {
		return fmt.Errorf("writing %s: %w", s.file, err)
	}
	return nil
}
