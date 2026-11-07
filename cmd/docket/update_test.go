package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestUpdateCommandDiscovery(t *testing.T) {
	var output, diagnostics bytes.Buffer
	code := execute([]string{"update", "--help"}, strings.NewReader(""), &output, &diagnostics)
	if code != 0 || !strings.Contains(output.String(), "--force") || diagnostics.Len() != 0 {
		t.Fatalf("update help unavailable: exit=%d stdout=%q stderr=%q", code, output.String(), diagnostics.String())
	}
}

func TestStableUpdateVersions(t *testing.T) {
	for _, value := range []string{"dev", "", "0.1.0", "v01.2.3", "v1.2.3-beta", "v1.2.3\n", "v1.2.3/other", "v1.2.3?token=redacted"} {
		if _, valid := stableVersion(value); valid {
			t.Fatalf("accepted invalid version %q", value)
		}
	}
	for _, pair := range [][2]string{{"v1.9.0", "v1.10.0"}, {"v0.99.99", "v1.0.0"}, {"v1.2.99999999999999999999", "v1.3.0"}} {
		source, first := stableVersion(pair[0])
		target, second := stableVersion(pair[1])
		if !first || !second || compareVersions(source, target) >= 0 || compareVersions(target, source) <= 0 || compareVersions(source, source) != 0 {
			t.Fatalf("bad version order: %v", pair)
		}
	}
}

type updateTransport func(*http.Request) (*http.Response, error)

func (transport updateTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

var updateAssetName, _ = updateAssetFor(runtime.GOOS, runtime.GOARCH)

func TestSelfUpdate(t *testing.T) {
	directory := t.TempDir()
	build := func(version string) []byte {
		t.Helper()
		path := filepath.Join(directory, version)
		command := exec.Command("go", "build", "-ldflags", "-X main.buildVersion="+version, "-o", path, ".")
		command.Env = append(os.Environ(), "GOMAXPROCS=2")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("build candidate: %v %s", err, output)
		}
		if runtime.GOOS == "darwin" {
			if output, err := exec.Command("/usr/bin/codesign", "--force", "--sign", "-", "--timestamp=none", path).CombinedOutput(); err != nil {
				t.Fatalf("Sign fixture: %v %s", err, output)
			}
		}
		payload, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}
	original := build("v0.1.0")
	candidate := build("v0.2.0")
	for _, scenario := range []string{"success", "group", "forced-development", "current", "newer", "development", "checksum", "duplicate", "missing", "wrong-version", "wrong-platform", "oversize", "interrupted", "http-error", "draft", "prerelease", "lock", "concurrent-change", "lock-change", "symlink", "hardlink"} {
		t.Run(scenario, func(t *testing.T) {
			folder := t.TempDir()
			executable := filepath.Join(folder, "docket")
			if err := os.WriteFile(executable, original, 0o755); err != nil {
				t.Fatal(err)
			}
			if scenario == "group" {
				group := os.Getegid()
				if os.Geteuid() == 0 {
					group = 1
				} else {
					groups, err := os.Getgroups()
					if err != nil {
						t.Fatal(err)
					}
					for _, candidate := range groups {
						if candidate != group {
							group = candidate
							break
						}
					}
				}
				if group == os.Getegid() {
					t.Skip("No alternate group available for ownership verification")
				}
				if err := os.Chown(executable, -1, group); err != nil {
					t.Fatal(err)
				}
			}
			opened, err := os.Open(executable)
			if err != nil {
				t.Fatal(err)
			}
			defer opened.Close()
			version := "v0.1.0"
			force := false
			system := runtime.GOOS
			if scenario == "current" {
				version = "v0.2.0"
			}
			if scenario == "newer" {
				version = "v0.3.0"
			}
			if scenario == "development" || scenario == "forced-development" {
				version = "dev"
				force = scenario == "forced-development"
			}
			if scenario == "wrong-platform" {
				system = "windows"
			}
			payload := candidate
			if scenario == "wrong-version" {
				payload = original
			}
			digest := sha256.Sum256(payload)
			manifest := fmt.Sprintf("%x  %s\n", digest, updateAssetName)
			if scenario == "checksum" {
				manifest = strings.Repeat("0", sha256.Size*2) + "  " + updateAssetName + "\n"
			}
			if scenario == "duplicate" {
				manifest += manifest
			}
			if scenario == "missing" {
				manifest = ""
			}
			if scenario == "lock" {
				if err := os.WriteFile(filepath.Join(folder, ".docket.update.lock"), []byte("other updater"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "symlink" {
				if err := os.Symlink(executable, filepath.Join(folder, "link")); err != nil {
					t.Fatal(err)
				}
				executable = filepath.Join(folder, "link")
			}
			if scenario == "hardlink" {
				if err := os.Link(executable, filepath.Join(folder, "hardlink")); err != nil {
					t.Fatal(err)
				}
			}
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				requests.Add(1)
				if request.URL.Path == "/repos/dastagirS/docket/releases/latest" {
					size := len(payload)
					if scenario == "oversize" {
						size = updateBinaryBytesMax + 1
					}
					if scenario == "http-error" {
						writer.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					if err := json.NewEncoder(writer).Encode(map[string]any{"tag_name": "v0.2.0", "draft": scenario == "draft", "prerelease": scenario == "prerelease", "assets": []map[string]any{{"name": updateAssetName, "size": size}, {"name": "SHA256SUMS", "size": len(manifest)}}}); err != nil {
						t.Error(err)
					}
				} else if request.URL.Path == "/dastagirS/docket/releases/download/v0.2.0/SHA256SUMS" {
					if _, err := io.WriteString(writer, manifest); err != nil {
						t.Error(err)
					}
				} else if request.URL.Path == "/dastagirS/docket/releases/download/v0.2.0/"+updateAssetName {
					if scenario == "concurrent-change" {
						if err := os.WriteFile(executable, []byte("external change"), 0o755); err != nil {
							t.Error(err)
						}
					}
					if scenario == "lock-change" {
						lock := filepath.Join(folder, ".docket.update.lock")
						if err := os.Rename(lock, lock+".retained"); err != nil {
							t.Error(err)
						}
						if err := os.WriteFile(lock, []byte("new owner"), 0o600); err != nil {
							t.Error(err)
						}
					}
					data := payload
					if scenario == "interrupted" {
						data = data[:len(data)/2]
					}
					if _, err := writer.Write(data); err != nil {
						t.Error(err)
					}
				} else {
					t.Errorf("Unexpected request path %s", request.URL.Path)
					writer.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			location, err := url.Parse(server.URL)
			if err != nil {
				t.Fatal(err)
			}
			transport := server.Client().Transport
			client := &http.Client{Transport: updateTransport(func(request *http.Request) (*http.Response, error) {
				if request.URL.Scheme != "https" || (request.URL.Host != "api.github.com" && request.URL.Host != "github.com") {
					return nil, errors.New("unexpected production release origin")
				}
				clone := request.Clone(request.Context())
				address := *request.URL
				address.Host = location.Host
				clone.URL = &address
				return transport.RoundTrip(clone)
			}), Timeout: 10 * time.Second}
			result, err := (selfUpdater{executable, version, system, runtime.GOARCH, client}).update(context.Background(), force)
			success := scenario == "success" || scenario == "group" || scenario == "forced-development" || scenario == "current" || scenario == "newer"
			if success && err != nil {
				t.Fatal(err)
			}
			if !success && err == nil {
				t.Fatal("unsafe update unexpectedly succeeded")
			}
			contents, readError := os.ReadFile(executable)
			if readError != nil {
				t.Fatal(readError)
			}
			if scenario == "success" || scenario == "group" || scenario == "forced-development" {
				if result.Status != "updated" || !bytes.Equal(contents, candidate) {
					t.Fatal("candidate was not installed")
				}
				beforeInfo, err := opened.Stat()
				if err != nil {
					t.Fatal(err)
				}
				afterInfo, err := os.Stat(executable)
				if err != nil {
					t.Fatal(err)
				}
				if beforeInfo.Sys().(*syscall.Stat_t).Gid != afterInfo.Sys().(*syscall.Stat_t).Gid {
					t.Fatal("Executable group changed")
				}
				held, err := io.ReadAll(opened)
				if err != nil || !bytes.Equal(held, original) {
					t.Fatal("original inode was truncated")
				}
				versionOutput, err := exec.Command(executable, "version").Output()
				if err != nil || string(versionOutput) != "v0.2.0\n" {
					t.Fatalf("installed executable failed: %v %s", err, versionOutput)
				}
			} else if scenario == "concurrent-change" {
				if string(contents) != "external change" {
					t.Fatal("external executable edit overwritten")
				}
			} else if !bytes.Equal(contents, original) {
				t.Fatal("failure changed original executable")
			}
			if scenario == "development" || scenario == "wrong-platform" || scenario == "lock" || scenario == "symlink" || scenario == "hardlink" {
				if requests.Load() != 0 {
					t.Fatal("unsafe target contacted network")
				}
			}
			entries, err := os.ReadDir(folder)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".docket-update-") {
					t.Fatal("staging file leaked")
				}
				if entry.Name() == ".docket.update.lock" && scenario != "lock" && scenario != "lock-change" {
					t.Fatal("lock leaked")
				}
			}
		})
	}
	t.Run("real-command-running-server", func(t *testing.T) { verifyUpdateProcess(t, original, candidate) })
}

func verifyUpdateProcess(t *testing.T, original, candidate []byte) {
	t.Helper()
	folder := t.TempDir()
	if runtime.GOOS == "darwin" {
		path, err := os.Executable()
		if err != nil {
			t.Fatal(err)
		}
		original, err = os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"api.github.com", "github.com"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	encoded, err := x509.CreateCertificate(rand.Reader, certificate, certificate, public, private)
	if err != nil {
		t.Fatal(err)
	}
	certificatePath := filepath.Join(folder, "certificate.pem")
	if err := os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: encoded}), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(candidate)
	manifest := fmt.Sprintf("%x  %s\n", digest, updateAssetName)
	var redirectMode atomic.Int32
	cancellationStarted := make(chan struct{}, 1)
	release := httptest.NewUnstartedServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if redirectMode.Load() == 1 {
			http.Redirect(writer, request, "http://github.com/unsafe", http.StatusFound)
			return
		}
		if redirectMode.Load() == 2 {
			http.Redirect(writer, request, "https://api.github.com"+request.URL.Path, http.StatusFound)
			return
		}
		switch request.URL.Path {
		case "/repos/dastagirS/docket/releases/latest":
			if err := json.NewEncoder(writer).Encode(map[string]any{"tag_name": "v0.2.0", "draft": false, "prerelease": false, "assets": []map[string]any{{"name": updateAssetName, "size": len(candidate)}, {"name": "SHA256SUMS", "size": len(manifest)}}}); err != nil {
				t.Error(err)
			}
		case "/dastagirS/docket/releases/download/v0.2.0/SHA256SUMS":
			if _, err := io.WriteString(writer, manifest); err != nil {
				t.Error(err)
			}
		case "/dastagirS/docket/releases/download/v0.2.0/" + updateAssetName:
			if redirectMode.Load() == 3 {
				cancellationStarted <- struct{}{}
				<-request.Context().Done()
				return
			}
			if _, err := writer.Write(candidate); err != nil {
				t.Error(err)
			}
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	release.TLS = &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{encoded}, PrivateKey: private}}}
	release.StartTLS()
	defer release.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect || (request.Host != "api.github.com:443" && request.Host != "github.com:443") {
			t.Error("Unexpected updater proxy destination")
			writer.WriteHeader(http.StatusForbidden)
			return
		}
		upstream, err := net.DialTimeout("tcp", release.Listener.Addr().String(), requestTimeoutDefault)
		if err != nil {
			t.Error(err)
			writer.WriteHeader(http.StatusBadGateway)
			return
		}
		defer upstream.Close()
		connection, buffer, err := writer.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		defer connection.Close()
		if err := connection.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		if err := upstream.SetDeadline(time.Now().Add(10 * time.Second)); err != nil {
			t.Error(err)
			return
		}
		if _, err := buffer.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
			t.Error(err)
			return
		}
		if err := buffer.Flush(); err != nil {
			t.Error(err)
			return
		}
		finished := make(chan error, 1)
		go func() { _, err := io.Copy(upstream, connection); finished <- err }()
		_, receiveError := io.Copy(connection, upstream)
		connection.Close()
		upstream.Close()
		sendError := <-finished
		for _, err := range []error{receiveError, sendError} {
			if err != nil && !errors.Is(err, net.ErrClosed) && !errors.Is(err, syscall.EPIPE) && !errors.Is(err, syscall.ECONNRESET) {
				t.Error(err)
			}
		}
	}))
	defer proxy.Close()
	for _, scenario := range []string{"normal-output", "closed-output", "http-redirect", "redirect-loop", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			redirectMode.Store(0)
			if scenario == "http-redirect" {
				redirectMode.Store(1)
			}
			if scenario == "redirect-loop" {
				redirectMode.Store(2)
			}
			if scenario == "cancelled" {
				redirectMode.Store(3)
			}
			home := t.TempDir()
			executable := filepath.Join(home, "docket")
			if err := os.WriteFile(executable, original, 0o755); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			arguments := []string{"serve", "--database", filepath.Join(home, "fixture.sqlite"), "--listen", "127.0.0.1:0"}
			if runtime.GOOS == "darwin" {
				arguments = append([]string{"-test.run=^TestUpdateProcessEntry$", "--"}, arguments...)
			}
			server := exec.CommandContext(ctx, executable, arguments...)
			if runtime.GOOS == "darwin" {
				server.Env = append(os.Environ(), "DOCKET_UPDATE_TEST_ENTRY=1", "SSL_CERT_FILE="+certificatePath, "GODEBUG=x509usefallbackroots=1")
			}
			diagnostics, err := server.StderrPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := server.Process.Signal(syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
					t.Error(err)
				}
				if err := server.Wait(); err != nil {
					t.Error(err)
				}
			}()
			readiness := make(chan string, 1)
			go func() {
				scanner := bufio.NewScanner(diagnostics)
				scanner.Buffer(make([]byte, 4096), 16*1024)
				if scanner.Scan() {
					readiness <- scanner.Text()
				} else {
					readiness <- ""
				}
			}()
			var line string
			select {
			case line = <-readiness:
			case <-ctx.Done():
				t.Fatal("Isolated server readiness timeout")
			}
			match := regexp.MustCompile(`url=(http://127\.0\.0\.1:\d+)`).FindStringSubmatch(line)
			if len(match) != 2 {
				t.Fatalf("Missing readiness URL: %s", line)
			}
			oldInfo, err := os.Stat(executable)
			if err != nil {
				t.Fatal(err)
			}
			updateArguments := []string{"update"}
			if runtime.GOOS == "darwin" {
				updateArguments = append([]string{"-test.run=^TestUpdateProcessEntry$", "--"}, updateArguments...)
			}
			command := exec.CommandContext(ctx, executable, updateArguments...)
			command.Env = append(os.Environ(), "HOME="+home, "HTTPS_PROXY="+proxy.URL, "HTTP_PROXY="+proxy.URL, "NO_PROXY=", "SSL_CERT_FILE="+certificatePath, "SSL_CERT_DIR="+home)
			if runtime.GOOS == "darwin" {
				command.Env = append(command.Env, "DOCKET_UPDATE_TEST_ENTRY=1", "GODEBUG=x509usefallbackroots=1")
			}
			var output, errorsOutput bytes.Buffer
			command.Stdout = &output
			command.Stderr = &errorsOutput
			if scenario == "closed-output" {
				reader, full, err := os.Pipe()
				if err == nil {
					err = reader.Close()
				}
				if err != nil {
					t.Fatal(err)
				}
				defer full.Close()
				command.Stdout = full
			}
			if scenario == "cancelled" {
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				select {
				case <-cancellationStarted:
				case <-ctx.Done():
					t.Fatal("Update cancellation fixture timed out")
				}
				if err := command.Process.Signal(os.Interrupt); err != nil {
					t.Fatal(err)
				}
				err = command.Wait()
			} else {
				err = command.Run()
			}
			if scenario == "normal-output" {
				if err != nil {
					t.Fatalf("real CLI update failed: %v %s", err, errorsOutput.String())
				}
				var result updateResult
				if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Status != "updated" {
					t.Fatalf("Invalid command result: %s", output.String())
				}
			} else if scenario == "closed-output" {
				var failure *exec.ExitError
				if !errors.As(err, &failure) || failure.ExitCode() != exitUncertain || !strings.Contains(errorsOutput.String(), "outcome_unknown") {
					t.Fatalf("Committed update output failure not reported: %v %s", err, errorsOutput.String())
				}
			} else {
				var failure *exec.ExitError
				if !errors.As(err, &failure) || failure.ExitCode() != exitUnavailable {
					t.Fatalf("Unsafe redirect accepted: %v %s", err, errorsOutput.String())
				}
			}
			current, err := os.ReadFile(executable)
			expected := candidate
			if scenario == "http-redirect" || scenario == "redirect-loop" || scenario == "cancelled" {
				expected = original
			}
			if err != nil || !bytes.Equal(current, expected) {
				t.Fatal("Real command executable contents are incorrect")
			}
			entries, readError := os.ReadDir(home)
			if readError != nil {
				t.Fatal(readError)
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".docket-update-") || entry.Name() == ".docket.update.lock" {
					t.Fatal("Real command left updater staging/lock files")
				}
			}
			if runtime.GOOS == "linux" {
				running, err := os.Stat("/proc/" + strconv.Itoa(server.Process.Pid) + "/exe")
				if err != nil || !os.SameFile(oldInfo, running) {
					t.Fatal("Running server lost its original executable inode")
				}
			}
			response, err := (&http.Client{Timeout: requestTimeoutDefault}).Get(match[1] + "/healthz")
			if err != nil {
				t.Fatal(err)
			}
			closeError := response.Body.Close()
			if response.StatusCode != http.StatusOK || closeError != nil {
				t.Fatal("Running server was interrupted")
			}
		})
	}
}

func TestUpdateHelpOutputFailure(t *testing.T) {
	var diagnostics bytes.Buffer
	if code := execute([]string{"update", "--help"}, strings.NewReader(""), brokenWriter{}, &diagnostics); code != exitFailure {
		t.Fatalf("help output error exit=%d, want %d", code, exitFailure)
	}
}

// macOS uses system trust instead of SSL_CERT_FILE; only test subprocesses opt into a fixture CA.
func TestUpdateProcessEntry(t *testing.T) {
	if os.Getenv("DOCKET_UPDATE_TEST_ENTRY") != "1" {
		return
	}
	certificate, err := os.ReadFile(os.Getenv("SSL_CERT_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(certificate) {
		t.Fatal("Invalid fixture CA")
	}
	x509.SetFallbackRoots(roots)
	buildVersion = "v0.1.0"
	signal.Ignore(syscall.SIGPIPE)
	for i, argument := range os.Args {
		if argument == "--" {
			os.Exit(execute(os.Args[i+1:], os.Stdin, os.Stdout, os.Stderr))
		}
	}
	t.Fatal("Missing fixture command separator")
}

func TestUpdatePlatformSelection(t *testing.T) {
	for _, item := range []struct{ system, architecture, asset string }{
		{"linux", "amd64", "docket-linux-amd64"}, {"darwin", "arm64", "docket-darwin-arm64"},
		{"linux", "arm64", ""}, {"darwin", "amd64", ""}, {"windows", "amd64", ""},
	} {
		asset, err := updateAssetFor(item.system, item.architecture)
		if asset != item.asset || (err != nil) != (item.asset == "") {
			t.Fatalf("Unexpected platform selection: %s/%s %s %v", item.system, item.architecture, asset, err)
		}
	}
}
