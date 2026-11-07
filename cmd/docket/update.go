package main

import (
	"context"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/macho"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
)

var buildVersion = "dev"

const (
	updateRepository       = "https://github.com/dastagirS/docket"
	updateMetadataURL      = "https://api.github.com/repos/dastagirS/docket/releases/latest"
	updateMetadataBytesMax = 1024 * 1024
	updateManifestBytesMax = 16 * 1024
	updateBinaryBytesMax   = 64 * 1024 * 1024
	updateAssetsMax        = 1000
	updateRedirectsMax     = 3
	updateTimeout          = 3 * time.Minute
	updateDownloadTimeout  = 2 * time.Minute
	updateTagBytesMax      = 64
	updateELFHeaderBytes   = 64
	updateMachineOffset    = 18
	updateMachineAMD64     = 62
	updateHelp             = "Usage: docket update [--force] [--json]\n\nDownload the latest stable GitHub release and atomically replace this executable.\n--force allows development/unversioned builds to be replaced by a stable release.\nEqual/newer stable versions are left alone. No sudo, database changes, or service restart.\n"
)

type updateResult struct {
	Status         string `json:"status"`
	CurrentVersion string `json:"current_version"`
	LatestVersion  string `json:"latest_version"`
	Executable     string `json:"executable"`
	ServiceRestart string `json:"service_restart"`
}

type selfUpdater struct {
	executable      string
	version         string
	operatingSystem string
	architecture    string
	client          *http.Client
}

type releaseMetadata struct {
	Tag        string `json:"tag_name"`
	Draft      *bool  `json:"draft"`
	Prerelease *bool  `json:"prerelease"`
	Assets     []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
	} `json:"assets"`
}

func updateAssetFor(system, architecture string) (string, error) {
	switch system + "/" + architecture {
	case "linux/amd64":
		return "docket-linux-amd64", nil
	case "darwin/arm64":
		return "docket-darwin-arm64", nil
	default:
		return "", errors.New("Supported updates: Linux x86-64 and Apple Silicon macOS")
	}
}

func stableVersion(value string) ([3]string, bool) {
	var result [3]string
	if len(value) > updateTagBytesMax || !strings.HasPrefix(value, "v") {
		return result, false
	}
	parts := strings.Split(value[1:], ".")
	if len(parts) != len(result) {
		return result, false
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, false
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return result, false
			}
		}
		result[i] = part
	}
	return result, true
}

func compareVersions(source, target [3]string) int {
	for i := range source {
		if len(source[i]) < len(target[i]) {
			return -1
		}
		if len(source[i]) > len(target[i]) {
			return 1
		}
		if comparison := strings.Compare(source[i], target[i]); comparison != 0 {
			return comparison
		}
	}
	return 0
}

func executeUpdate(arguments []string, output, diagnostics io.Writer) int {
	flags := flag.NewFlagSet("docket update", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	force := flags.Bool("force", false, "allow development builds")
	jsonOutput := flags.Bool("json", true, "JSON output")
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			if _, err := io.WriteString(output, updateHelp); err != nil {
				return emitFailure(diagnostics, &cliFailure{Exit: exitFailure, Code: "output_error", Message: "Cannot write update help"})
			}
			return 0
		}
		return emitFailure(diagnostics, errors.New("Invalid update flags; use docket update --help"))
	}
	if flags.NArg() != 0 || !*jsonOutput {
		return emitFailure(diagnostics, errors.New("Use docket update [--force] [--json]"))
	}
	executable, err := os.Executable()
	if err != nil {
		return emitFailure(diagnostics, &cliFailure{Exit: exitFailure, Code: "update_error", Message: "Cannot determine the running executable"})
	}
	transport := &http.Transport{Proxy: http.ProxyFromEnvironment, TLSHandshakeTimeout: requestTimeoutDefault, ResponseHeaderTimeout: requestTimeoutDefault, MaxResponseHeaderBytes: responseHeaderBytesMax, MaxConnsPerHost: 1, MaxIdleConns: 1, IdleConnTimeout: requestTimeoutDefault}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: updateDownloadTimeout, Transport: transport, CheckRedirect: func(request *http.Request, previous []*http.Request) error {
		if len(previous) > updateRedirectsMax || request.URL.Scheme != "https" || request.URL.User != nil {
			return errors.New("Updater refused an unsafe or excessive redirect")
		}
		return nil
	}}
	signalContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(signalContext, updateTimeout)
	defer cancel()
	result, err := (selfUpdater{executable, buildVersion, runtime.GOOS, runtime.GOARCH, client}).update(ctx, *force)
	if err != nil {
		return emitFailure(diagnostics, err)
	}
	if err := json.NewEncoder(output).Encode(result); err != nil {
		if result.Status == "updated" {
			return emitFailure(diagnostics, &cliFailure{Exit: exitUncertain, Code: "outcome_unknown", Message: "Executable was updated but output failed; inspect docket version before retrying", OutcomeUnknown: true})
		}
		return emitFailure(diagnostics, &cliFailure{Exit: exitFailure, Code: "output_error", Message: "Cannot write update result"})
	}
	return 0
}

func (updater selfUpdater) download(ctx context.Context, location string, target io.Writer, bytesMax int64) (int64, error) {
	parsed, err := url.Parse(location)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil {
		return 0, errors.New("Updater requires credential-free HTTPS downloads")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return 0, errors.New("Cannot construct release download")
	}
	request.Header.Set("User-Agent", "docket-self-update")
	request.Header.Set("Cache-Control", "no-cache")
	response, err := updater.client.Do(request)
	if err != nil {
		return 0, &cliFailure{Exit: exitUnavailable, Code: "update_download", Message: "Release download failed; the existing executable was not changed"}
	}
	if response.StatusCode != http.StatusOK {
		closeError := response.Body.Close()
		return 0, errors.Join(fmt.Errorf("Release download returned HTTP %d", response.StatusCode), closeError)
	}
	size, copyError := io.Copy(target, io.LimitReader(response.Body, bytesMax+1))
	closeError := response.Body.Close()
	if copyError != nil || closeError != nil || size > bytesMax {
		return 0, errors.New("Release download interrupted or exceeded its size limit; existing executable unchanged")
	}
	return size, nil
}

func removeOwnedUpdateFile(path string, original os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(original, current) {
		return errors.New("Updater cleanup refused a replaced temporary file or lock")
	}
	return os.Remove(path)
}

func executableDigest(file *os.File) (string, error) {
	digest := sha256.New()
	size, err := io.Copy(digest, io.NewSectionReader(file, 0, updateBinaryBytesMax+1))
	if err != nil || size > updateBinaryBytesMax {
		return "", errors.New("Cannot safely inspect the current executable")
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func verifyUpdateBinary(ctx context.Context, file *os.File, version, system, architecture string) error {
	header := make([]byte, updateELFHeaderBytes)
	if _, err := file.ReadAt(header, 0); err != nil {
		return errors.New("Release binary has an incomplete ELF header")
	}
	if system == "linux" && architecture == "amd64" {
		if string(header[:6]) != "\x7fELF\x02\x01" || (header[7] != 0 && header[7] != 3) || binary.LittleEndian.Uint16(header[updateMachineOffset:]) != updateMachineAMD64 {
			return errors.New("Release is not a Linux x86-64 executable")
		}
	} else if system == "darwin" && architecture == "arm64" {
		image, err := macho.Open(file.Name())
		if err != nil {
			return errors.New("Release is not a Mach-O executable")
		}
		valid := image.Magic == macho.Magic64 && image.Cpu == macho.CpuArm64 && image.Type == macho.TypeExec
		closeError := image.Close()
		if !valid || closeError != nil {
			return errors.New("Release is not an Apple Silicon macOS executable")
		}
		verificationContext, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if err := exec.CommandContext(verificationContext, "/usr/bin/codesign", "--verify", "--strict", file.Name()).Run(); err != nil {
			return errors.New("macOS release signature validation failed")
		}
	} else {
		return errors.New("Unsupported update platform")
	}
	information, err := buildinfo.ReadFile(file.Name())
	if err != nil || information.Path != "docket/cmd/docket" {
		return errors.New("Release does not contain Docket Go build metadata")
	}
	for _, setting := range information.Settings {
		if setting.Key != "-ldflags" {
			continue
		}
		fields := strings.Fields(setting.Value)
		for i := 1; i < len(fields); i++ {
			if fields[i-1] == "-X" && fields[i] == "main.buildVersion="+version {
				return nil
			}
		}
	}
	return errors.New("Release binary version does not match the selected release")
}

func (updater selfUpdater) update(ctx context.Context, force bool) (result updateResult, resultError error) {
	assetName, err := updateAssetFor(updater.operatingSystem, updater.architecture)
	if err != nil {
		return result, err
	}
	currentVersion, currentStable := stableVersion(updater.version)
	if !currentStable && !force {
		return result, errors.New("Development/unversioned builds require docket update --force; latest stable may lack local changes")
	}
	defer func() {
		if resultError == nil {
			return
		}
		var known *cliFailure
		if result.Status == "updated" {
			resultError = &cliFailure{Exit: exitUncertain, Code: "outcome_unknown", Message: "Executable replacement completed but finalization failed; inspect docket version and update lock before retrying", OutcomeUnknown: true}
		} else if !errors.As(resultError, &known) {
			resultError = &cliFailure{Exit: exitFailure, Code: "update_error", Message: resultError.Error()}
		}
	}()
	original, err := os.Lstat(updater.executable)
	if err != nil || !original.Mode().IsRegular() || original.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
		return result, errors.New("Updater requires a regular, non-privileged executable (not a symlink)")
	}
	ownership, ok := original.Sys().(*syscall.Stat_t)
	if !ok || ownership.Nlink != 1 || ownership.Uid != uint32(os.Geteuid()) {
		return result, errors.New("Updater requires an executable owned by this user with exactly one hard link")
	}
	if err := checkUpdatePermissions(updater.executable); err != nil {
		return result, err
	}
	opened, err := os.OpenFile(updater.executable, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return result, err
	}
	defer func() { resultError = errors.Join(resultError, opened.Close()) }()
	openedInfo, err := opened.Stat()
	if err != nil || !os.SameFile(original, openedInfo) {
		return result, errors.New("Executable changed before update inspection")
	}
	originalDigest, err := executableDigest(opened)
	if err != nil {
		return result, err
	}
	directory := filepath.Dir(updater.executable)
	lockPath := filepath.Join(directory, "."+filepath.Base(updater.executable)+".update.lock")
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return result, errors.New("Cannot reserve update lock; check directory permissions or inspect an existing lock before retrying")
	}
	lockInfo, statError := lock.Stat()
	_, writeError := fmt.Fprintf(lock, "pid=%d\n", os.Getpid())
	closeError := lock.Close()
	if statError != nil {
		return result, errors.Join(statError, writeError, closeError)
	}
	defer func() {
		if err := removeOwnedUpdateFile(lockPath, lockInfo); err != nil {
			if result.Status == "updated" {
				resultError = &cliFailure{Exit: exitUncertain, Code: "outcome_unknown", Message: "Executable updated but lock cleanup failed; inspect before retrying", OutcomeUnknown: true}
			} else {
				resultError = errors.Join(resultError, err)
			}
		}
	}()
	if writeError != nil || closeError != nil {
		return result, errors.Join(writeError, closeError)
	}
	var metadataBuffer strings.Builder
	if _, err := updater.download(ctx, updateMetadataURL, &metadataBuffer, updateMetadataBytesMax); err != nil {
		return result, err
	}
	var release releaseMetadata
	if err := json.Unmarshal([]byte(metadataBuffer.String()), &release); err != nil {
		return result, errors.New("Invalid GitHub release metadata")
	}
	latestVersion, stable := stableVersion(release.Tag)
	if !stable || release.Draft == nil || *release.Draft || release.Prerelease == nil || *release.Prerelease {
		return result, errors.New("GitHub did not return a published stable release")
	}
	result = updateResult{Status: "up_to_date", CurrentVersion: updater.version, LatestVersion: release.Tag, Executable: updater.executable, ServiceRestart: "not_restarted"}
	if currentStable && compareVersions(currentVersion, latestVersion) >= 0 {
		return result, nil
	}
	if len(release.Assets) > updateAssetsMax {
		return result, errors.New("Release asset metadata exceeds its limit")
	}
	var binarySize int64
	binaryCount, manifestCount := 0, 0
	for _, asset := range release.Assets {
		if asset.Name == assetName {
			binaryCount++
			binarySize = asset.Size
		}
		if asset.Name == "SHA256SUMS" {
			manifestCount++
			if asset.Size <= 0 || asset.Size > updateManifestBytesMax {
				return result, errors.New("Invalid release checksum size")
			}
		}
	}
	if binaryCount != 1 || manifestCount != 1 || binarySize < updateELFHeaderBytes || binarySize > updateBinaryBytesMax {
		return result, errors.New("Release must contain one bounded executable and SHA256SUMS")
	}
	releaseURL := updateRepository + "/releases/download/" + release.Tag + "/"
	var manifest strings.Builder
	if _, err := updater.download(ctx, releaseURL+"SHA256SUMS", &manifest, updateManifestBytesMax); err != nil {
		return result, err
	}
	expected := ""
	for _, line := range strings.Split(manifest.String(), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 || fields[1] != assetName {
			continue
		}
		if len(fields) != 2 || expected != "" || len(fields[0]) != sha256.Size*2 {
			return result, errors.New("Duplicate or malformed executable checksum")
		}
		decoded, err := hex.DecodeString(fields[0])
		if err != nil || len(decoded) != sha256.Size {
			return result, errors.New("Invalid executable checksum")
		}
		expected = strings.ToLower(fields[0])
	}
	if expected == "" {
		return result, errors.New("Release is missing its executable checksum")
	}
	staged, err := os.CreateTemp(directory, ".docket-update-*")
	if err != nil {
		return result, err
	}
	stagedInfo, err := staged.Stat()
	if err != nil {
		return result, errors.Join(err, staged.Close())
	}
	defer func() { resultError = errors.Join(resultError, removeOwnedUpdateFile(staged.Name(), stagedInfo)) }()
	if err := checkUpdatePermissions(staged.Name()); err != nil {
		return result, errors.Join(err, staged.Close())
	}
	digest := sha256.New()
	size, downloadError := updater.download(ctx, releaseURL+assetName, io.MultiWriter(staged, digest), updateBinaryBytesMax)
	if downloadError == nil && (size != binarySize || hex.EncodeToString(digest.Sum(nil)) != expected) {
		downloadError = errors.New("Release size or SHA-256 mismatch; existing executable unchanged")
	}
	if downloadError == nil {
		downloadError = verifyUpdateBinary(ctx, staged, release.Tag, updater.operatingSystem, updater.architecture)
	}
	if downloadError == nil {
		downloadError = staged.Chown(-1, int(ownership.Gid))
	}
	if downloadError == nil {
		downloadError = staged.Chmod(original.Mode().Perm())
	}
	if downloadError == nil {
		downloadError = staged.Sync()
	}
	if err := errors.Join(downloadError, staged.Close()); err != nil {
		return result, err
	}
	current, err := os.Lstat(updater.executable)
	if err != nil || !os.SameFile(original, current) || current.Mode() != original.Mode() {
		return result, errors.New("Executable changed during download; refusing to overwrite it")
	}
	currentOwnership, ok := current.Sys().(*syscall.Stat_t)
	if !ok || currentOwnership.Uid != ownership.Uid || currentOwnership.Gid != ownership.Gid || currentOwnership.Nlink != ownership.Nlink {
		return result, errors.New("Executable ownership changed during download; refusing replacement")
	}
	if err := checkUpdatePermissions(updater.executable); err != nil {
		return result, err
	}
	currentDigest, err := executableDigest(opened)
	if err != nil || currentDigest != originalDigest {
		return result, errors.New("Executable contents changed during download; refusing to overwrite them")
	}
	currentLock, err := os.Lstat(lockPath)
	if err != nil || !os.SameFile(lockInfo, currentLock) {
		return result, errors.New("Update lock changed; refusing replacement")
	}
	if err := ctx.Err(); err != nil {
		return result, &cliFailure{Exit: exitUnavailable, Code: "update_cancelled", Message: "Update cancelled before replacement; existing executable unchanged"}
	}
	if err := os.Rename(staged.Name(), updater.executable); err != nil {
		return result, err
	}
	result.Status = "updated"
	if err := syncFolder(directory); err != nil {
		return result, &cliFailure{Exit: exitUncertain, Code: "outcome_unknown", Message: "Executable replaced but directory sync failed; inspect docket version before retrying", OutcomeUnknown: true}
	}
	return result, nil
}
