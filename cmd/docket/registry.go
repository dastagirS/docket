package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf8"
)

const registryName = "registry.json"
const registryLockName = "init.lock"
const registryBytesMax = 1024 * 1024
const registryBindingsMax = 1024
const folderBytesMax = 4096
const journalBytesMax = 16 * 1024

type folderRegistry struct {
	Version  int             `json:"version"`
	Bindings []folderBinding `json:"bindings"`
	original []byte
}

func registryLocation(create bool) (string, error) {
	home, err := os.UserHomeDir()
	home = filepath.Clean(home)
	if err != nil || !validRegistryFolder(home) {
		return "", errors.New("An absolute HOME directory is required for ~/.docket")
	}
	home, err = filepath.EvalSymlinks(home)
	if err != nil {
		return "", errors.New("Cannot resolve HOME for the Docket registry")
	}
	directory := filepath.Join(home, markerName)
	if create {
		if err := os.Mkdir(directory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", errors.New("Cannot create private ~/.docket registry directory")
		}
	}
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) && !create {
		return filepath.Join(directory, registryName), nil
	}
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("~/.docket must be a private regular directory (mode 700), not a file or symlink; no existing configuration was overwritten")
	}
	return filepath.Join(directory, registryName), nil
}

func validRegistryFolder(folder string) bool {
	return len(folder) > 0 && len(folder) <= folderBytesMax && utf8.ValidString(folder) && !strings.ContainsRune(folder, 0) && filepath.IsAbs(folder) && filepath.Clean(folder) == folder
}

func readPrivateMetadata(path string, limit int64) ([]byte, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("Cannot read Docket metadata; symlinks are not allowed")
	}
	info, statError := file.Stat()
	if statError != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		if err := file.Close(); err != nil {
			return nil, errors.New("Cannot close invalid Docket metadata")
		}
		return nil, errors.New("Docket metadata must be a regular owner-only file (mode 600)")
	}
	payload, readError := io.ReadAll(io.LimitReader(file, limit+1))
	closeError := file.Close()
	if readError != nil || closeError != nil || int64(len(payload)) > limit || !utf8.Valid(payload) {
		return nil, errors.New("Docket metadata is unreadable, oversized, or invalid UTF-8")
	}
	return payload, nil
}

func readRegistry(path string) (folderRegistry, error) {
	registry := folderRegistry{Version: markerVersion, Bindings: []folderBinding{}}
	payload, err := readPrivateMetadata(path, registryBytesMax)
	if err != nil || payload == nil {
		return registry, err
	}
	registry = folderRegistry{}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&registry) != nil || decoder.Decode(new(any)) != io.EOF || registry.Version != markerVersion || registry.Bindings == nil || len(registry.Bindings) > registryBindingsMax {
		return folderRegistry{}, errors.New("Unsupported or invalid home registry; no board was guessed")
	}
	registry.original = payload
	folders := make(map[string]bool, len(registry.Bindings))
	for i := range registry.Bindings {
		binding := &registry.Bindings[i]
		if !validRegistryFolder(binding.Folder) || folders[binding.Folder] {
			return folderRegistry{}, errors.New("Registry contains an invalid or duplicate folder")
		}
		if err := validateBinding(*binding); err != nil {
			return folderRegistry{}, err
		}
		folders[binding.Folder] = true
		binding.path = path
	}
	return registry, nil
}

func pendingRegistryFolder(directory string) (string, error) {
	payload, err := readPrivateMetadata(filepath.Join(directory, registryLockName), journalBytesMax)
	if err != nil || payload == nil {
		return "", err
	}
	var pending struct {
		Folder string `json:"folder"`
	}
	if json.Unmarshal(payload, &pending) != nil || !validRegistryFolder(pending.Folder) {
		return "", errors.New("Registry initialization is running or incomplete; inspect ~/.docket/init.lock before using implicit scope")
	}
	return pending.Folder, nil
}

func sameProjectBinding(first, other folderBinding) bool {
	return first.Version == other.Version && first.ProjectURL == other.ProjectURL && first.ProjectKey == other.ProjectKey && first.ProjectCreatedAt == other.ProjectCreatedAt
}

func removeLegacyBinding(binding *folderBinding) error {
	if binding == nil || binding.legacyPath == "" {
		return nil
	}
	current, err := readBinding(binding.legacyPath)
	if err != nil {
		return err
	}
	if current == nil {
		return nil
	}
	if !bytes.Equal(current.original, binding.original) {
		return errors.New("Legacy marker changed during migration; registry retained and marker was not removed")
	}
	if err := os.Remove(binding.legacyPath); err != nil {
		return errors.New("Registry saved, but legacy marker could not be removed")
	}
	return syncFolder(filepath.Dir(binding.legacyPath))
}
