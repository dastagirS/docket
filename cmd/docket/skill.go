package main

import (
	"errors"
	"io"
	"io/fs"
	"path"
	"strings"
)

const skillBytesMax = 128 * 1024
const skillReferencesMax = 32
const skillUsage = "Usage: docket --skill [--references [filename]]\n"

func skillOutput(arguments []string, bundle fs.FS) ([]byte, error) {
	document := "docket-cli/SKILL.md"
	if (len(arguments) == 1 && (arguments[0] == "--help" || arguments[0] == "-h")) || (len(arguments) == 2 && arguments[0] == "--references" && (arguments[1] == "--help" || arguments[1] == "-h")) {
		return []byte(skillUsage), nil
	}
	if len(arguments) > 0 {
		if arguments[0] != "--references" || len(arguments) > 2 {
			return nil, errors.New("Use docket --skill [--references [filename]]")
		}
		entries, err := fs.ReadDir(bundle, "docket-cli/references")
		if err != nil {
			return nil, &cliFailure{Exit: exitFailure, Code: "skill_unavailable", Message: "Cannot read bundled skill references"}
		}
		if len(entries) > skillReferencesMax {
			return nil, &cliFailure{Exit: exitFailure, Code: "invalid_skill_bundle", Message: "Bundled skill exceeds the reference limit"}
		}
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".md") {
				names = append(names, entry.Name())
			}
		}
		name := ""
		if len(arguments) == 2 {
			name = arguments[1]
			if !fs.ValidPath(name) || path.Base(name) != name || strings.Contains(name, "\\") {
				return nil, errors.New("Reference must be one listed filename, not a path")
			}
		} else if len(names) == 1 {
			name = names[0]
		} else {
			var listing strings.Builder
			listing.WriteString(skillUsage)
			listing.WriteString("\nAvailable references:\n")
			for _, name := range names {
				listing.WriteString("  " + name + "\n")
			}
			if len(names) == 0 {
				listing.WriteString("  (none)\n")
			}
			if listing.Len() > skillBytesMax {
				return nil, &cliFailure{Exit: exitFailure, Code: "invalid_skill_bundle", Message: "Bundled reference index is too large"}
			}
			return []byte(listing.String()), nil
		}
		found := false
		for _, candidate := range names {
			if candidate == name {
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("Unknown reference; list available filenames with docket --skill --references")
		}
		document = "docket-cli/references/" + name
	}
	file, err := bundle.Open(document)
	if err != nil {
		return nil, &cliFailure{Exit: exitFailure, Code: "skill_unavailable", Message: "Cannot read bundled skill document"}
	}
	payload, readError := io.ReadAll(io.LimitReader(file, skillBytesMax+1))
	closeError := file.Close()
	if readError != nil || closeError != nil || len(payload) > skillBytesMax {
		return nil, &cliFailure{Exit: exitFailure, Code: "invalid_skill_bundle", Message: "Bundled skill document is unreadable or exceeds 128 KiB"}
	}
	return payload, nil
}
