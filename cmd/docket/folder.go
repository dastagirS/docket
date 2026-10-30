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
	"time"

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
	Folder           string `json:"folder,omitempty"`
	ProjectURL       string `json:"project_url"`
	ProjectKey       string `json:"project_key"`
	ProjectCreatedAt string `json:"project_created_at"`
	path             string
	original         []byte
	legacyPath       string
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

func validateBinding(binding folderBinding) error {
	if binding.Version != markerVersion || len(binding.ProjectKey) < projectKeyLengthMin || len(binding.ProjectKey) > projectKeyLengthMax {
		return errors.New("Unsupported or invalid Docket project binding")
	}
	for _, character := range binding.ProjectKey {
		if !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') {
			return errors.New("Invalid bound project key")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, binding.ProjectCreatedAt); err != nil {
		return errors.New("Invalid bound project identity timestamp")
	}
	_, _, err := binding.reference()
	return err
}

func readBinding(path string) (*folderBinding, error) {
	payload, err := readPrivateMetadata(path, markerBytesMax)
	if err != nil || payload == nil {
		return nil, err
	}
	var binding folderBinding
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&binding) != nil || decoder.Decode(new(any)) != io.EOF || binding.Folder != "" {
		return nil, errors.New("Unsupported or invalid legacy .docket marker; no project was guessed")
	}
	if err := validateBinding(binding); err != nil {
		return nil, err
	}
	binding.path = path
	binding.original = payload
	binding.legacyPath = path
	return &binding, nil
}

// Registry entries and legacy markers obey the same Git boundary; nested repos never inherit a parent's board.
func discoverBinding() (*folderBinding, string, error) {
	registryPath, err := registryLocation(false)
	if err != nil {
		return nil, "", err
	}
	registry, err := readRegistry(registryPath)
	if err != nil {
		return nil, "", err
	}
	pending, err := pendingRegistryFolder(filepath.Dir(registryPath))
	if err != nil {
		return nil, "", err
	}
	registered := make(map[string]folderBinding, len(registry.Bindings))
	for _, binding := range registry.Bindings {
		registered[binding.Folder] = binding
	}
	directory, err := os.Getwd()
	if err != nil {
		return nil, "", errors.New("Cannot determine the current folder")
	}
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil || !validRegistryFolder(directory) {
		return nil, "", errors.New("Cannot resolve a valid current folder")
	}
	initial := directory
	for i := 0; i < ancestorsMax; i++ {
		if pending == directory {
			return nil, "", errors.New("Folder initialization is running or incomplete; inspect ~/.docket/init.lock before using implicit scope")
		}
		if _, err := os.Lstat(filepath.Join(directory, initLockName)); err == nil {
			return nil, "", errors.New("Legacy initialization is incomplete; reconcile the local .docket.lock before migration")
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, "", errors.New("Cannot inspect legacy initialization state")
		}
		var legacy *folderBinding
		if filepath.Join(directory, markerName) != filepath.Dir(registryPath) {
			legacy, err = readBinding(filepath.Join(directory, markerName))
			if err != nil {
				return nil, "", err
			}
		}
		binding, exists := registered[directory]
		if exists {
			if legacy != nil {
				if !sameProjectBinding(binding, *legacy) {
					return nil, "", errors.New("Home registry and legacy marker disagree; reconcile the folder binding explicitly")
				}
				binding.legacyPath = legacy.legacyPath
				binding.original = legacy.original
			}
			return &binding, directory, nil
		}
		if legacy != nil {
			legacy.Folder = directory
			return legacy, directory, nil
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
