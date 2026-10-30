package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"docket/skills"
)

const argumentsCountMax = 128
const argumentsBytesMax = 128 * 1024
const cliHelp = `Docket — ticket commands for humans and agents

Usage:
  docket --skill [--references [filename]]
  docket doctor [options]
  docket project init [options]
  docket project list|create [options]
  docket project get|update|archive|restore ID [options]
  docket ticket list|create [--project PROJECT] [options]
  docket ticket get|update|archive|restore|history ID [--project PROJECT] [options]
  docket comment list|add TICKET [--project PROJECT] [options]
  docket events list [--project PROJECT] [options]
  docket serve [server options]

Client results are JSON on stdout; errors are JSON on stderr. Help/skill output is Markdown/text.
Flags follow the command and its positional ID. Use COMMAND --help for flags.
Writes to existing records require --version from a previous read.
Use --input FILE (or - for stdin) for JSON write bodies instead of field flags.
No service is auto-started and writes are never automatically retried.
Initialize with docket project init; ~/.docket/registry.json maps folders to project/server.
Explicit --project bypasses folder defaults. Otherwise a bound server stays pinned.
Unbound server: --server, then DOCKET_URL, then http://127.0.0.1:7331.
Legacy server flags (--listen, --database, --public-url, --backup-to) still work.
`

func execute(arguments []string, input io.Reader, output, diagnostics io.Writer) int {
	if len(arguments) > argumentsCountMax {
		return emitFailure(diagnostics, errors.New("Too many arguments"))
	}
	size := 0
	for _, argument := range arguments {
		size += len(argument)
	}
	if size > argumentsBytesMax {
		return emitFailure(diagnostics, errors.New("Arguments exceed 128 KiB; use --input for write bodies"))
	}
	if len(arguments) == 0 || arguments[0] == "help" || arguments[0] == "--help" || arguments[0] == "-h" {
		if _, err := io.WriteString(output, cliHelp); err != nil {
			return emitFailure(diagnostics, &cliFailure{Exit: exitFailure, Code: "output_error", Message: "Cannot write help to stdout"})
		}
		return 0
	}
	if arguments[0] == "--skill" {
		payload, err := skillOutput(arguments[1:], skills.Docket)
		if err != nil {
			return emitFailure(diagnostics, err)
		}
		if _, err := output.Write(payload); err != nil {
			return emitFailure(diagnostics, &cliFailure{Exit: exitFailure, Code: "output_error", Message: "Cannot write bundled skill to stdout"})
		}
		return 0
	}
	serverArguments := arguments
	isServer := arguments[0] == "serve"
	if isServer {
		serverArguments = arguments[1:]
	}
	switch strings.SplitN(arguments[0], "=", 2)[0] {
	case "--listen", "-listen", "--database", "-database", "--public-url", "-public-url", "--backup-to", "-backup-to":
		isServer = true
	}
	if isServer {
		if err := serve(serverArguments, diagnostics); err != nil {
			return emitFailure(diagnostics, &cliFailure{Exit: exitFailure, Code: "server_error", Message: err.Error()})
		}
		return 0
	}
	if len(arguments) >= 2 && arguments[0] == "project" && arguments[1] == "init" {
		payload, err := initializeProject(arguments[2:], input, diagnostics)
		if err != nil {
			return emitFailure(diagnostics, err)
		}
		if _, err := output.Write(append(payload, '\n')); err != nil {
			if !json.Valid(payload) {
				return emitFailure(diagnostics, &cliFailure{Exit: exitFailure, Code: "output_error", Message: "Cannot write initialization help"})
			}
			return emitFailure(diagnostics, &cliFailure{Exit: exitUncertain, Code: "outcome_unknown", Message: "Initialization result could not reach stdout; inspect ~/.docket/registry.json and ~/.docket/init.lock before retrying", OutcomeUnknown: true})
		}
		return 0
	}
	command, help, err := parseCommand(arguments, input)
	if err != nil {
		return emitFailure(diagnostics, err)
	}
	if help != "" {
		if _, err := io.WriteString(output, help); err != nil {
			return emitFailure(diagnostics, &cliFailure{Exit: exitFailure, Code: "output_error", Message: "Cannot write command help"})
		}
		return 0
	}
	payload, err := command.request()
	if err != nil {
		return emitFailure(diagnostics, err)
	}
	if _, err := output.Write(payload); err != nil {
		if command.method != http.MethodGet {
			return emitFailure(diagnostics, &cliFailure{Exit: exitUncertain, Code: "outcome_unknown", Message: "Write result could not reach stdout. Read the record before retrying.", OutcomeUnknown: true})
		}
		return emitFailure(diagnostics, &cliFailure{Exit: exitFailure, Code: "output_error", Message: "Cannot write result to stdout"})
	}
	return 0
}

func emitFailure(output io.Writer, err error) int {
	failure := &cliFailure{Exit: exitUsage, Code: "usage", Message: err.Error()}
	var known *cliFailure
	if errors.As(err, &known) {
		failure = known
	}
	if err := json.NewEncoder(output).Encode(struct {
		Error *cliFailure `json:"error"`
	}{failure}); err != nil {
		return exitFailure
	}
	return failure.Exit
}

func parseCommand(arguments []string, input io.Reader) (apiCommand, string, error) {
	command := apiCommand{method: http.MethodGet}
	noun := arguments[0]
	action := ""
	rest := arguments[1:]
	if noun != "doctor" {
		if len(rest) == 0 {
			return command, "", errors.New("A subcommand is required; use docket --help")
		}
		action = rest[0]
		rest = rest[1:]
	}
	needsID := false
	write := false
	list := false
	switch noun {
	case "doctor":
		command.path = "/healthz"
	case "project", "ticket":
		switch action {
		case "list":
			list = true
		case "create":
			write = true
			command.method = http.MethodPost
		case "get", "update", "archive", "restore":
			needsID = true
			if action != "get" {
				write = true
				command.method = http.MethodPatch
			}
		case "history":
			if noun != "ticket" {
				return command, "", errors.New("Only tickets have history")
			}
			needsID = true
			list = true
		default:
			return command, "", errors.New("Unknown subcommand; use docket --help")
		}
	case "comment":
		needsID = true
		if action == "list" {
			list = true
		} else if action == "add" {
			write = true
			command.method = http.MethodPost
		} else {
			return command, "", errors.New("Use comment list or comment add")
		}
	case "events":
		if action != "list" {
			return command, "", errors.New("Use events list")
		}
		list = true
	default:
		return command, "", errors.New("Unknown command; use docket --help")
	}
	var identifier int64
	if needsID && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
		var err error
		identifier, err = strconv.ParseInt(rest[0], 10, 64)
		if err != nil || identifier <= 0 {
			return command, "", errors.New("Record ID must be a positive integer")
		}
		rest = rest[1:]
	}
	flags := flag.NewFlagSet(noun+" "+action, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}
	registerClientFlags(flags, &command)
	var projectID, version, after, parentID int64
	var limit int
	var query, inputPath string
	var archived bool
	fields := map[string]*string{}
	if noun == "ticket" || noun == "comment" || noun == "events" {
		flags.Int64Var(&projectID, "project", 0, "Project ID; otherwise look up this folder in ~/.docket/registry.json")
	}
	if write && action != "create" {
		flags.Int64Var(&version, "version", 0, "Required current record version; never refreshed automatically")
	}
	if list {
		flags.Int64Var(&after, "after", 0, "Cursor from a previous page")
		flags.IntVar(&limit, "limit", pageSizeMax, "Page size, 1–100; one page per command")
		if noun == "ticket" && action == "list" {
			flags.StringVar(&query, "q", "", "Substring title search")
			flags.BoolVar(&archived, "archived", false, "Select archived tickets instead of active tickets")
			flags.Int64Var(&parentID, "parent", 0, "Filter direct subtickets of this parent")
		}
	}
	if write && action != "archive" && action != "restore" {
		flags.StringVar(&inputPath, "input", "", "JSON object file, or - for stdin; cannot combine with field flags")
		var names []string
		switch noun {
		case "project":
			names = []string{"name", "statuses"}
			if action == "create" {
				names = append(names, "key")
			}
		case "ticket":
			names = []string{"title", "description", "status", "assignee"}
			if action == "create" {
				flags.Int64Var(&parentID, "parent", 0, "Create a subticket of an existing top-level ticket")
			}
		case "comment":
			names = []string{"body"}
		}
		for _, name := range names {
			description := "Set " + name
			if name == "statuses" {
				description = "Ordered JSON string array; project updates require name and statuses together"
			}
			if name == "assignee" {
				description = "Set free-text assignee; an explicit empty value unassigns"
			}
			fields[name] = flags.String(name, "", description)
		}
	}
	if err := flags.Parse(rest); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			var help strings.Builder
			fmt.Fprintf(&help, "Usage: docket %s %s", noun, action)
			if needsID {
				help.WriteString(" ID")
			}
			help.WriteString(" [options]\n\n")
			flags.SetOutput(&help)
			flags.PrintDefaults()
			return command, help.String(), nil
		}
		return command, "", errors.New("Invalid flags; use the command's --help. Flags follow its positional ID")
	}
	if flags.NArg() != 0 {
		return command, "", errors.New("Unexpected positional arguments; use command --help")
	}
	seen := map[string]bool{}
	flags.Visit(func(value *flag.Flag) { seen[value.Name] = true })
	if value := flags.Lookup("json"); value.Value.String() != "true" {
		return command, "", errors.New("Only JSON output is supported")
	}
	if needsID && identifier <= 0 {
		return command, "", errors.New("A positive record ID is required before flags")
	}
	if noun == "ticket" || noun == "comment" || noun == "events" {
		if !seen["project"] {
			binding, _, err := discoverBinding()
			if err != nil {
				return command, "", err
			}
			if binding == nil {
				return command, "", errors.New("Supply --project or run docket project init in an authorized folder")
			}
			origin, identifier, err := binding.reference()
			if err != nil {
				return command, "", err
			}
			if seen["server"] {
				selected, err := serverOrigin(command.server)
				if err != nil || selected != origin {
					return command, "", errors.New("A different --server requires an explicit --project; folder binding was not retargeted")
				}
			}
			command.server = origin
			projectID = identifier
			command.binding = binding
		}
		if projectID <= 0 {
			return command, "", errors.New("Project ID must be positive")
		}
	}
	if (noun == "doctor" || noun == "project") && !seen["server"] {
		binding, _, err := discoverBinding()
		if err != nil {
			return command, "", err
		}
		if binding != nil {
			origin, _, err := binding.reference()
			if err != nil {
				return command, "", err
			}
			command.server = origin
		}
	}
	if write && action != "create" && version <= 0 {
		return command, "", errors.New("A positive --version from a previous read is required")
	}
	if seen["parent"] && parentID <= 0 {
		return command, "", errors.New("Parent ID must be positive")
	}
	if list && (after < 0 || limit < 1 || limit > pageSizeMax) {
		return command, "", errors.New("Cursor must be nonnegative and limit must be 1–100")
	}
	command.version = version
	if noun != "doctor" {
		command.path = "/api/projects"
		if noun == "project" {
			if needsID {
				command.path += "/" + strconv.FormatInt(identifier, 10)
			}
		} else {
			command.path += "/" + strconv.FormatInt(projectID, 10)
			if noun == "events" {
				command.path += "/events"
			} else {
				command.path += "/tickets"
				if needsID {
					command.path += "/" + strconv.FormatInt(identifier, 10)
				}
				if noun == "comment" {
					command.path += "/comments"
				}
				if action == "history" {
					command.path += "/history"
				}
			}
		}
	}
	if list {
		parameters := url.Values{"after": {strconv.FormatInt(after, 10)}, "limit": {strconv.Itoa(limit)}}
		if noun == "ticket" && action == "list" {
			parameters.Set("archived", strconv.FormatBool(archived))
			if query != "" {
				parameters.Set("q", query)
			}
			if seen["parent"] {
				parameters.Set("parent_id", strconv.FormatInt(parentID, 10))
			}
		}
		command.path += "?" + parameters.Encode()
	}
	if write {
		body := map[string]json.RawMessage{}
		for name, value := range fields {
			if !seen[name] {
				continue
			}
			if !utf8.ValidString(*value) {
				return command, "", errors.New("Field values must be valid UTF-8")
			}
			if name == "statuses" {
				var statuses []string
				if json.Unmarshal([]byte(*value), &statuses) != nil || statuses == nil {
					return command, "", errors.New("Statuses must be a JSON string array")
				}
				body[name] = json.RawMessage(*value)
			} else {
				encoded, err := json.Marshal(*value)
				if err != nil {
					return command, "", errors.New("Cannot encode field value")
				}
				body[name] = encoded
			}
		}
		if action == "create" && seen["parent"] {
			body["parent_id"] = json.RawMessage(strconv.FormatInt(parentID, 10))
		}
		if action == "archive" {
			body["archived"] = json.RawMessage("true")
		}
		if action == "restore" {
			body["archived"] = json.RawMessage("false")
		}
		if seen["input"] {
			if inputPath == "" || len(body) > 0 {
				return command, "", errors.New("Use either --input or field flags, not both")
			}
			payload, err := readCommandInput(inputPath, input, command.timeout)
			if err != nil {
				return command, "", err
			}
			if !utf8.Valid(payload) || json.Unmarshal(payload, &body) != nil || body == nil {
				return command, "", errors.New("Input must be one valid UTF-8 JSON object")
			}
		}
		if len(body) == 0 {
			return command, "", errors.New("Supply write fields or --input")
		}
		encoded, err := json.Marshal(body)
		if err != nil {
			return command, "", errors.New("Cannot encode JSON request")
		}
		if len(encoded) > requestBytesMax {
			return command, "", errors.New("JSON request exceeds 64 KiB")
		}
		command.body = encoded
	}
	return command, "", nil
}

func readCommandInput(path string, input io.Reader, timeout time.Duration) (payload []byte, result error) {
	if timeout <= 0 || timeout > requestTimeoutMax {
		return nil, errors.New("Timeout must be positive and at most 1m")
	}
	reader := input
	if path != "-" {
		// Nonblocking open lets us reject FIFOs instead of hanging before the regular-file check.
		file, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
		if err != nil {
			return nil, errors.New("Cannot open JSON input file")
		}
		defer func() {
			if err := file.Close(); err != nil {
				result = errors.Join(result, errors.New("Cannot close JSON input file"))
			}
		}()
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			return nil, errors.New("JSON input must be a regular file or - for stdin")
		}
		reader = file
	}
	type inputResult struct {
		payload []byte
		err     error
	}
	completed := make(chan inputResult, 1)
	// One bounded worker allows a timeout even for stdin descriptors without deadline support.
	// The buffer lets it finish after timeout; a CLI process runs only one command before exiting.
	go func() {
		data, err := io.ReadAll(io.LimitReader(reader, requestBytesMax+1))
		completed <- inputResult{data, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case read := <-completed:
		if read.err != nil {
			return nil, errors.New("Cannot read JSON input")
		}
		payload = read.payload
	case <-timer.C:
		return nil, &cliFailure{Exit: exitUsage, Code: "input_timeout", Message: "JSON input did not finish within --timeout; close stdin after sending the body. No request was sent."}
	}
	if len(payload) > requestBytesMax {
		return nil, errors.New("JSON input exceeds 64 KiB")
	}
	return payload, nil
}

func registerClientFlags(flags *flag.FlagSet, command *apiCommand) {
	server := os.Getenv("DOCKET_URL")
	if server == "" {
		server = serverDefault
	}
	flags.StringVar(&command.server, "server", server, "Docket HTTP origin (DOCKET_URL)")
	flags.DurationVar(&command.timeout, "timeout", requestTimeoutDefault, "HTTP timeout (positive, at most 1m)")
	flags.StringVar(&command.actor, "actor", os.Getenv("DOCKET_ACTOR"), "Attribution label, not authentication (DOCKET_ACTOR)")
	// Help must not echo environment values, which may accidentally contain credentials.
	flags.Lookup("server").DefValue = serverDefault
	flags.Lookup("actor").DefValue = ""
	flags.Bool("json", true, "JSON output (always enabled)")
}
