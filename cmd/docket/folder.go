package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"docket/internal/docket"
)

const markerVersion = 1
const markerBytesMax = 8192
const serverURLBytesMax = 4096
const ancestorsMax = 64
const markerName = ".docket"
const initLockName = ".docket.lock"

type folderBinding struct {
	Version          int    `json:"version"`
	ProjectURL       string `json:"project_url"`
	ProjectKey       string `json:"project_key"`
	ProjectCreatedAt string `json:"project_created_at"`
	path             string
	original         []byte
}

func serverOrigin(value string) (string, error) {
	if len(value) > serverURLBytesMax {
		return "", errors.New("Server origin is too long")
	}
	location, err := url.Parse(value)
	if err != nil || location.Hostname() == "" || (location.Scheme != "http" && location.Scheme != "https") || location.User != nil || location.RawQuery != "" || location.ForceQuery || location.Fragment != "" || (location.Path != "" && location.Path != "/") || location.Opaque != "" {
		return "", errors.New("Server must be an http(s) origin without credentials, query, fragment, or subpath")
	}
	return location.Scheme + "://" + strings.ToLower(location.Host), nil
}

func (binding folderBinding) reference() (string, int64, error) {
	location, err := url.Parse(binding.ProjectURL)
	if err != nil {
		return "", 0, errors.New("Invalid .docket project URL")
	}
	parts := strings.Split(strings.TrimPrefix(location.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "projects" || location.RawQuery != "" || location.Fragment != "" || location.User != nil {
		return "", 0, errors.New("Invalid .docket project URL")
	}
	identifier, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || identifier <= 0 {
		return "", 0, errors.New("Invalid .docket project identifier")
	}
	location.Path = ""
	origin, err := serverOrigin(location.String())
	return origin, identifier, err
}

func readBinding(path string) (*folderBinding, error) {
	file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New("Cannot read .docket; markers must be regular files, not symlinks")
	}
	info, statError := file.Stat()
	if statError != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		if err := file.Close(); err != nil {
			return nil, errors.New("Cannot close invalid .docket marker")
		}
		return nil, errors.New(".docket must be a regular owner-only file (mode 600)")
	}
	payload, readError := io.ReadAll(io.LimitReader(file, markerBytesMax+1))
	closeError := file.Close()
	if readError != nil || closeError != nil || len(payload) > markerBytesMax || !utf8.Valid(payload) {
		return nil, errors.New(".docket is unreadable, invalid UTF-8, or exceeds 8 KiB")
	}
	var binding folderBinding
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&binding) != nil || decoder.Decode(new(any)) != io.EOF || binding.Version != markerVersion || binding.ProjectKey == "" || binding.ProjectCreatedAt == "" {
		return nil, errors.New("Unsupported or invalid .docket marker; no project was guessed")
	}
	if _, err := time.Parse(time.RFC3339Nano, binding.ProjectCreatedAt); err != nil {
		return nil, errors.New("Invalid .docket project identity timestamp")
	}
	if _, _, err := binding.reference(); err != nil {
		return nil, err
	}
	binding.path = path
	binding.original = payload
	return &binding, nil
}

// Discovery stops at the nearest Git boundary so a nested repository cannot inherit another repo's board.
func discoverBinding() (*folderBinding, string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return nil, "", errors.New("Cannot determine the current folder")
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, "", errors.New("Cannot resolve the current folder")
	}
	initial := directory
	for i := 0; i < ancestorsMax; i++ {
		if _, err := os.Lstat(filepath.Join(directory, initLockName)); err == nil {
			return nil, "", errors.New("Folder initialization is running or incomplete; inspect .docket.lock before retrying or using implicit scope")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, "", errors.New("Cannot inspect folder initialization state")
		}
		binding, err := readBinding(filepath.Join(directory, markerName))
		if err != nil {
			return nil, "", err
		}
		if binding != nil {
			return binding, directory, nil
		}
		if _, err := os.Lstat(filepath.Join(directory, ".git")); err == nil {
			return nil, initial, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, "", errors.New("Cannot inspect repository boundary")
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return nil, initial, nil
		}
		directory = parent
	}
	return nil, "", errors.New("Folder discovery exceeded 64 ancestors; use explicit --project and --server")
}

func (command apiCommand) checkedProject(binding folderBinding) (docket.Project, error) {
	origin, identifier, err := binding.reference()
	if err != nil {
		return docket.Project{}, err
	}
	lookup := apiCommand{server: origin, method: "GET", path: "/api/projects/" + strconv.FormatInt(identifier, 10), timeout: command.timeout, actor: command.actor}
	payload, err := lookup.requestOnce()
	if err != nil {
		return docket.Project{}, err
	}
	var project docket.Project
	if json.Unmarshal(payload, &project) != nil || project.ID != identifier || project.Version <= 0 || project.Key != binding.ProjectKey || project.CreatedAt != binding.ProjectCreatedAt {
		return docket.Project{}, errors.New("Bound project identity changed; no request was sent to the selected ticket. Reconcile .docket explicitly")
	}
	return project, nil
}
