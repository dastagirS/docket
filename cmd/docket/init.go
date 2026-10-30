package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
	"unsafe"

	"docket/internal/docket"
)

const promptBytesMax = 4096
const promptTimeout = 2 * time.Minute
const projectKeyLengthMin = 2
const projectKeyLengthMax = 10

type projectCounts struct {
	Total    int64 `json:"total"`
	Active   int64 `json:"active"`
	Archived int64 `json:"archived"`
}
type initJournal struct {
	Stage         string `json:"stage"`
	PreviousURL   string `json:"previous_project_url,omitempty"`
	CreatedURL    string `json:"created_project_url,omitempty"`
	RequestedName string `json:"requested_name"`
	RequestedKey  string `json:"requested_key"`
}

func promptInit(reader *bufio.Reader, output io.Writer, question string) (string, error) {
	if _, err := io.WriteString(output, question); err != nil {
		return "", errors.New("Cannot write initialization prompt")
	}
	type answer struct {
		text string
		err  error
	}
	completed := make(chan answer, 1)
	go func() { line, err := reader.ReadSlice('\n'); completed <- answer{string(line), err} }()
	timer := time.NewTimer(promptTimeout)
	defer timer.Stop()
	select {
	case result := <-completed:
		if result.err != nil || !utf8.ValidString(result.text) {
			return "", errors.New("Initialization input closed, exceeded its line bound, or was invalid; no requested choice was accepted")
		}
		return strings.TrimSpace(result.text), nil
	case <-timer.C:
		return "", errors.New("Initialization prompt timed out; no choice was accepted")
	}
}

// /dev/null is also a character device; termios avoids prompts corrupting noninteractive JSON diagnostics.
func terminalInput(input io.Reader) (bool, error) {
	file, ok := input.(*os.File)
	if !ok {
		return false, nil
	}
	connection, err := file.SyscallConn()
	if err != nil {
		return false, errors.New("Cannot inspect initialization input")
	}
	var status syscall.Errno
	if err := connection.Control(func(descriptor uintptr) {
		var settings syscall.Termios
		_, _, status = syscall.Syscall(syscall.SYS_IOCTL, descriptor, syscall.TCGETS, uintptr(unsafe.Pointer(&settings)))
	}); err != nil {
		return false, errors.New("Cannot inspect initialization terminal")
	}
	if status == syscall.ENOTTY {
		return false, nil
	}
	if status != 0 {
		return false, errors.New("Cannot inspect initialization terminal")
	}
	return true, nil
}

func defaultProjectKey(name string) string {
	var key strings.Builder
	for _, character := range strings.ToUpper(name) {
		if (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') {
			key.WriteRune(character)
		}
		if key.Len() == projectKeyLengthMax {
			break
		}
	}
	if key.Len() == 0 {
		return "PROJECT"
	}
	if key.Len() == 1 {
		return key.String() + "P"
	}
	return key.String()
}

func initializeProject(arguments []string, input io.Reader, prompts io.Writer) (payload []byte, result error) {
	command := apiCommand{}
	flags := flag.NewFlagSet("docket project init", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	registerClientFlags(flags, &command)
	var name, key, choice string
	var forceInteractive bool
	flags.StringVar(&name, "name", "", "New project name; defaults to folder name")
	flags.StringVar(&key, "key", "", "New unique project key; defaults to letters/digits from folder name")
	flags.StringVar(&choice, "on-existing", "", "Explicit repeat-init choice: keep, create, or archive (archive and replace)")
	flags.BoolVar(&forceInteractive, "interactive", false, "Prompt even when stdin is piped; use explicit choices in agent workflows")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			var help strings.Builder
			help.WriteString("Usage: docket project init [options]\n\n")
			flags.SetOutput(&help)
			flags.PrintDefaults()
			return []byte(help.String()), nil
		}
		return nil, errors.New("Invalid init flags; use docket project init --help")
	}
	if flags.NArg() != 0 {
		return nil, errors.New("Unexpected init arguments")
	}
	if flags.Lookup("json").Value.String() != "true" {
		return nil, errors.New("Only JSON results are supported")
	}
	seen := map[string]bool{}
	flags.Visit(func(value *flag.Flag) { seen[value.Name] = true })
	if choice != "" && choice != "keep" && choice != "create" && choice != "archive" {
		return nil, errors.New("--on-existing must be keep, create, or archive")
	}
	if command.timeout <= 0 || command.timeout > requestTimeoutMax {
		return nil, errors.New("Timeout must be positive and at most 1m")
	}
	binding, root, err := discoverBinding()
	if err != nil {
		return nil, err
	}
	interactive := forceInteractive
	if !interactive {
		interactive, err = terminalInput(input)
		if err != nil {
			return nil, err
		}
	}
	reader := bufio.NewReaderSize(input, promptBytesMax)
	var previous docket.Project
	var counts projectCounts
	if binding != nil {
		origin, identifier, err := binding.reference()
		if err != nil {
			return nil, err
		}
		if seen["server"] {
			selected, err := serverOrigin(command.server)
			if err != nil || selected != origin {
				return nil, errors.New("Existing .docket pins a different server; no binding was replaced")
			}
		}
		command.server = origin
		previous, err = command.checkedProject(*binding)
		if err != nil {
			return nil, err
		}
		lookup := command
		lookup.method = http.MethodGet
		lookup.path = "/api/projects/" + strconv.FormatInt(identifier, 10) + "/stats"
		data, err := lookup.requestOnce()
		if err != nil {
			return nil, err
		}
		var response struct {
			Total    *int64 `json:"total"`
			Active   *int64 `json:"active"`
			Archived *int64 `json:"archived"`
		}
		if json.Unmarshal(data, &response) != nil || response.Total == nil || response.Active == nil || response.Archived == nil {
			return nil, &cliFailure{Exit: exitFailure, Code: "invalid_response", Message: "Missing project ticket counts"}
		}
		counts = projectCounts{*response.Total, *response.Active, *response.Archived}
		if counts.Total < 0 || counts.Total > docket.TicketsMax || counts.Active < 0 || counts.Active > docket.TicketsMax || counts.Archived < 0 || counts.Archived > docket.TicketsMax || counts.Active+counts.Archived != counts.Total {
			return nil, &cliFailure{Exit: exitFailure, Code: "invalid_response", Message: "Invalid project ticket counts"}
		}
		if choice == "" {
			if !interactive {
				return nil, fmt.Errorf("Project %q already has %d tickets (%d active, %d archived). Use --on-existing keep, create, or archive; no changes made", previous.Name, counts.Total, counts.Active, counts.Archived)
			}
			question := fmt.Sprintf("Project %q has %d tickets (%d active, %d archived). Binding: %q\n1) Keep existing [default]\n2) Create another (leave old project intact)\n3) Archive old project and create a replacement\nChoice [1]: ", previous.Name, counts.Total, counts.Active, counts.Archived, binding.path)
			answer, err := promptInit(reader, prompts, question)
			if err != nil {
				return nil, err
			}
			switch answer {
			case "", "1", "keep":
				choice = "keep"
			case "2", "create":
				choice = "create"
			case "3", "archive", "delete":
				choice = "archive"
			default:
				return nil, errors.New("Unknown initialization choice; no changes made")
			}
		}
		if choice == "keep" {
			return json.Marshal(struct {
				Action  string         `json:"action"`
				Project docket.Project `json:"project"`
				Marker  string         `json:"marker"`
				Tickets projectCounts  `json:"tickets"`
			}{"kept", previous, binding.path, counts})
		}
	} else if choice == "keep" || choice == "archive" {
		return nil, errors.New("No project is initialized in this folder; omit --on-existing to create one")
	}
	origin, err := serverOrigin(command.server)
	if err != nil {
		return nil, err
	}
	command.server = origin
	if name == "" {
		name = filepath.Base(root)
	}
	if interactive && !seen["name"] {
		answer, err := promptInit(reader, prompts, fmt.Sprintf("New project name [%q]: ", name))
		if err != nil {
			return nil, err
		}
		if answer != "" {
			name = answer
		}
	}
	name = strings.TrimSpace(name)
	if key == "" {
		key = defaultProjectKey(name)
	}
	if interactive && !seen["key"] {
		answer, err := promptInit(reader, prompts, fmt.Sprintf("New unique project key [%q]: ", key))
		if err != nil {
			return nil, err
		}
		if answer != "" {
			key = answer
		}
	}
	key = strings.ToUpper(strings.TrimSpace(key))
	if !utf8.ValidString(name) || len(name) == 0 || len(name) > docket.NameBytesMax || strings.ContainsRune(name, 0) || len(key) < projectKeyLengthMin || len(key) > projectKeyLengthMax {
		return nil, errors.New("Name must fit 100 UTF-8 bytes and key must have 2–10 ASCII letters/digits")
	}
	for _, character := range key {
		if !(character >= 'A' && character <= 'Z') && !(character >= '0' && character <= '9') {
			return nil, errors.New("Project key must contain only ASCII letters/digits")
		}
	}
	markerPath := filepath.Join(root, markerName)
	lockPath := filepath.Join(root, initLockName)
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return nil, errors.New("Initialization lock already exists or folder is not writable; no project was created")
	}
	preserveLock := false
	remoteChanged := false
	defer func() {
		if err := lock.Close(); err != nil {
			result = errors.Join(result, errors.New("Cannot close initialization journal"))
		}
		if !preserveLock {
			if err := os.Remove(lockPath); err != nil {
				result = errors.Join(result, errors.New("Cannot remove initialization lock; inspect .docket.lock"))
			}
		}
		if result != nil && (preserveLock || remoteChanged) {
			var failure *cliFailure
			if !errors.As(result, &failure) || !failure.OutcomeUnknown {
				result = &cliFailure{Exit: exitUncertain, Code: "init_incomplete", Message: "Initialization changed or may have changed server state; inspect .docket and .docket.lock before retrying", OutcomeUnknown: true}
			}
		}
	}()
	// Recheck after exclusive reservation: another init might have completed while the user answered.
	current, err := readBinding(markerPath)
	if err != nil {
		return nil, err
	}
	if (binding == nil && current != nil) || (binding != nil && (current == nil || !bytes.Equal(binding.original, current.original))) {
		return nil, errors.New("Folder binding changed during initialization; no project was created")
	}
	temporary, err := os.CreateTemp(root, ".docket-write-*")
	if err != nil {
		return nil, errors.New("Cannot stage a folder marker; no project was created")
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, errors.New("Cannot remove staged marker"))
		}
	}()
	if err := temporary.Close(); err != nil {
		return nil, errors.New("Cannot close staged marker; no project was created")
	}
	journal := initJournal{Stage: "creating", RequestedName: name, RequestedKey: key}
	if binding != nil {
		journal.PreviousURL = binding.ProjectURL
	}
	if err := saveInitJournal(lock, journal); err != nil {
		return nil, err
	}
	if err := syncFolder(root); err != nil {
		return nil, errors.New("Cannot persist initialization reservation; no project was created")
	}
	command.method = http.MethodPost
	command.path = "/api/projects"
	command.body, err = json.Marshal(map[string]string{"name": name, "key": key})
	if err != nil {
		return nil, errors.New("Cannot encode new project")
	}
	data, err := command.requestOnce()
	if err != nil {
		var failure *cliFailure
		if errors.As(err, &failure) && failure.OutcomeUnknown {
			preserveLock = true
		}
		return nil, err
	}
	preserveLock = true
	remoteChanged = true
	var created docket.Project
	if json.Unmarshal(data, &created) != nil || created.ID <= 0 || created.Version <= 0 || created.Key != key || created.Name != name || created.CreatedAt == "" || created.Archived {
		return nil, &cliFailure{Exit: exitUncertain, Code: "init_incomplete", Message: "Project creation response was unexpected; inspect .docket.lock before retrying", OutcomeUnknown: true}
	}
	if _, err := time.Parse(time.RFC3339Nano, created.CreatedAt); err != nil {
		return nil, &cliFailure{Exit: exitUncertain, Code: "init_incomplete", Message: "Unexpected project identity timestamp; inspect .docket.lock before retrying", OutcomeUnknown: true}
	}
	journal.Stage = "created"
	journal.CreatedURL = origin + "/projects/" + strconv.FormatInt(created.ID, 10)
	if err := saveInitJournal(lock, journal); err != nil {
		return nil, err
	}
	action := "created"
	if binding != nil {
		action = "created_another"
	}
	if binding != nil && choice == "archive" {
		action = "archived_and_replaced"
		journal.Stage = "archiving_previous"
		if err := saveInitJournal(lock, journal); err != nil {
			return nil, err
		}
		archive := apiCommand{server: origin, method: http.MethodPatch, path: "/api/projects/" + strconv.FormatInt(previous.ID, 10), body: []byte(`{"archived":true}`), version: previous.Version, actor: command.actor, timeout: command.timeout}
		data, err := archive.requestOnce()
		var archived docket.Project
		if err != nil || json.Unmarshal(data, &archived) != nil || archived.ID != previous.ID || !archived.Archived || archived.Version <= previous.Version || archived.Key != previous.Key || archived.CreatedAt != previous.CreatedAt {
			return nil, &cliFailure{Exit: exitUncertain, Code: "init_incomplete", Message: "Replacement was created, but old-project archival could not be confirmed. Original marker retained; inspect .docket.lock and project history before retrying", OutcomeUnknown: true}
		}
	}
	journal.Stage = "binding"
	if err := saveInitJournal(lock, journal); err != nil {
		return nil, err
	}
	selected := folderBinding{Version: markerVersion, ProjectURL: journal.CreatedURL, ProjectKey: created.Key, ProjectCreatedAt: created.CreatedAt}
	encoded, err := json.MarshalIndent(selected, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := writeFolderMarker(temporaryPath, markerPath, append(encoded, '\n')); err != nil {
		return nil, &cliFailure{Exit: exitUncertain, Code: "init_incomplete", Message: "Project was created but folder binding could not be confirmed; inspect .docket.lock before retrying", OutcomeUnknown: true}
	}
	preserveLock = false
	return json.Marshal(struct {
		Action  string         `json:"action"`
		Project docket.Project `json:"project"`
		Marker  string         `json:"marker"`
	}{action, created, markerPath})
}

func saveInitJournal(file *os.File, journal initJournal) error {
	payload, err := json.MarshalIndent(journal, "", "  ")
	if err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return &cliFailure{Exit: exitUncertain, Code: "init_incomplete", Message: "Cannot update initialization journal; inspect .docket.lock", OutcomeUnknown: true}
	}
	if _, err := file.Seek(0, 0); err != nil {
		return err
	}
	if _, err := file.Write(append(payload, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func writeFolderMarker(temporaryPath, markerPath string, payload []byte) error {
	if len(payload) > markerBytesMax {
		return errors.New("Folder marker exceeds size limit")
	}
	file, err := os.OpenFile(temporaryPath, os.O_WRONLY|os.O_TRUNC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return err
	}
	_, writeError := file.Write(payload)
	syncError := file.Sync()
	closeError := file.Close()
	if writeError != nil || syncError != nil || closeError != nil {
		return errors.New("Cannot persist staged folder marker")
	}
	if err := os.Rename(temporaryPath, markerPath); err != nil {
		return err
	}
	return syncFolder(filepath.Dir(markerPath))
}

func syncFolder(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncError := directory.Sync()
	closeError := directory.Close()
	return errors.Join(syncError, closeError)
}
