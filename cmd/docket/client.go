package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	requestBytesMax        = 64 * 1024
	responseBytesMax       = 16 * 1024 * 1024
	responseHeaderBytesMax = 16 * 1024
	requestTimeoutDefault  = 10 * time.Second
	requestTimeoutMax      = time.Minute
	actorBytesMax          = 80
	retryAfterBytesMax     = 128
	pageSizeMax            = 100
	serverDefault          = "http://127.0.0.1:7331"
	exitFailure            = 1
	exitUsage              = 2
	exitNotFound           = 3
	exitConflict           = 4
	exitUnavailable        = 5
	exitUncertain          = 6
)

type cliFailure struct {
	Exit           int    `json:"-"`
	Code           string `json:"code"`
	Message        string `json:"message"`
	HTTPStatus     int    `json:"http_status,omitempty"`
	RetryAfter     string `json:"retry_after,omitempty"`
	OutcomeUnknown bool   `json:"outcome_unknown,omitempty"`
}

func (failure *cliFailure) Error() string { return failure.Message }

type apiCommand struct {
	server  string
	method  string
	path    string
	body    []byte
	version int64
	actor   string
	timeout time.Duration
	binding *folderBinding
}

func (command apiCommand) request() ([]byte, error) {
	if command.binding != nil {
		project, err := command.checkedProject(*command.binding)
		if err != nil {
			return nil, err
		}
		if project.Archived && command.method != http.MethodGet {
			return nil, errors.New("Bound project is archived; restore it explicitly or use project init to select a new project")
		}
	}
	return command.requestOnce()
}

func (command apiCommand) requestOnce() ([]byte, error) {
	origin, err := serverOrigin(command.server)
	if err != nil {
		return nil, err
	}
	location, err := url.Parse(origin)
	if err != nil {
		return nil, errors.New("Invalid server origin")
	}
	if command.timeout <= 0 || command.timeout > requestTimeoutMax {
		return nil, errors.New("Timeout must be positive and at most 1m")
	}
	if !utf8.ValidString(command.actor) || len(command.actor) > actorBytesMax || strings.ContainsAny(command.actor, "\r\n\x00") {
		return nil, errors.New("Actor must be valid UTF-8, at most 80 bytes, and contain no header control characters")
	}
	location.Path = command.path
	// The path is constructed internally; queries are already escaped by url.Values.
	if path, query, found := strings.Cut(command.path, "?"); found {
		location.Path = path
		location.RawQuery = query
	}
	request, err := http.NewRequest(command.method, location.String(), bytes.NewReader(command.body))
	if err != nil {
		return nil, errors.New("Invalid HTTP request configuration")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if command.actor != "" {
		request.Header.Set("X-Docket-Actor", command.actor)
	}
	if command.version > 0 {
		request.Header.Set("If-Match", strconv.Quote(strconv.FormatInt(command.version, 10)))
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: command.timeout, IdleConnTimeout: command.timeout}
	transport.MaxConnsPerHost = 1
	transport.MaxIdleConns = 1
	transport.MaxResponseHeaderBytes = responseHeaderBytesMax
	transport.ResponseHeaderTimeout = command.timeout
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: command.timeout, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		if command.method != http.MethodGet {
			return nil, &cliFailure{Exit: exitUncertain, Code: "outcome_unknown", Message: "Request failed; the operation may already be committed. Read the record before retrying.", OutcomeUnknown: true}
		}
		return nil, &cliFailure{Exit: exitUnavailable, Code: "unavailable", Message: "Cannot reach Docket within the configured timeout. Check the server URL and service status."}
	}
	payload, readError := io.ReadAll(io.LimitReader(response.Body, responseBytesMax+1))
	closeError := response.Body.Close()
	if readError != nil || closeError != nil || len(payload) > responseBytesMax {
		if command.method != http.MethodGet {
			return nil, &cliFailure{Exit: exitUncertain, Code: "outcome_unknown", Message: "Response interrupted or too large; read the record before retrying the write.", OutcomeUnknown: true}
		}
		return nil, &cliFailure{Exit: exitUnavailable, Code: "invalid_response", Message: "Server response was interrupted or exceeded 16 MiB"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := &cliFailure{Exit: exitFailure, Code: "server_error", Message: fmt.Sprintf("Server returned HTTP %d", response.StatusCode), HTTPStatus: response.StatusCode}
		var problem struct {
			Error string `json:"error"`
		}
		if json.Unmarshal(payload, &problem) == nil && problem.Error != "" {
			failure.Message = problem.Error
		}
		switch response.StatusCode {
		case 400, 403, 415, 428:
			failure.Exit = exitUsage
			failure.Code = "invalid_request"
		case 404:
			failure.Exit = exitNotFound
			failure.Code = "not_found"
		case 409:
			failure.Exit = exitConflict
			failure.Code = "conflict"
		case 429, 503:
			failure.Exit = exitUnavailable
			failure.Code = "unavailable"
		}
		if command.method != http.MethodGet && (response.StatusCode >= 500 || response.StatusCode == 408 || (response.StatusCode >= 300 && response.StatusCode < 400)) {
			failure.Exit = exitUncertain
			failure.Code = "outcome_unknown"
			failure.OutcomeUnknown = true
			failure.Message += ". Read the record before retrying the write."
		}
		if value := response.Header.Get("Retry-After"); len(value) <= retryAfterBytesMax {
			failure.RetryAfter = value
		}
		return nil, failure
	}
	payload = bytes.TrimSpace(payload)
	if len(payload) == 0 || payload[0] != '{' || !utf8.Valid(payload) || !json.Valid(payload) {
		if command.method != http.MethodGet {
			return nil, &cliFailure{Exit: exitUncertain, Code: "outcome_unknown", Message: "Invalid success response; read the record before retrying the write.", OutcomeUnknown: true}
		}
		return nil, &cliFailure{Exit: exitFailure, Code: "invalid_response", Message: "Expected one JSON object from Docket"}
	}
	if command.path == "/healthz" {
		var health struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(payload, &health) != nil || health.Status != "ok" {
			return nil, &cliFailure{Exit: exitFailure, Code: "invalid_response", Message: "Docket health response did not report ok"}
		}
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, payload); err != nil {
		return nil, &cliFailure{Exit: exitFailure, Code: "invalid_response", Message: "Invalid server JSON"}
	}
	return append(compact.Bytes(), '\n'), nil
}
